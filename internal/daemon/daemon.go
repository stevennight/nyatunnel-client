// Package daemon runs the client core for the GUI: it owns the identity and the agent, and serves
// a small authenticated HTTP API on loopback that the GUI (Tauri) calls.
package daemon

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/stevennight/nyatunnel-common/deeplink"
	"github.com/stevennight/nyatunnel-common/tunnelproto"

	"nyatunnel-client/internal/agent"
	"nyatunnel-client/internal/enroll"
	"nyatunnel-client/internal/identity"
	"nyatunnel-client/internal/logbuf"
	"nyatunnel-client/internal/service"
	"nyatunnel-client/internal/update"
)

// Daemon manages one device identity and its agent.
type Daemon struct {
	Dir     string
	Version string
	GUI     bool
	Log     *slog.Logger
	Logs    *logbuf.Buffer

	mu     sync.Mutex
	id     *identity.Identity
	agent  *agent.Agent
	stop   context.CancelFunc
	done   chan struct{}
	notice string // shown once the device was revoked
	ctx    context.Context

	update   *UpdateInfo
	updateAt time.Time
}

// Start loads the identity (if any) and starts the agent. ctx bounds the daemon's lifetime.
func (d *Daemon) Start(ctx context.Context) error {
	d.mu.Lock()
	d.ctx = ctx
	d.mu.Unlock()
	id, err := identity.Load(d.Dir)
	if errors.Is(err, identity.ErrNotEnrolled) {
		return nil
	}
	if err != nil {
		return err
	}
	d.run(id)
	return nil
}

// run starts the agent for id (replacing any running one).
func (d *Daemon) run(id *identity.Identity) {
	d.halt()
	ctx, cancel := context.WithCancel(d.ctx)
	a := agent.New(agent.Options{Identity: id, Version: d.Version, GUI: d.GUI, Log: d.Log, Dir: d.Dir})
	done := make(chan struct{})
	d.mu.Lock()
	d.id, d.agent, d.stop, d.done, d.notice = id, a, cancel, done, ""
	d.mu.Unlock()
	go func() {
		defer close(done)
		err := a.Run(ctx)
		switch {
		case errors.Is(err, agent.ErrRevoked):
			_ = identity.Remove(d.Dir)
			d.mu.Lock()
			if d.agent == a {
				d.id, d.agent = nil, nil
				d.notice = "服务器已吊销本设备（或其所属账号被禁用），本机密钥已删除。如需继续使用，请重新注册。"
			}
			d.mu.Unlock()
			d.Log.Warn("device revoked; identity removed")
		case errors.Is(err, agent.ErrUpgradeRequired):
			d.mu.Lock()
			d.notice = "服务器要求更新客户端版本，请升级 NyaTunnel。"
			d.mu.Unlock()
		}
	}()
}

// halt stops the running agent and waits for it.
func (d *Daemon) halt() {
	d.mu.Lock()
	stop, done := d.stop, d.done
	d.stop, d.done = nil, nil
	d.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

// State is everything the GUI shows.
type State struct {
	Version      string               `json:"version"`
	Enrolled     bool                 `json:"enrolled"`
	Server       string               `json:"server,omitempty"`
	DeviceID     string               `json:"deviceId,omitempty"`
	DeviceName   string               `json:"deviceName,omitempty"`
	Fingerprint  string               `json:"fingerprint,omitempty"`
	ConfigDir    string               `json:"configDir"`
	Connected    bool                 `json:"connected"`
	Transport    string               `json:"transport,omitempty"`
	ConnectedAt  *time.Time           `json:"connectedAt,omitempty"`
	LastError    string               `json:"lastError,omitempty"`
	Notice       string               `json:"notice,omitempty"`
	Tunnels      []tunnelproto.Tunnel `json:"tunnels"`
	TunnelErrors map[string]string    `json:"tunnelErrors"`
	CanRequest   bool                 `json:"canRequest"`
	ConfigRev    int64                `json:"configRev"`
	// Service is set when a system service runs a device on this machine.
	Service *ServiceState `json:"service,omitempty"`
}

// ServiceState describes the system service (see `nyatunnel service install`).
type ServiceState struct {
	Running    bool   `json:"running"`
	Server     string `json:"server"`
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName"`
	Detail     string `json:"detail,omitempty"`
}

// State returns a snapshot.
func (d *Daemon) State() State {
	d.mu.Lock()
	id, a, notice := d.id, d.agent, d.notice
	d.mu.Unlock()
	st := State{Version: d.Version, ConfigDir: d.Dir, Notice: notice, Tunnels: []tunnelproto.Tunnel{}, TunnelErrors: map[string]string{}}
	if info, err := service.ReadInfo(); err == nil {
		ss, _ := service.QueryStatus()
		st.Service = &ServiceState{Running: ss.Running, Server: info.Server, DeviceID: info.DeviceID, DeviceName: info.DeviceName, Detail: ss.Detail}
	}
	if id == nil {
		return st
	}
	st.Enrolled, st.Server, st.DeviceID, st.DeviceName, st.Fingerprint = true, id.Server, id.DeviceID, id.DeviceName, id.Fingerprint()
	if a == nil {
		return st
	}
	as := a.State()
	st.Connected, st.LastError, st.TunnelErrors, st.Transport = as.Connected, as.LastError, as.TunnelErrors, as.Transport
	if as.Connected {
		t := as.ConnectedAt
		st.ConnectedAt = &t
	}
	if as.Config != nil {
		st.Tunnels, st.CanRequest, st.ConfigRev = as.Config.Tunnels, as.Config.CanRequest, as.Config.Rev
		if st.Tunnels == nil {
			st.Tunnels = []tunnelproto.Tunnel{}
		}
	}
	return st
}

// Link is a parsed deep link.
type Link struct {
	Kind     string `json:"kind"` // enroll, open
	Server   string `json:"server"`
	Code     string `json:"code,omitempty"`
	TunnelID string `json:"tunnelId,omitempty"`
	// SwitchServer is set when the link points at a different server than the one enrolled.
	SwitchServer bool `json:"switchServer"`
}

// ParseLink validates a nyatunnel:// link (it never acts on it).
func (d *Daemon) ParseLink(raw string) (*Link, error) {
	v, err := deeplink.Parse(raw)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	id := d.id
	d.mu.Unlock()
	var l Link
	switch x := v.(type) {
	case *deeplink.Enroll:
		l = Link{Kind: "enroll", Server: "https://" + x.Server, Code: x.Code}
	case *deeplink.Open:
		l = Link{Kind: "open", Server: "https://" + x.Server, TunnelID: x.TunnelID}
	}
	if id != nil {
		host, _ := id.Host()
		linkHost, _ := deeplink.ServerHost(l.Server)
		l.SwitchServer = host != linkHost
	}
	return &l, nil
}

// Preview asks the server what a code would give.
func (d *Daemon) Preview(ctx context.Context, server, code string) (*enroll.Preview, string, error) {
	server, code, err := normalize(server, code)
	if err != nil {
		return nil, "", err
	}
	p, err := enroll.GetPreview(ctx, server, code)
	return p, server, err
}

func normalize(server, code string) (string, string, error) {
	s, err := identity.NormalizeServer(server)
	if err != nil {
		return "", "", err
	}
	c, err := deeplink.NormalizeCode(code)
	if err != nil {
		return "", "", errors.New("注册码格式不正确（应为 XXXX-XXXX）")
	}
	return s, c, nil
}

// Enroll registers this device and starts the agent.
func (d *Daemon) Enroll(ctx context.Context, server, code, name string) error {
	server, code, err := normalize(server, code)
	if err != nil {
		return err
	}
	if name == "" {
		name, _ = os.Hostname()
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	deviceID, err := enroll.Claim(ctx, server, code, name, d.Version, pub, d.GUI)
	if err != nil {
		return err
	}
	id := &identity.Identity{Server: server, DeviceID: deviceID, DeviceName: name, PrivateKey: priv}
	if err := identity.SaveUser(d.Dir, id); err != nil {
		return err
	}
	d.Log.Info("enrolled", "server", server, "device", deviceID)
	d.run(id)
	return nil
}

// Logout stops the agent and forgets the identity.
func (d *Daemon) Logout() error {
	d.halt()
	d.mu.Lock()
	d.id, d.agent, d.notice = nil, nil, ""
	d.mu.Unlock()
	return identity.Remove(d.Dir)
}

// ErrNotEnrolled means there is no agent to forward a request to.
var ErrNotEnrolled = errors.New("本机尚未注册")

func (d *Daemon) current() (*agent.Agent, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.agent == nil {
		return nil, ErrNotEnrolled
	}
	return d.agent, nil
}

// Update forwards a tunnel change to the server.
func (d *Daemon) Update(ctx context.Context, u tunnelproto.TunnelUpdate) error {
	a, err := d.current()
	if err != nil {
		return err
	}
	return a.Update(ctx, u)
}

// Request files a tunnel request.
func (d *Daemon) Request(ctx context.Context, r tunnelproto.TunnelRequest) error {
	a, err := d.current()
	if err != nil {
		return err
	}
	return a.Request(ctx, r)
}

// UpdateInfo tells the GUI whether a newer release exists (the GUI opens the release page; its
// installer updates the bundled core too).
type UpdateInfo struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
	Newer   bool   `json:"newer"`
	URL     string `json:"url"`
}

// CheckUpdate asks GitHub at most every six hours unless forced.
func (d *Daemon) CheckUpdate(ctx context.Context, force bool) (*UpdateInfo, error) {
	d.mu.Lock()
	cached, at := d.update, d.updateAt
	d.mu.Unlock()
	if cached != nil && !force && time.Since(at) < 6*time.Hour {
		return cached, nil
	}
	r, err := update.Latest(ctx)
	if err != nil {
		return nil, err
	}
	current := strings.TrimPrefix(d.Version, "v")
	info := &UpdateInfo{Current: current, Latest: r.Version, Newer: update.Newer(r.Version, current), URL: r.URL}
	d.mu.Lock()
	d.update, d.updateAt = info, time.Now()
	d.mu.Unlock()
	return info, nil
}

// DownloadedUpdate is a verified installer ready to run.
type DownloadedUpdate struct {
	Version string `json:"version"`
	Path    string `json:"path"`
	Kind    string `json:"kind"` // nsis, dmg, appimage, deb
}

// DownloadUpdate fetches the latest release's installer for this platform when it is newer.
func (d *Daemon) DownloadUpdate(ctx context.Context, appImage bool) (*DownloadedUpdate, error) {
	r, err := update.Latest(ctx)
	if err != nil {
		return nil, err
	}
	if !update.Newer(r.Version, strings.TrimPrefix(d.Version, "v")) {
		return nil, errors.New("已是最新版本")
	}
	path, kind, err := update.DownloadGUI(ctx, r, appImage)
	if err != nil {
		return nil, err
	}
	d.Log.Info("update downloaded", "version", r.Version, "path", path)
	return &DownloadedUpdate{Version: r.Version, Path: path, Kind: kind}, nil
}

// Close stops the agent.
func (d *Daemon) Close() { d.halt() }
