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
)

// fakeServer accepts one device and lets the test drive the session.
type fakeServer struct {
	srv      *httptest.Server
	pub      ed25519.PublicKey
	refuse   atomic.Bool
	sessions chan *serverSession
}

type serverSession struct {
	ws      *websocket.Conn
	mux     *tunnelproto.Session
	ctl     *tunnelproto.Control
	hello   *tunnelproto.Hello
	updates chan tunnelproto.TunnelUpdate
	done    chan struct{}
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
		s := &serverSession{ws: ws, mux: mux, ctl: tunnelproto.NewControl(stream), hello: hello, updates: make(chan tunnelproto.TunnelUpdate, 4), done: make(chan struct{})}
		f.sessions <- s
		defer close(s.done)
		for {
			m, err := s.ctl.Recv()
			if err != nil {
				return
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
	pub, priv, _ := ed25519.GenerateKey(nil)
	f := newFakeServer(t, pub)
	id := &identity.Identity{Server: f.srv.URL, DeviceID: "dev_1", PrivateKey: priv}
	return f, New(Options{Identity: id, Version: "test"})
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
	f, a := setup(t)
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
	s.ctl.Send(tunnelproto.TypeConfig, "", tunnelproto.Config{Rev: 1, Tunnels: []tunnelproto.Tunnel{
		{ID: "tun_ok", Type: "tcp", LocalIP: "127.0.0.1", LocalPort: port, Enabled: true},
		{ID: "tun_dead", Type: "tcp", LocalIP: "127.0.0.1", LocalPort: 1, Enabled: true},
		{ID: "tun_paused", Type: "tcp", LocalIP: "127.0.0.1", LocalPort: port, Enabled: true, PausedByClient: true},
		{ID: "tun_lan", Type: "tcp", LocalIP: "192.168.1.1", LocalPort: port, Enabled: true, Permissions: tunnelproto.Permissions{LoopbackOnly: true}},
	}})
	waitState(t, a, func(s State) bool { return s.Config != nil && s.Config.Rev == 1 })

	open := func(id string) (net.Conn, error) {
		st, err := s.mux.Open()
		if err != nil {
			t.Fatal(err)
		}
		tunnelproto.WriteStreamHeader(st, tunnelproto.StreamHeader{TunnelID: id, Proto: "tcp"})
		return st, tunnelproto.ReadStreamReply(st)
	}
	st, err := open("tun_ok")
	if err != nil {
		t.Fatal(err)
	}
	st.Write([]byte("hi"))
	buf := make([]byte, 2)
	if _, err := io.ReadFull(st, buf); err != nil || string(buf) != "hi" {
		t.Fatalf("echo %q %v", buf, err)
	}
	for id, want := range map[string]tunnelproto.StreamReply{
		"tun_dead": tunnelproto.ReplyDialFailed, "tun_paused": tunnelproto.ReplyInactive,
		"tun_lan": tunnelproto.ReplyForbidden, "tun_unknown": tunnelproto.ReplyUnknownTunnel,
	} {
		if _, err := open(id); !errors.Is(err, want) {
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
