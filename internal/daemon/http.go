package daemon

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stevennight/nyatunnel-common/tunnelproto"

	"nyatunnel-client/internal/agent"
	"nyatunnel-client/internal/enroll"
)

// Handler serves the GUI API. Every request needs "Authorization: Bearer <token>"; browsers cannot
// add that header cross-origin without a preflight this API never answers, and the Host check
// stops DNS rebinding.
func (d *Daemon) Handler(token string, shutdown func()) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/state", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, d.State()) })
	mux.HandleFunc("GET /v1/logs", func(w http.ResponseWriter, r *http.Request) {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		writeJSON(w, 200, map[string]any{"lines": d.Logs.After(after)})
	})
	mux.HandleFunc("POST /v1/link", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL string `json:"url"`
		}
		if !decode(w, r, &body) {
			return
		}
		l, err := d.ParseLink(body.URL)
		if err != nil {
			fail(w, 400, "invalid_link", "不是有效的 NyaTunnel 链接："+err.Error())
			return
		}
		writeJSON(w, 200, l)
	})
	mux.HandleFunc("POST /v1/enroll/preview", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Server, Code string }
		if !decode(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		p, server, err := d.Preview(ctx, body.Server, body.Code)
		if err != nil {
			failErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"preview": p, "server": server})
	})
	mux.HandleFunc("POST /v1/enroll", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Server, Code, Name string }
		if !decode(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := d.Enroll(ctx, body.Server, body.Code, strings.TrimSpace(body.Name)); err != nil {
			failErr(w, err)
			return
		}
		writeJSON(w, 200, d.State())
	})
	mux.HandleFunc("POST /v1/logout", func(w http.ResponseWriter, r *http.Request) {
		if err := d.Logout(); err != nil {
			failErr(w, err)
			return
		}
		writeJSON(w, 200, d.State())
	})
	mux.HandleFunc("POST /v1/tunnels/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			LocalIP   *string `json:"localIp"`
			LocalPort *int    `json:"localPort"`
			Paused    *bool   `json:"paused"`
		}
		if !decode(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		err := d.Update(ctx, tunnelproto.TunnelUpdate{TunnelID: r.PathValue("id"), LocalIP: body.LocalIP, LocalPort: body.LocalPort, PausedByClient: body.Paused})
		if err != nil {
			failErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /v1/requests", func(w http.ResponseWriter, r *http.Request) {
		var body tunnelproto.TunnelRequest
		if !decode(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := d.Request(ctx, body); err != nil {
			failErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /v1/update", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		info, err := d.CheckUpdate(ctx, r.URL.Query().Get("force") == "1")
		if err != nil {
			fail(w, 502, "update_check_failed", "检查更新失败："+err.Error())
			return
		}
		writeJSON(w, 200, info)
	})
	mux.HandleFunc("POST /v1/update/download", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AppImage bool `json:"appImage"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
		defer cancel()
		info, err := d.DownloadUpdate(ctx, body.AppImage)
		if err != nil {
			fail(w, 502, "update_download_failed", "下载更新失败："+err.Error())
			return
		}
		writeJSON(w, 200, info)
	})
	mux.HandleFunc("POST /v1/shutdown", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"ok": true})
		go shutdown()
	})

	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			fail(w, 403, "forbidden_host", "")
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			fail(w, 401, "unauthorized", "")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		fail(w, 400, "bad_request", "请求格式不正确")
		return false
	}
	return true
}

// failErr maps errors to user-facing messages (Chinese) and codes.
func failErr(w http.ResponseWriter, err error) {
	var se *enroll.Error
	var re *agent.RefusedError
	switch {
	case errors.As(err, &se):
		msg := se.Message
		if se.Code == "enrollment_invalid" {
			msg = "注册码无效或已过期"
		}
		fail(w, 400, se.Code, msg)
	case errors.As(err, &re):
		msgs := map[string]string{
			"field_not_allowed":  "管理员没有允许修改此项",
			"local_not_loopback": "该隧道只允许本机回环地址",
			"invalid_local_ip":   "本地地址不正确",
			"invalid_port":       "端口不正确",
			"unsupported":        "服务器不支持此操作",
		}
		msg := msgs[re.Code]
		if msg == "" {
			msg = "服务器拒绝了请求（" + re.Code + "）"
		}
		fail(w, 400, re.Code, msg)
	case errors.Is(err, agent.ErrOffline):
		fail(w, 503, "offline", "尚未连接到服务器")
	case errors.Is(err, ErrNotEnrolled):
		fail(w, 409, "not_enrolled", err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		fail(w, 504, "timeout", "服务器无响应")
	default:
		fail(w, 400, "error", err.Error())
	}
}
