package identity

import (
	"crypto/ed25519"
	"errors"
	"testing"
)

func TestSaveLoadRemove(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("empty dir: %v", err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	id := &Identity{Server: "https://tunnel.example.com", DeviceID: "dev_1", DeviceName: "pc", PrivateKey: priv}
	if err := Save(dir, id); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !got.PrivateKey.Equal(priv) || got.DeviceID != "dev_1" || got.Fingerprint() != id.Fingerprint() {
		t.Fatalf("round trip: %+v", got)
	}
	if h, _ := got.Host(); h != "tunnel.example.com" {
		t.Fatalf("host %q", h)
	}
	if err := Remove(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("after remove: %v", err)
	}
}

func TestNormalizeServer(t *testing.T) {
	ok := map[string]string{
		"tunnel.example.com":             "https://tunnel.example.com",
		"https://Tunnel.Example.com/":    "https://tunnel.example.com",
		"https://tunnel.example.com:443": "https://tunnel.example.com",
		"tunnel.example.com:8443":        "https://tunnel.example.com:8443",
		"http://127.0.0.1:8080":          "http://127.0.0.1:8080",
	}
	for in, want := range ok {
		if got, err := NormalizeServer(in); err != nil || got != want {
			t.Errorf("NormalizeServer(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"ftp://x.com", "https://x.com/path", "https://user@x.com", "https://x.com/?a=1", ""} {
		if _, err := NormalizeServer(in); err == nil {
			t.Errorf("NormalizeServer(%q) accepted", in)
		}
	}
}
