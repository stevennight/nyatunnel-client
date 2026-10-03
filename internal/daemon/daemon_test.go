package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nyatunnel-client/internal/logbuf"
)

func newTestDaemon(t *testing.T) (*Daemon, *httptest.Server) {
	logs := logbuf.New(10)
	d := &Daemon{Dir: t.TempDir(), Version: "test", Log: slog.New(logs.Handler(nil)), Logs: logs}
	if err := d.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(d.Handler("0123456789abcdef", func() {}))
	t.Cleanup(srv.Close)
	return d, srv
}

func call(t *testing.T, srv *httptest.Server, method, path, token, host, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	json.Unmarshal(b, &m)
	return resp.StatusCode, m
}

func TestAuthAndHostChecks(t *testing.T) {
	_, srv := newTestDaemon(t)
	if code, _ := call(t, srv, "GET", "/v1/state", "", "", ""); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := call(t, srv, "GET", "/v1/state", "wrong-token-xxxxx", "", ""); code != 401 {
		t.Fatalf("wrong token: %d", code)
	}
	if code, _ := call(t, srv, "GET", "/v1/state", "0123456789abcdef", "evil.example:1234", ""); code != 403 {
		t.Fatalf("rebinding host: %d", code)
	}
	code, st := call(t, srv, "GET", "/v1/state", "0123456789abcdef", "", "")
	if code != 200 || st["enrolled"] != false || st["version"] != "test" {
		t.Fatalf("state: %d %v", code, st)
	}
}

func TestLinksAndNotEnrolled(t *testing.T) {
	_, srv := newTestDaemon(t)
	code, l := call(t, srv, "POST", "/v1/link", "0123456789abcdef", "", `{"url":"nyatunnel://enroll?v=1&s=tunnel.example.com&c=k7qp3xmd"}`)
	if code != 200 || l["kind"] != "enroll" || l["server"] != "https://tunnel.example.com" || l["code"] != "K7QP-3XMD" {
		t.Fatalf("link: %d %v", code, l)
	}
	if code, _ := call(t, srv, "POST", "/v1/link", "0123456789abcdef", "", `{"url":"nyatunnel://config?x=1"}`); code != 400 {
		t.Fatalf("bad link: %d", code)
	}
	if code, e := call(t, srv, "POST", "/v1/tunnels/tun_x", "0123456789abcdef", "", `{"paused":true}`); code != 409 || e["error"] != "not_enrolled" {
		t.Fatalf("update while not enrolled: %d %v", code, e)
	}
	if code, e := call(t, srv, "POST", "/v1/enroll", "0123456789abcdef", "", `{"server":"tunnel.example.com","code":"bad"}`); code != 400 || !strings.Contains(e["message"].(string), "注册码") {
		t.Fatalf("bad code: %d %v", code, e)
	}
}
