package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stevennight/nyatunnel-common/tunnelproto"
)

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestImportsLegacyFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(legacyIdentity, `{"server":"https://tunnel.example.com","deviceId":"dev_1","deviceName":"pc","privateKey":"c2VlZA==","keyStore":""}`)
	write(legacyDirect, `{"addr":"tunnel.example.com:7443","certSha256":"`+string(make64('a'))+`"}`)

	s := open(t, dir)
	d, err := s.Device()
	if err != nil || d == nil || d.DeviceID != "dev_1" || d.Key != "c2VlZA==" || d.Server != "https://tunnel.example.com" {
		t.Fatalf("device %+v %v", d, err)
	}
	if dr := s.Direct(); dr == nil || dr.Addr != "tunnel.example.com:7443" {
		t.Fatalf("direct %+v", dr)
	}
	// The old files stay until the new version connected (an update may still be rolled back).
	if _, err := os.Stat(filepath.Join(dir, legacyIdentity)); err != nil {
		t.Fatal("legacy identity removed too early")
	}

	// The first snapshot after the upgrade is trusted: those tunnels were running before.
	running := tunnelproto.Tunnel{ID: "tun_a", Type: "tcp", LocalIP: "127.0.0.1", LocalPort: 22}
	if err := s.ApplyConfig(&tunnelproto.Config{Rev: 1, Tunnels: []tunnelproto.Tunnel{running}}); err != nil {
		t.Fatal(err)
	}
	cs, _ := s.Confirmations()
	if c, ok := cs["tun_a"]; !ok || !c.Covers(&running) {
		t.Fatalf("upgrade did not keep the running tunnel: %+v", cs)
	}
	// Only the first one: later tunnels need the owner.
	added := tunnelproto.Tunnel{ID: "tun_b", Type: "tcp", LocalIP: "192.168.1.10", LocalPort: 3389}
	if err := s.ApplyConfig(&tunnelproto.Config{Rev: 2, Tunnels: []tunnelproto.Tunnel{running, added}}); err != nil {
		t.Fatal(err)
	}
	if cs, _ := s.Confirmations(); len(cs) != 1 {
		t.Fatalf("later tunnel confirmed by itself: %+v", cs)
	}
	if cfg, _, err := s.LastConfig(); err != nil || cfg == nil || cfg.Rev != 2 || len(cfg.Tunnels) != 2 {
		t.Fatalf("last config %+v %v", cfg, err)
	}

	s.DropLegacy()
	for _, name := range []string{legacyIdentity, legacyDirect} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still there", name)
		}
	}
	// Reopening does not import again or change anything.
	s.Close()
	s = open(t, dir)
	if d, _ := s.Device(); d == nil || d.DeviceID != "dev_1" {
		t.Fatalf("after reopen %+v", d)
	}
}

func TestNewDeviceStartsUntrusted(t *testing.T) {
	s := open(t, t.TempDir())
	if d, err := s.Device(); d != nil || err != nil {
		t.Fatalf("empty store has %+v %v", d, err)
	}
	if err := s.SetDevice(Device{Server: "https://tunnel.example.com", DeviceID: "dev_2", Key: "x"}); err != nil {
		t.Fatal(err)
	}
	tun := tunnelproto.Tunnel{ID: "tun_a", Type: "https", LocalIP: "127.0.0.1", LocalPort: 3000}
	if err := s.ApplyConfig(&tunnelproto.Config{Rev: 1, Tunnels: []tunnelproto.Tunnel{tun}}); err != nil {
		t.Fatal(err)
	}
	if cs, _ := s.Confirmations(); len(cs) != 0 {
		t.Fatalf("a fresh enrollment confirmed %+v", cs)
	}
	if err := s.Confirm(ConfirmationFor(&tun)); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(t *tunnelproto.Tunnel){
		func(t *tunnelproto.Tunnel) { t.LocalPort = 22 },
		func(t *tunnelproto.Tunnel) { t.LocalIP = "10.0.0.5" },
		func(t *tunnelproto.Tunnel) { t.Type = "tcp" },
	} {
		changed := tun
		change(&changed)
		if ConfirmationFor(&tun).Covers(&changed) {
			t.Errorf("confirmation covers %+v", changed)
		}
	}
	renamed := tun
	renamed.Name, renamed.PublicURL = "new name", "https://other.example.com"
	if !ConfirmationFor(&tun).Covers(&renamed) {
		t.Error("renaming should not need a new confirmation")
	}

	// Moving the device to the service keeps its confirmations.
	dst := open(t, t.TempDir())
	if err := dst.SetDevice(Device{Server: "https://tunnel.example.com", DeviceID: "dev_2", Key: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CopyConfirmations(dst); err != nil {
		t.Fatal(err)
	}
	if cs, _ := dst.Confirmations(); !cs["tun_a"].Covers(&tun) {
		t.Fatalf("copied %+v", cs)
	}

	old, err := s.ForgetDevice()
	if err != nil || old == nil || old.DeviceID != "dev_2" {
		t.Fatalf("forget %+v %v", old, err)
	}
	if cs, _ := s.Confirmations(); len(cs) != 0 {
		t.Fatalf("confirmations survived logout %+v", cs)
	}
	if cfg, _, _ := s.LastConfig(); cfg != nil {
		t.Fatal("configuration survived logout")
	}
}

func TestDatabaseFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	open(t, dir)
	fi, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("database mode %o", mode)
	}
}

func make64(c byte) []byte {
	b := make([]byte, 64)
	for i := range b {
		b[i] = c
	}
	return b
}
