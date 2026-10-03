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
	LastError   string
	Config      *tunnelproto.Config
	// TunnelErrors holds the last local error per tunnel id (e.g. local service unreachable).
	TunnelErrors map[string]string
}

// Agent runs a device.
type Agent struct {
	id      *identity.Identity
	version string
	gui     bool
	log     *slog.Logger
	dial    func(ctx context.Context, url string) (*websocket.Conn, error)

	mu       sync.Mutex
	state    State
	ctl      *tunnelproto.Control
	pending  map[string]chan tunnelproto.Result
	watchers []chan State
	nextID   atomic.Int64
	stats    map[string]*counter
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
}

// New returns an agent; call Run.
func New(o Options) *Agent {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Agent{
		id: o.Identity, version: o.Version, gui: o.GUI, log: o.Log, pending: map[string]chan tunnelproto.Result{},
		stats: map[string]*counter{}, state: State{TunnelErrors: map[string]string{}},
		dial: func(ctx context.Context, u string) (*websocket.Conn, error) {
			c, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: map[string][]string{"User-Agent": {"nyatunnel/" + o.Version}}})
			return c, err
		},
	}
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
	ws, err := a.dial(dctx, connectURL(a.id.Server))
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
	a.state.Connected, a.state.ConnectedAt, a.state.LastError = true, time.Now(), ""
	a.changed()
	a.mu.Unlock()
	a.log.Info("connected", "server", a.id.Server, "device", a.id.DeviceID)
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
	a.setState(func(s *State) {
		s.Config = cfg
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
}

func (a *Agent) reportStatus(ctl *tunnelproto.Control, cfg *tunnelproto.Config) {
	now := time.Now()
	a.mu.Lock()
	errs := a.state.TunnelErrors
	statuses := make([]tunnelproto.Status, 0, len(cfg.Tunnels))
	for _, t := range cfg.Tunnels {
		st := tunnelproto.Status{TunnelID: t.ID, State: tunnelproto.StateRunning}
		switch {
		case !t.Active(now):
			st.State = tunnelproto.StatePaused
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
	if changed && ctl != nil {
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

// localAllowed re-checks the loopback rule on the device: the server enforces it too, but the
// device never dials outside what its own copy of the permissions allows.
func localAllowed(t *tunnelproto.Tunnel) bool {
	if !t.Permissions.LoopbackOnly || t.LocalIP == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(t.LocalIP)
	return err == nil && ip.Unmap().IsLoopback()
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

// Update asks the server to change a tunnel (local target or pause) and waits for its answer.
func (a *Agent) Update(ctx context.Context, u tunnelproto.TunnelUpdate) error {
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
	if err := ctl.Send(tunnelproto.TypeTunnelUpdate, id, u); err != nil {
		return err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return ErrOffline
		}
		if !r.OK {
			return fmt.Errorf("server refused: %s", r.Error)
		}
		return nil
	case <-ctx.Done():
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
		return ctx.Err()
	}
}
