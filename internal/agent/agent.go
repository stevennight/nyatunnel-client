// Package agent is the device side of a NyaTunnel session: it keeps one WebSocket to the server
// alive, applies configuration snapshots and forwards data streams to local services
// (nyatunnel-server/docs/协议.md §4).
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"github.com/stevennight/nyatunnel-common/tunnelproto"

	"nyatunnel-client/internal/identity"
	"nyatunnel-client/internal/store"
)

// Fatal errors end Run: the device must not keep reconnecting.
var (
	ErrRevoked         = errors.New("the server no longer accepts this device (revoked, or its owner was disabled)")
	ErrUpgradeRequired = errors.New("the server requires a newer client version")
)

// State is a snapshot for status displays.
type State struct {
	Connected   bool
	ConnectedAt time.Time
	// Transport is "direct" (pinned TLS to the server's direct listener) or "proxy" (WebSocket via
	// the public URL).
	Transport string
	LastError string
	Config    *tunnelproto.Config
	// TunnelErrors holds the last local error per tunnel id (e.g. local service unreachable).
	TunnelErrors map[string]string
	// Confirmed says per tunnel id whether the device owner confirmed the tunnel's current local
	// target (协议.md §4.6); unconfirmed tunnels are refused.
	Confirmed map[string]bool
}

// Agent runs a device.
type Agent struct {
	id      *identity.Identity
	version string
	gui     bool
	log     *slog.Logger
	dial    func(ctx context.Context, url string, opts *websocket.DialOptions) (*websocket.Conn, error)
	store   *store.Store // nil: nothing is persisted and no tunnel is confirmed

	direct      *store.Direct
	directAfter time.Time // do not retry the direct path before this
	legacy      bool      // legacy files may still be around (dropped after the first connection)

	mu       sync.Mutex
	state    State
	ctl      *tunnelproto.Control
	pending  map[string]chan tunnelproto.Result
	watchers []chan State
	nextID   atomic.Int64
	stats    map[string]*counter
	confirms map[string]store.Confirmation
}

type counter struct {
	in, out atomic.Int64
	conns   atomic.Int64
}

// Options configures an Agent.
type Options struct {
	Identity *identity.Identity
	Version  string
	GUI      bool
	Log      *slog.Logger
	// Store keeps the direct endpoint, the confirmations and the last configuration. Without it
	// the agent persists nothing and serves no tunnel (every tunnel counts as unconfirmed).
	Store *store.Store
}

// New returns an agent; call Run.
func New(o Options) *Agent {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	a := &Agent{
		id: o.Identity, version: o.Version, gui: o.GUI, log: o.Log, pending: map[string]chan tunnelproto.Result{},
		stats: map[string]*counter{}, state: State{TunnelErrors: map[string]string{}, Confirmed: map[string]bool{}}, store: o.Store,
		confirms: map[string]store.Confirmation{},
		dial: func(ctx context.Context, u string, opts *websocket.DialOptions) (*websocket.Conn, error) {
			opts.HTTPHeader = map[string][]string{"User-Agent": {"nyatunnel/" + o.Version}}
			c, _, err := websocket.Dial(ctx, u, opts)
			return c, err
		},
	}
	if o.Store != nil {
		a.direct = o.Store.Direct()
		a.legacy = true
		if cs, err := o.Store.Confirmations(); err == nil {
			a.confirms = cs
		}
	}
	return a
}

// State returns the current state.
func (a *Agent) State() State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snapshot()
}

func (a *Agent) snapshot() State {
	s := a.state
	s.TunnelErrors = make(map[string]string, len(a.state.TunnelErrors))
	for k, v := range a.state.TunnelErrors {
		s.TunnelErrors[k] = v
	}
	s.Confirmed = make(map[string]bool, len(a.state.Confirmed))
	for k, v := range a.state.Confirmed {
		s.Confirmed[k] = v
	}
	return s
}

// Watch returns a channel that receives the state after every change (latest value wins).
func (a *Agent) Watch() <-chan State {
	ch := make(chan State, 1)
	a.mu.Lock()
	a.watchers = append(a.watchers, ch)
	ch <- a.snapshot()
	a.mu.Unlock()
	return ch
}

func (a *Agent) changed() {
	s := a.snapshot()
	for _, ch := range a.watchers {
		select {
		case <-ch:
		default:
		}
		ch <- s
	}
}

func (a *Agent) setState(f func(s *State)) {
	a.mu.Lock()
	f(&a.state)
	a.changed()
	a.mu.Unlock()
}

// Run connects and reconnects until ctx ends or the server refuses the device for good.
func (a *Agent) Run(ctx context.Context) error {
	if a.store != nil {
		go a.watchConfirmations(ctx)
	}
	backoff := time.Second
	for {
		started := time.Now()
		err := a.session(ctx, nil)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch websocket.CloseStatus(err) {
		case tunnelproto.CloseUnauthorized:
			a.setState(func(s *State) { s.LastError = ErrRevoked.Error() })
			return ErrRevoked
		case tunnelproto.CloseUpgradeRequired:
			a.setState(func(s *State) { s.LastError = ErrUpgradeRequired.Error() })
			return ErrUpgradeRequired
		case tunnelproto.CloseReplaced:
			// Another process with the same identity took over; do not fight over the session.
			backoff = time.Minute
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second // the last session was healthy: retry quickly
		}
		msg := "disconnected"
		if err != nil {
			msg = err.Error()
		}
		a.setState(func(s *State) { s.Connected, s.LastError = false, msg })
		wait := backoff/2 + rand.N(backoff/2+1)
		a.log.Info("reconnecting", "in", wait.Round(time.Millisecond), "reason", msg)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

// Once connects, waits for the first configuration and disconnects (for `nyatunnel status`).
func (a *Agent) Once(ctx context.Context) (*tunnelproto.Config, error) {
	got := make(chan *tunnelproto.Config, 1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- a.session(ctx, got) }()
	select {
	case cfg := <-got:
		return cfg, nil
	case err := <-errc:
		if websocket.CloseStatus(err) == tunnelproto.CloseUnauthorized {
			return nil, ErrRevoked
		}
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// connect dials the direct endpoint when one is known and not recently failing, else the public URL.
func (a *Agent) connect(ctx context.Context) (*websocket.Conn, string, error) {
	a.mu.Lock()
	d, after := a.direct, a.directAfter
	a.mu.Unlock()
	if d != nil && time.Now().After(after) {
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ws, err := a.dial(dctx, "wss://"+d.Addr+tunnelproto.ConnectPath, &websocket.DialOptions{
			HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: tunnelproto.PinnedTLS(d.CertSHA256)}},
		})
		cancel()
		if err == nil {
			return ws, "direct", nil
		}
		a.log.Info("direct connection failed; using the public address", "addr", d.Addr, "err", err)
		a.mu.Lock()
		a.directAfter = time.Now().Add(10 * time.Minute)
		a.mu.Unlock()
	}
	ws, err := a.dial(ctx, connectURL(a.id.Server), &websocket.DialOptions{})
	return ws, "proxy", err
}

// rememberDirect stores the server's direct endpoint from a configuration snapshot.
func (a *Agent) rememberDirect(ep *tunnelproto.DirectEndpoint) {
	var d *store.Direct
	if ep != nil && ep.Addr != "" && len(ep.CertSHA256) == 64 {
		d = &store.Direct{Addr: ep.Addr, CertSHA256: ep.CertSHA256}
	}
	a.mu.Lock()
	same := (a.direct == nil && d == nil) || (a.direct != nil && d != nil && *a.direct == *d)
	if !same {
		a.direct, a.directAfter = d, time.Time{}
	}
	a.mu.Unlock()
	if !same && a.store != nil {
		if err := a.store.SetDirect(d); err != nil {
			a.log.Warn("cannot remember the direct endpoint", "err", err)
		}
	}
}

func connectURL(server string) string {
	if rest, ok := strings.CutPrefix(server, "https://"); ok {
		return "wss://" + rest + tunnelproto.ConnectPath
	}
	return "ws://" + strings.TrimPrefix(server, "http://") + tunnelproto.ConnectPath
}

// session runs one connection until it fails. When firstConfig is set, the first snapshot is sent
// there and data streams are not served.
func (a *Agent) session(ctx context.Context, firstConfig chan<- *tunnelproto.Config) error {
	host, err := a.id.Host()
	if err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	ws, transport, err := a.connect(dctx)
	if err != nil {
		cancel()
		return err
	}
	features := []string{}
	if a.gui {
		features = append(features, "gui")
	}
	if firstConfig != nil {
		features = append(features, tunnelproto.FeatureProbe)
	}
	_, err = tunnelproto.ClientHandshake(dctx, ws, host, tunnelproto.Hello{DeviceID: a.id.DeviceID, ClientVersion: a.version, Features: features}, a.id.PrivateKey)
	cancel()
	if err != nil {
		ws.CloseNow()
		return err
	}
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	mux, err := tunnelproto.ClientSession(sctx, ws)
	if err != nil {
		ws.CloseNow()
		return err
	}
	defer mux.Close()
	stream, err := mux.Open()
	if err != nil {
		return err
	}
	ctl := tunnelproto.NewControl(stream)
	a.mu.Lock()
	a.ctl = ctl
	a.state.Connected, a.state.ConnectedAt, a.state.LastError, a.state.Transport = true, time.Now(), "", transport
	a.changed()
	a.mu.Unlock()
	a.log.Info("connected", "server", a.id.Server, "device", a.id.DeviceID, "transport", transport)
	if a.store != nil && a.legacy && firstConfig == nil {
		// This version works: an update will not be rolled back to one that needs the old files.
		a.store.DropLegacy()
		a.legacy = false
	}
	defer func() {
		a.mu.Lock()
		if a.ctl == ctl {
			a.ctl = nil
		}
		for id, ch := range a.pending {
			close(ch)
			delete(a.pending, id)
		}
		a.mu.Unlock()
	}()

	if firstConfig == nil {
		go a.acceptStreams(mux)
		go a.reportStats(sctx, ctl)
	}

	// The WebSocket close status (4401 etc.) is what callers need, not the multiplexer's EOF.
	readErr := make(chan error, 1)
	go func() { readErr <- a.readControl(ctl, firstConfig) }()
	select {
	case err := <-readErr:
		if ce := mux.CloseError(); websocket.CloseStatus(ce) != -1 {
			return ce
		}
		return err
	case <-ctx.Done():
		ws.Close(websocket.StatusNormalClosure, "")
		return ctx.Err()
	}
}

func (a *Agent) readControl(ctl *tunnelproto.Control, firstConfig chan<- *tunnelproto.Config) error {
	for {
		m, err := ctl.Recv()
		if err != nil {
			return err
		}
		switch m.Type {
		case tunnelproto.TypeConfig:
			var cfg tunnelproto.Config
			if err := m.Decode(&cfg); err != nil {
				return err
			}
			a.applyConfig(&cfg)
			if firstConfig != nil {
				firstConfig <- &cfg
				firstConfig = nil
				continue
			}
			a.reportStatus(ctl, &cfg)
		case tunnelproto.TypePing:
			_ = ctl.Send(tunnelproto.TypePong, m.ID, nil)
		case tunnelproto.TypeResult:
			var r tunnelproto.Result
			_ = m.Decode(&r)
			a.mu.Lock()
			ch := a.pending[m.ID]
			delete(a.pending, m.ID)
			a.mu.Unlock()
			if ch != nil {
				ch <- r
			}
		case tunnelproto.TypeNotice:
			var n tunnelproto.Notice
			if m.Decode(&n) == nil {
				a.log.Info("notice from server", "level", n.Level, "text", n.Text)
			}
		}
	}
}

func (a *Agent) applyConfig(cfg *tunnelproto.Config) {
	a.rememberDirect(cfg.Direct)
	var confirms map[string]store.Confirmation
	if a.store != nil {
		if err := a.store.ApplyConfig(cfg); err != nil {
			a.log.Warn("cannot store the configuration", "err", err)
		}
		if cs, err := a.store.Confirmations(); err == nil {
			confirms = cs
		} else {
			a.log.Warn("cannot read tunnel confirmations", "err", err)
		}
	}
	a.setState(func(s *State) {
		if confirms != nil {
			a.confirms = confirms
		}
		s.Config = cfg
		a.recomputeConfirmed()
		for id := range s.TunnelErrors {
			found := false
			for _, t := range cfg.Tunnels {
				found = found || t.ID == id
			}
			if !found {
				delete(s.TunnelErrors, id)
			}
		}
	})
	a.log.Info("configuration applied", "rev", cfg.Rev, "tunnels", len(cfg.Tunnels))
	a.logUnconfirmed()
}

// recomputeConfirmed refreshes State.Confirmed; a.mu must be held.
func (a *Agent) recomputeConfirmed() {
	a.state.Confirmed = map[string]bool{}
	if a.state.Config == nil {
		return
	}
	for i := range a.state.Config.Tunnels {
		t := &a.state.Config.Tunnels[i]
		c, ok := a.confirms[t.ID]
		a.state.Confirmed[t.ID] = ok && c.Covers(t)
	}
}

// logUnconfirmed tells whoever reads the log which tunnels wait for the device owner.
func (a *Agent) logUnconfirmed() {
	a.mu.Lock()
	var names []string
	if a.state.Config != nil {
		for _, t := range a.state.Config.Tunnels {
			if !a.state.Confirmed[t.ID] {
				names = append(names, fmt.Sprintf("%s（%s，本地 %s）", t.Name, t.ID, net.JoinHostPort(t.LocalIP, strconv.Itoa(t.LocalPort))))
			}
		}
	}
	a.mu.Unlock()
	if len(names) > 0 {
		a.log.Warn("有隧道等待本机确认，确认前不会接通", "tunnels", strings.Join(names, "、"))
	}
}

// confirmPoll is how often confirmations made by another process (the CLI next to a service) are
// picked up.
var confirmPoll = 2 * time.Second

// watchConfirmations reloads the confirmations until ctx ends.
func (a *Agent) watchConfirmations(ctx context.Context) {
	t := time.NewTicker(confirmPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		_ = a.reloadConfirmations()
	}
}

// setConfirmations installs cs and, when that changes what is served, updates the state and
// reports the tunnels to the server.
func (a *Agent) setConfirmations(cs map[string]store.Confirmation) {
	a.mu.Lock()
	before := a.state.Confirmed
	a.confirms = cs
	a.recomputeConfirmed()
	changed := len(before) != len(a.state.Confirmed)
	for id, v := range a.state.Confirmed {
		changed = changed || before[id] != v
	}
	ctl, cfg := a.ctl, a.state.Config
	if changed {
		a.changed()
	}
	a.mu.Unlock()
	if changed && ctl != nil && cfg != nil {
		a.reportStatus(ctl, cfg)
	}
}

// ErrUnknownTunnel means the tunnel is not in the current configuration.
var ErrUnknownTunnel = errors.New("no such tunnel on this device")

// Confirm records that the device owner agrees to serve tunnel id with its current type and local
// target; a later change of either needs a new confirmation.
func (a *Agent) Confirm(id string) error {
	t, ok := a.tunnel(id)
	if !ok {
		return ErrUnknownTunnel
	}
	return a.confirm(store.ConfirmationFor(&t))
}

func (a *Agent) confirm(c store.Confirmation) error {
	if a.store == nil {
		return errors.New("no local database")
	}
	if err := a.store.Confirm(c); err != nil {
		return err
	}
	return a.reloadConfirmations()
}

// Unconfirm withdraws the confirmation of tunnel id: the device stops serving it.
func (a *Agent) Unconfirm(id string) error {
	if a.store == nil {
		return errors.New("no local database")
	}
	if err := a.store.Unconfirm(id); err != nil {
		return err
	}
	return a.reloadConfirmations()
}

func (a *Agent) reloadConfirmations() error {
	cs, err := a.store.Confirmations()
	if err != nil {
		return err
	}
	a.setConfirmations(cs)
	return nil
}

func (a *Agent) reportStatus(ctl *tunnelproto.Control, cfg *tunnelproto.Config) {
	now := time.Now()
	a.mu.Lock()
	errs, confirmed := a.state.TunnelErrors, a.state.Confirmed
	statuses := make([]tunnelproto.Status, 0, len(cfg.Tunnels))
	for _, t := range cfg.Tunnels {
		st := tunnelproto.Status{TunnelID: t.ID, State: tunnelproto.StateRunning}
		switch {
		case !t.Active(now):
			st.State = tunnelproto.StatePaused
		case !confirmed[t.ID]:
			st.State = tunnelproto.StateUnconfirmed
		case errs[t.ID] != "":
			st.State, st.Error = tunnelproto.StateError, errs[t.ID]
		}
		statuses = append(statuses, st)
	}
	a.mu.Unlock()
	for _, st := range statuses {
		_ = ctl.Send(tunnelproto.TypeStatus, "", st)
	}
}

func (a *Agent) tunnel(id string) (tunnelproto.Tunnel, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.Config == nil {
		return tunnelproto.Tunnel{}, false
	}
	for _, t := range a.state.Config.Tunnels {
		if t.ID == id {
			return t, true
		}
	}
	return tunnelproto.Tunnel{}, false
}

// tunnelError records (or with "" clears) a local problem and tells the server when it changes.
func (a *Agent) tunnelError(id, msg string) {
	a.mu.Lock()
	changed := a.state.TunnelErrors[id] != msg
	confirmed := a.state.Confirmed[id]
	if msg == "" {
		delete(a.state.TunnelErrors, id)
	} else {
		a.state.TunnelErrors[id] = msg
	}
	ctl := a.ctl
	if changed {
		a.changed()
	}
	a.mu.Unlock()
	if changed && ctl != nil && confirmed {
		st := tunnelproto.Status{TunnelID: id, State: tunnelproto.StateRunning}
		if msg != "" {
			st.State, st.Error = tunnelproto.StateError, msg
		}
		_ = ctl.Send(tunnelproto.TypeStatus, "", st)
	}
}

func (a *Agent) acceptStreams(mux *tunnelproto.Session) {
	for {
		s, err := mux.AcceptStream()
		if err != nil {
			return
		}
		go a.serveStream(s)
	}
}

// localAllowed re-checks the target on the device: an IP address (or "localhost", never another
// host name that DNS could point anywhere), not unspecified or multicast, and loopback when the
// permissions say so. The server enforces the same, but the device does not rely on it.
func localAllowed(t *tunnelproto.Tunnel) bool {
	if t.LocalIP == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(t.LocalIP)
	if err != nil || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	return !t.Permissions.LoopbackOnly || ip.IsLoopback()
}

// confirmed reports whether the owner confirmed t as it is now.
func (a *Agent) confirmed(t *tunnelproto.Tunnel) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.confirms[t.ID]
	return ok && c.Covers(t)
}

func (a *Agent) serveStream(s *yamux.Stream) {
	defer s.Close()
	_ = s.SetReadDeadline(time.Now().Add(15 * time.Second))
	h, err := tunnelproto.ReadStreamHeader(s)
	if err != nil {
		return
	}
	_ = s.SetReadDeadline(time.Time{})
	t, ok := a.tunnel(h.TunnelID)
	switch {
	case !ok:
		tunnelproto.WriteStreamReply(s, tunnelproto.ReplyUnknownTunnel)
		return
	case !t.Active(time.Now()):
		tunnelproto.WriteStreamReply(s, tunnelproto.ReplyInactive)
		return
	case !a.confirmed(&t):
		tunnelproto.WriteStreamReply(s, tunnelproto.ReplyUnconfirmed)
		return
	case !localAllowed(&t):
		tunnelproto.WriteStreamReply(s, tunnelproto.ReplyForbidden)
		return
	}
	addr := net.JoinHostPort(t.LocalIP, strconv.Itoa(t.LocalPort))
	network := "tcp"
	if h.Proto == "udp" {
		network = "udp"
	}
	local, err := net.DialTimeout(network, addr, 5*time.Second)
	if err != nil {
		tunnelproto.WriteStreamReply(s, tunnelproto.ReplyDialFailed)
		a.tunnelError(t.ID, fmt.Sprintf("本地服务 %s 不可达", addr))
		return
	}
	defer local.Close()
	a.tunnelError(t.ID, "")
	if err := tunnelproto.WriteStreamReply(s, tunnelproto.ReplyOK); err != nil {
		return
	}
	c := a.counter(t.ID)
	c.conns.Add(1)
	if network == "udp" {
		relayUDP(s, local, c)
		return
	}
	pipe(s, local, c)
}

func (a *Agent) counter(id string) *counter {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.stats[id]
	if c == nil {
		c = &counter{}
		a.stats[id] = c
	}
	return c
}

func (a *Agent) reportStats(ctx context.Context, ctl *tunnelproto.Control) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.mu.Lock()
		var out []tunnelproto.Stats
		for id, c := range a.stats {
			st := tunnelproto.Stats{TunnelID: id, BytesIn: c.in.Swap(0), BytesOut: c.out.Swap(0), Conns: int(c.conns.Swap(0))}
			if st.BytesIn+st.BytesOut+int64(st.Conns) > 0 {
				out = append(out, st)
			}
		}
		a.mu.Unlock()
		for _, st := range out {
			_ = ctl.Send(tunnelproto.TypeStats, "", st)
		}
	}
}

type countingWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// pipe copies between the stream (visitor side) and the local connection.
func pipe(stream *yamux.Stream, local net.Conn, c *counter) {
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(countingWriter{local, &c.in}, stream)
		if tc, ok := local.(*net.TCPConn); ok {
			tc.CloseWrite()
		} else {
			local.Close()
		}
		done <- struct{}{}
	}()
	go func() {
		io.Copy(countingWriter{stream, &c.out}, local)
		stream.Close() // half-close: the visitor still gets what is in flight
		done <- struct{}{}
	}()
	<-done
	<-done
}

const udpIdle = 60 * time.Second

// relayUDP moves framed datagrams between the stream and a connected local UDP socket.
func relayUDP(stream *yamux.Stream, local net.Conn, c *counter) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, tunnelproto.MaxDatagram)
		for {
			_ = local.SetReadDeadline(time.Now().Add(udpIdle))
			n, err := local.Read(buf)
			if err != nil {
				stream.Close()
				return
			}
			c.out.Add(int64(n))
			if err := tunnelproto.WriteDatagram(stream, buf[:n]); err != nil {
				return
			}
		}
	}()
	buf := make([]byte, tunnelproto.MaxDatagram)
	for {
		d, err := tunnelproto.ReadDatagram(stream, buf)
		if err != nil {
			break
		}
		c.in.Add(int64(len(d)))
		if _, err := local.Write(d); err != nil {
			break
		}
	}
	local.Close()
	<-done
}

// ErrOffline means there is no session to send a request on.
var ErrOffline = errors.New("not connected to the server")

// Update asks the server to change a tunnel (local target or pause) and waits for its answer. A
// local target the device owner sets here counts as confirmed.
func (a *Agent) Update(ctx context.Context, u tunnelproto.TunnelUpdate) error {
	t, known := a.tunnel(u.TunnelID)
	if err := a.call(ctx, tunnelproto.TypeTunnelUpdate, u); err != nil {
		return err
	}
	if known && (u.LocalIP != nil || u.LocalPort != nil) && a.store != nil {
		c := store.ConfirmationFor(&t)
		if u.LocalIP != nil {
			c.LocalIP = *u.LocalIP
		}
		if u.LocalPort != nil {
			c.LocalPort = *u.LocalPort
		}
		if err := a.confirm(c); err != nil {
			a.log.Warn("cannot confirm the new local target", "tunnel", t.ID, "err", err)
		}
	}
	return nil
}

// Request files a tunnel request with the administrators.
func (a *Agent) Request(ctx context.Context, r tunnelproto.TunnelRequest) error {
	return a.call(ctx, tunnelproto.TypeRequestCreate, r)
}

// RefusedError is the server's refusal of a request, with its error code.
type RefusedError struct{ Code string }

func (e *RefusedError) Error() string { return "server refused: " + e.Code }

func (a *Agent) call(ctx context.Context, typ string, payload any) error {
	a.mu.Lock()
	ctl := a.ctl
	id := strconv.FormatInt(a.nextID.Add(1), 10)
	ch := make(chan tunnelproto.Result, 1)
	if ctl != nil {
		a.pending[id] = ch
	}
	a.mu.Unlock()
	if ctl == nil {
		return ErrOffline
	}
	if err := ctl.Send(typ, id, payload); err != nil {
		return err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return ErrOffline
		}
		if !r.OK {
			return &RefusedError{Code: r.Error}
		}
		return nil
	case <-ctx.Done():
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
		return ctx.Err()
	}
}
