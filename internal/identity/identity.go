// Package identity is this device's enrollment: the server it belongs to, its device id and its
// Ed25519 private key. The key never leaves the device. It is kept in the local database (package
// store), with the private key in the OS keychain where there is one.
package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/stevennight/nyatunnel-common/deeplink"
	"github.com/zalando/go-keyring"

	"nyatunnel-client/internal/store"
)

// Identity is an enrolled device.
type Identity struct {
	// Server is the public URL, e.g. https://tunnel.example.com.
	Server     string
	DeviceID   string
	DeviceName string
	PrivateKey ed25519.PrivateKey
	// KeyStore is "keyring" when the seed lives in the OS keychain, "" when it is in the database.
	KeyStore string
}

const keyringService = "NyaTunnel"

// Host is the server host the device signs for.
func (id *Identity) Host() (string, error) { return deeplink.ServerHost(id.Server) }

// Fingerprint is a short, human-comparable digest of the public key.
func (id *Identity) Fingerprint() string {
	pub := id.PrivateKey.Public().(ed25519.PublicKey)
	return "ed25519:" + base64.RawStdEncoding.EncodeToString(pub)[:16]
}

// ErrNotEnrolled means no device is stored.
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

// Load reads the identity from the database in dir.
func Load(dir string) (*Identity, error) {
	st, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	return FromStore(st)
}

// FromStore reads the identity from an open database.
func FromStore(st *store.Store) (*Identity, error) {
	d, err := st.Device()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, ErrNotEnrolled
	}
	key := d.Key
	if d.KeyStore == "keyring" {
		v, err := keyring.Get(keyringService, d.DeviceID)
		if err != nil {
			return nil, fmt.Errorf("cannot read the device key from the system keychain: %w", err)
		}
		key = v
	}
	seed, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(seed) != ed25519.SeedSize || d.DeviceID == "" || d.Server == "" {
		return nil, errors.New("the stored device identity is damaged")
	}
	return &Identity{Server: d.Server, DeviceID: d.DeviceID, DeviceName: d.DeviceName, PrivateKey: ed25519.NewKeyFromSeed(seed), KeyStore: d.KeyStore}, nil
}

// Save stores the identity with the key in the database (system services, servers). It replaces
// any previous device in dir.
func Save(dir string, id *Identity) error { return save(dir, id, false) }

// SaveUser prefers the OS keychain (Windows Credential Manager, macOS Keychain, Secret Service)
// for the private key and falls back to the database where there is none (headless Linux, Docker).
func SaveUser(dir string, id *Identity) error { return save(dir, id, true) }

func save(dir string, id *Identity, useKeyring bool) error {
	st, err := store.Open(dir)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := forget(st); err != nil {
		return err
	}
	seed := base64.StdEncoding.EncodeToString(id.PrivateKey.Seed())
	d := store.Device{Server: id.Server, DeviceID: id.DeviceID, DeviceName: id.DeviceName, Key: seed}
	id.KeyStore = ""
	if useKeyring && keyring.Set(keyringService, id.DeviceID, seed) == nil {
		d.Key, d.KeyStore, id.KeyStore = "", "keyring", "keyring"
	}
	return st.SetDevice(d)
}

// Remove deletes the identity (logout or revoked) with everything stored for it, including a key in
// the keychain.
func Remove(dir string) error {
	st, err := store.Open(dir)
	if err != nil {
		return err
	}
	defer st.Close()
	return forget(st)
}

func forget(st *store.Store) error {
	old, err := st.ForgetDevice()
	if err == nil && old != nil && old.KeyStore == "keyring" {
		_ = keyring.Delete(keyringService, old.DeviceID)
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
