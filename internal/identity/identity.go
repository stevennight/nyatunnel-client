// Package identity stores this device's enrollment: the server it belongs to, its device id and its
// Ed25519 private key. The key never leaves the device.
package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/stevennight/nyatunnel-common/deeplink"
)

// Identity is an enrolled device.
type Identity struct {
	// Server is the public URL, e.g. https://tunnel.example.com.
	Server     string             `json:"server"`
	DeviceID   string             `json:"deviceId"`
	DeviceName string             `json:"deviceName"`
	PrivateKey ed25519.PrivateKey `json:"-"`
	Key        string             `json:"privateKey"` // base64 seed, only in the file
}

// Host is the server host the device signs for.
func (id *Identity) Host() (string, error) { return deeplink.ServerHost(id.Server) }

// Fingerprint is a short, human-comparable digest of the public key.
func (id *Identity) Fingerprint() string {
	pub := id.PrivateKey.Public().(ed25519.PublicKey)
	return "ed25519:" + base64.RawStdEncoding.EncodeToString(pub)[:16]
}

// ErrNotEnrolled means there is no identity file.
var ErrNotEnrolled = errors.New("this device is not enrolled")

// Dir returns the configuration directory: $NYATUNNEL_HOME, or <user config dir>/NyaTunnel.
func Dir() (string, error) {
	if d := os.Getenv("NYATUNNEL_HOME"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "NyaTunnel"), nil
}

func path(dir string) string { return filepath.Join(dir, "identity.json") }

// Load reads the identity from dir.
func Load(dir string) (*Identity, error) {
	b, err := os.ReadFile(path(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotEnrolled
	}
	if err != nil {
		return nil, err
	}
	var id Identity
	if err := json.Unmarshal(b, &id); err != nil {
		return nil, fmt.Errorf("identity file is damaged: %w", err)
	}
	seed, err := base64.StdEncoding.DecodeString(id.Key)
	if err != nil || len(seed) != ed25519.SeedSize || id.DeviceID == "" || id.Server == "" {
		return nil, errors.New("identity file is damaged")
	}
	id.PrivateKey = ed25519.NewKeyFromSeed(seed)
	return &id, nil
}

// Save writes the identity with owner-only permissions, replacing any previous one atomically.
func Save(dir string, id *Identity) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	id.Key = base64.StdEncoding.EncodeToString(id.PrivateKey.Seed())
	b, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "identity-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && !isWindows() {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path(dir))
}

// Remove deletes the identity (logout or revoked).
func Remove(dir string) error {
	_ = SaveDirect(dir, nil)
	err := os.Remove(path(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// NormalizeServer turns user input ("tunnel.example.com", "https://tunnel.example.com/") into a
// public URL. Plain http is only kept when written explicitly (local testing).
func NormalizeServer(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.User != nil || u.RawQuery != "" {
		return "", fmt.Errorf("invalid server address %q", s)
	}
	host, err := deeplink.ServerHost(u.Scheme + "://" + u.Host)
	if err != nil {
		return "", err
	}
	if u.Scheme == "http" {
		return "http://" + u.Host, nil
	}
	return "https://" + host, nil
}

func isWindows() bool { return runtime.GOOS == "windows" }

// Direct is the remembered direct endpoint of the server (learned over an authenticated session).
type Direct struct {
	Addr       string `json:"addr"`
	CertSHA256 string `json:"certSha256"`
}

func directPath(dir string) string { return filepath.Join(dir, "direct.json") }

// LoadDirect returns the remembered direct endpoint, if any.
func LoadDirect(dir string) *Direct {
	b, err := os.ReadFile(directPath(dir))
	if err != nil {
		return nil
	}
	var d Direct
	if json.Unmarshal(b, &d) != nil || d.Addr == "" || len(d.CertSHA256) != 64 {
		return nil
	}
	return &d
}

// SaveDirect remembers (or with nil forgets) the direct endpoint.
func SaveDirect(dir string, d *Direct) error {
	if d == nil {
		err := os.Remove(directPath(dir))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	b, _ := json.Marshal(d)
	return os.WriteFile(directPath(dir), b, 0o600)
}
