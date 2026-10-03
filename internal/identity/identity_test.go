package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
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

func TestKeyringStorage(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	_, priv, _ := ed25519.GenerateKey(nil)
	if err := SaveUser(dir, &Identity{Server: "https://t.example.com", DeviceID: "dev_k", PrivateKey: priv}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "identity.json"))
	if strings.Contains(string(raw), base64.StdEncoding.EncodeToString(priv.Seed())) || !strings.Contains(string(raw), `"keyring"`) {
		t.Fatalf("key leaked into the file: %s", raw)
	}
	got, err := Load(dir)
	if err != nil || !got.PrivateKey.Equal(priv) {
		t.Fatalf("load from keyring: %v", err)
	}
	if err := Remove(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(keyringService, "dev_k"); err == nil {
		t.Fatal("keyring entry survived Remove")
	}
	keyring.MockInitWithError(errors.New("no keychain"))
	if err := SaveUser(dir, &Identity{Server: "https://t.example.com", DeviceID: "dev_f", PrivateKey: priv}); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(dir); err != nil || got.KeyStore != "" || !got.PrivateKey.Equal(priv) {
		t.Fatalf("file fallback: %+v %v", got, err)
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
