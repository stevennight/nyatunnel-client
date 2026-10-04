package agent

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stevennight/nyatunnel-common/tunnelproto"

	"nyatunnel-client/internal/identity"
	"nyatunnel-client/internal/store"
)

// fakeServer accepts one device and lets the test drive the session.
type fakeServer struct {
	srv      *httptest.Server
	pub      ed25519.PublicKey
	refuse   atomic.Bool
	sessions chan *serverSession
}

type serverSession struct {
	ws       *websocket.Conn
	mux      *tunnelproto.Session
	ctl      *tunnelproto.Control
	hello    *tunnelproto.Hello
	updates  chan tunnelproto.TunnelUpdate
	statuses chan tunnelproto.Status
	done     chan struct{}
}

func newFakeServer(t *testing.T, pub ed25519.PublicKey) *fakeServer {
	f := &fakeServer{pub: pub, sessions: make(chan *serverSession, 4)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		host := strings.TrimPrefix(f.srv.URL, "http://")
		hello, err := tunnelproto.ServerHandshake(r.Context(), ws, host, func(context.Context, *tunnelproto.Hello) (ed25519.PublicKey, error) {
			if f.refuse.Load() {
				return nil, tunnelproto.ErrUnauthorized
			}
			return f.pub, nil
		})
		if errors.Is(err, tunnelproto.ErrUnauthorized) {
			ws.Close(tunnelproto.CloseUnauthorized, "revoked")
			return
		}
		if err != nil {
			return
		}
		tunnelproto.SendWelcome(r.Context(), ws, tunnelproto.Welcome{SessionID: "s"})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		mux, _ := tunnelproto.ServerSession(ctx, ws)
		stream, err := mux.Accept()
		if err != nil {
			return
		}
		s := &serverSession{ws: ws, mux: mux, ctl: tunnelproto.NewControl(stream), hello: hello, updates: make(chan tunnelproto.TunnelUpdate, 4), statuses: make(chan tunnelproto.Status, 64), done: make(chan struct{})}
		f.sessions <- s
		defer close(s.done)
		for {
			m, err := s.ctl.Recv()
			if err != nil {
				return
			}
			if m.Type == tunnelproto.TypeStatus {
				var st tunnelproto.Status
				m.Decode(&st)
				select {
				case s.statuses <- st:
				default:
				}
			}
			if m.Type == tunnelproto.TypeTunnelUpdate {
				var u tunnelproto.TunnelUpdate
				m.Decode(&u)
				s.updates <- u
				s.ctl.Send(tunnelproto.TypeResult, m.ID, tunnelproto.Result{OK: u.LocalPort == nil, Error: map[bool]string{true: "", false: "field_not_allowed"}[u.LocalPort == nil]})
			}
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) next(t *testing.T) *serverSession {
	t.Helper()
	select {
	case s := <-f.sessions:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("device did not connect")
		return nil
	}
}

func setup(t *testing.T) (*fakeServer, *Agent) {
	f, a, _ := setupStore(t)
	return f, a
}

// setupStore also returns the agent's local database (in a temporary directory).
func setupStore(t *testing.T) (*fakeServer, *Agent, *store.Store) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	f := newFakeServer(t, pub)
	id := &identity.Identity{Server: f.srv.URL, DeviceID: "dev_1", PrivateKey: priv}
	st := openStore(t, t.TempDir())
	return f, New(Options{Identity: id, Version: "test", Store: st}), st
}

func openStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// waitStatus returns the next status the device reports for tunnel id.
func (s *serverSession) waitStatus(t *testing.T, id string) tunnelproto.Status {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case st := <-s.statuses:
			if st.TunnelID == id {
				return st
			}
		case <-timeout:
			t.Fatalf("no status for %s", id)
			return tunnelproto.Status{}
		}
	}
}

func openStream(t *testing.T, s *serverSession, id string) (net.Conn, error) {
	t.Helper()
	st, err := s.mux.Open()
	if err != nil {
		t.Fatal(err)
	}
	tunnelproto.WriteStreamHeader(st, tunnelproto.StreamHeader{TunnelID: id, Proto: "tcp"})
	return st, tunnelproto.ReadStreamReply(st)
}

func waitState(t *testing.T, a *Agent, pred func(State) bool) State {
	t.Helper()
	ch := a.Watch()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case s := <-ch:
			if pred(s) {
				return s
			}
		case <-timeout:
			t.Fatalf("state never matched; last %+v", a.State())
		}
	}
}

func TestForwardsStreamsAndReportsLocalErrors(t *testing.T) {
	f, a, st := setupStore(t)
	echo, _ := net.Listen("tcp", "127.0.0.1:0")
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	s := f.next(t)
	port := echo.Addr().(*net.TCPAddr).Port
	tunnels := []tunnelproto.Tunnel{
		{ID: "tun_ok", Type: "tcp", LocalIP: "127.0.0.1", LocalPort: port, Enabled: true},
		{ID: "tun_dead", Type: "tcp", LocalIP: "127.0.0.1", LocalPort: 1, Enabled: true},
		{ID: "tun_paused", Type: "tcp", LocalIP: "127.0.0.1", LocalPort: port, Enabled: true, PausedByClient: true},
		{ID: "tun_lan", Type: "tcp", LocalIP: "192.168.1.1", LocalPort: port, Enabled: true, Permissions: tunnelproto.Permissions{LoopbackOnly: true}},
		{ID: "tun_name", Type: "tcp", LocalIP: "example.com", LocalPort: port, Enabled: true},
	}
	for i := range tunnels {
		st.Confirm(store.ConfirmationFor(&tunnels[i]))
	}
	s.ctl.Send(tunnelproto.TypeConfig, "", tunnelproto.Config{Rev: 1, Tunnels: tunnels})
	waitState(t, a, func(s State) bool { return s.Config != nil && s.Config.Rev == 1 })

	c, err := openStream(t, s, "tun_ok")
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("hi"))
	buf := make([]byte, 2)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hi" {
		t.Fatalf("echo %q %v", buf, err)
	}
	for id, want := range map[string]tunnelproto.StreamReply{
		"tun_dead": tunnelproto.ReplyDialFailed, "tun_paused": tunnelproto.ReplyInactive,
		"tun_lan": tunnelproto.ReplyForbidden, "tun_unknown": tunnelproto.ReplyUnknownTunnel,
		"tun_name": tunnelproto.ReplyForbidden, // host names could resolve anywhere
	} {
		if _, err := openStream(t, s, id); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", id, err, want)
		}
	}
	got := waitState(t, a, func(s State) bool { return s.TunnelErrors["tun_dead"] != "" })
	if got.TunnelErrors["tun_ok"] != "" {
		t.Fatalf("errors %v", got.TunnelErrors)
	}

	// Requests travel over the control stream and return the server's verdict.
	if err := a.Update(ctx, tunnelproto.TunnelUpdate{TunnelID: "tun_ok", PausedByClient: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if err := a.Update(ctx, tunnelproto.TunnelUpdate{TunnelID: "tun_ok", LocalPort: ptr(9)}); err == nil || !strings.Contains(err.Error(), "field_not_allowed") {
		t.Fatalf("refused update: %v", err)
	}
}

func TestTunnelsNeedLocalConfirmation(t *testing.T) {
	confirmPoll = 50 * time.Millisecond
	t.Cleanup(func() { confirmPoll = 2 * time.Second })
	f, a, st := setupStore(t)
	echo, _ := net.Listen("tcp", "127.0.0.1:0")
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	port := echo.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	s := f.next(t)
	web := tunnelproto.Tunnel{ID: "tun_web", Name: "web", Type: "tcp", LocalIP: "127.0.0.1", LocalPort: port, Enabled: true,
		Permissions: tunnelproto.Permissions{EditLocal: true}}
	s.ctl.Send(tunnelproto.TypeConfig, "", tunnelproto.Config{Rev: 1, Tunnels: []tunnelproto.Tunnel{web}})

	// A tunnel nobody confirmed is refused, and the server learns why.
	if got := s.waitStatus(t, "tun_web"); got.State != tunnelproto.StateUnconfirmed {
		t.Fatalf("status %+v", got)
	}
	if _, err := openStream(t, s, "tun_web"); !errors.Is(err, tunnelproto.ReplyUnconfirmed) {
		t.Fatalf("unconfirmed stream: %v", err)
	}

	// Confirming on the device turns it on and is reported.
	if err := a.Confirm("tun_web"); err != nil {
		t.Fatal(err)
	}
	if got := s.waitStatus(t, "tun_web"); got.State != tunnelproto.StateRunning {
		t.Fatalf("status after confirm %+v", got)
	}
	if !a.State().Confirmed["tun_web"] {
		t.Fatal("state does not show the confirmation")
	}
	if c, err := openStream(t, s, "tun_web"); err != nil {
		t.Fatalf("confirmed stream: %v", err)
	} else {
		c.Close()
	}

	// The administrator moves the tunnel to another local port: the confirmation no longer covers it.
	moved := web
	moved.LocalPort = 22
	s.ctl.Send(tunnelproto.TypeConfig, "", tunnelproto.Config{Rev: 2, Tunnels: []tunnelproto.Tunnel{moved}})
	if got := s.waitStatus(t, "tun_web"); got.State != tunnelproto.StateUnconfirmed {
		t.Fatalf("status after admin change %+v", got)
	}
	if _, err := openStream(t, s, "tun_web"); !errors.Is(err, tunnelproto.ReplyUnconfirmed) {
		t.Fatalf("moved tunnel: %v", err)
	}

	// Another process (the CLI next to a service) confirms through the database.
	other := openStore(t, st.Dir())
	if err := other.Confirm(store.ConfirmationFor(&moved)); err != nil {
		t.Fatal(err)
	}
	if got := s.waitStatus(t, "tun_web"); got.State != tunnelproto.StateRunning {
		t.Fatalf("status after confirmation by the CLI %+v", got)
	}
	if err := other.Unconfirm("tun_web"); err != nil {
		t.Fatal(err)
	}
	if got := s.waitStatus(t, "tun_web"); got.State != tunnelproto.StateUnconfirmed {
		t.Fatalf("status after revoking %+v", got)
	}

	// A local target the device owner sets on the device counts as confirmed.
	if err := a.Update(ctx, tunnelproto.TunnelUpdate{TunnelID: "tun_web", LocalIP: ptr("127.0.0.2")}); err != nil {
		t.Fatal(err)
	}
	cs, _ := st.Confirmations()
	if c := cs["tun_web"]; c.LocalIP != "127.0.0.2" || c.LocalPort != 22 {
		t.Fatalf("confirmation after a local edit: %+v", c)
	}

	// Tunnels that disappear from the configuration lose their confirmation.
	s.ctl.Send(tunnelproto.TypeConfig, "", tunnelproto.Config{Rev: 3})
	waitState(t, a, func(s State) bool { return s.Config != nil && s.Config.Rev == 3 })
	if cs, _ := st.Confirmations(); len(cs) != 0 {
		t.Fatalf("stale confirmations %+v", cs)
	}
}

func TestRevokedDeviceStops(t *testing.T) {
	f, a := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- a.Run(ctx) }()
	s := f.next(t)
	f.refuse.Store(true)
	s.ws.Close(tunnelproto.CloseUnauthorized, "revoked") // the server kicks the live session
	select {
	case err := <-errc:
		if !errors.Is(err, ErrRevoked) {
			t.Fatalf("Run returned %v", err)
		}
	case <-ctx.Done():
		t.Fatal("agent kept running after being revoked")
	}
}

func TestReconnectsAfterDrop(t *testing.T) {
	f, a := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	first := f.next(t)
	first.ws.CloseNow() // network failure
	second := f.next(t)
	if second.hello.DeviceID != "dev_1" {
		t.Fatalf("hello %+v", second.hello)
	}
}

func TestOnceUsesProbe(t *testing.T) {
	f, a := setup(t)
	go func() {
		s := f.next(t)
		if !tunnelproto.HasFeature(s.hello.Features, tunnelproto.FeatureProbe) {
			t.Error("status session without probe feature")
		}
		s.ctl.Send(tunnelproto.TypeConfig, "", tunnelproto.Config{Rev: 3})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg, err := a.Once(ctx)
	if err != nil || cfg.Rev != 3 {
		t.Fatalf("Once: %+v %v", cfg, err)
	}
}

func ptr[T any](v T) *T { return &v }
