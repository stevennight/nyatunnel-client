// Package store is the client's local database: nyatunnel.db (SQLite) in the configuration
// directory. It holds the device identity, the remembered direct endpoint, the tunnels the device
// owner confirmed (协议.md §4.6) and the last configuration snapshot.
//
// Versions before the database kept identity.json and direct.json in the same directory. Open
// imports them; they are deleted only after the new version connected once (DropLegacy), so an
// automatic update that is rolled back still finds its identity.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/stevennight/nyatunnel-common/tunnelproto"
	_ "modernc.org/sqlite" // registers the "sqlite" driver (pure Go, no CGO)
)

// FileName is the database file in the configuration directory.
const FileName = "nyatunnel.db"

// Legacy files imported by Open.
const (
	legacyIdentity = "identity.json"
	legacyDirect   = "direct.json"
)

// Store is an open database.
type Store struct {
	db  *sql.DB
	dir string
}

// migrations[i] brings the schema from user_version i to i+1.
var migrations = []string{
	`CREATE TABLE device (
		id          INTEGER PRIMARY KEY CHECK (id = 1),
		server      TEXT NOT NULL,
		device_id   TEXT NOT NULL,
		device_name TEXT NOT NULL DEFAULT '',
		private_key TEXT NOT NULL DEFAULT '', -- base64 seed; '' when it lives in the OS keychain
		key_store   TEXT NOT NULL DEFAULT ''  -- 'keyring' or ''
	);
	CREATE TABLE direct_endpoint (
		id          INTEGER PRIMARY KEY CHECK (id = 1),
		addr        TEXT NOT NULL,
		cert_sha256 TEXT NOT NULL
	);
	CREATE TABLE tunnel_confirmations (
		tunnel_id    TEXT PRIMARY KEY,
		type         TEXT NOT NULL,
		local_ip     TEXT NOT NULL,
		local_port   INTEGER NOT NULL,
		confirmed_at INTEGER NOT NULL -- unix milliseconds
	);
	CREATE TABLE kv (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`,
}

// kv keys.
const (
	keyLastConfig   = "last_config"
	keyLastConfigAt = "last_config_at"
	// keyTrustUpgrade is set when an identity was imported from a version without confirmations:
	// the tunnels of the first snapshot afterwards were already running and count as confirmed.
	keyTrustUpgrade = "trust_first_config"
)

// Open opens (creating if needed) the database in dir, applies migrations and imports legacy files.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, FileName)
	// Create the file owner-only before SQLite does (it would use 0644); the WAL and shared-memory
	// files inherit its mode.
	if f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600); err == nil {
		f.Close()
	} else {
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, dir: dir}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("local database %s: %w", path, err)
	}
	if err := s.importLegacy(); err != nil {
		db.Close()
		return nil, fmt.Errorf("importing %s: %w", legacyIdentity, err)
	}
	return s, nil
}

// Dir is the configuration directory the database lives in.
func (s *Store) Dir() string { return s.dir }

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for ; v < len(migrations); v++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if _, err := tx.Exec(`PRAGMA user_version = ` + strconv.Itoa(v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if v > len(migrations) {
		return fmt.Errorf("written by a newer NyaTunnel (schema %d); update the client", v)
	}
	return nil
}

// legacyIdentityFile is the identity.json written by versions before the database.
type legacyIdentityFile struct {
	Server     string `json:"server"`
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName"`
	Key        string `json:"privateKey"`
	KeyStore   string `json:"keyStore"`
}

// importLegacy copies identity.json and direct.json into an empty database.
func (s *Store) importLegacy() error {
	if d, err := s.Device(); err != nil || d != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(s.dir, legacyIdentity))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var old legacyIdentityFile
	if err := json.Unmarshal(b, &old); err != nil || old.Server == "" || old.DeviceID == "" {
		return errors.New("the file is damaged")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO device (id, server, device_id, device_name, private_key, key_store) VALUES (1, ?, ?, ?, ?, ?)`,
		old.Server, old.DeviceID, old.DeviceName, old.Key, old.KeyStore); err != nil {
		return err
	}
	if b, err := os.ReadFile(filepath.Join(s.dir, legacyDirect)); err == nil {
		var d Direct
		if json.Unmarshal(b, &d) == nil && d.valid() {
			if _, err := tx.Exec(`INSERT INTO direct_endpoint (id, addr, cert_sha256) VALUES (1, ?, ?)`, d.Addr, d.CertSHA256); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO kv (key, value) VALUES (?, '1')`, keyTrustUpgrade); err != nil {
		return err
	}
	return tx.Commit()
}

// DropLegacy deletes the imported legacy files. Call it once the device connected: from then on
// an update is not rolled back to a version that would need them.
func (s *Store) DropLegacy() {
	for _, name := range []string{legacyIdentity, legacyDirect} {
		_ = os.Remove(filepath.Join(s.dir, name))
	}
}

// Device is the enrolled identity as stored.
type Device struct {
	Server     string
	DeviceID   string
	DeviceName string
	// Key is the base64 Ed25519 seed, "" when KeyStore is "keyring".
	Key      string
	KeyStore string
}

// Device returns the enrolled device, or nil when there is none.
func (s *Store) Device() (*Device, error) {
	var d Device
	err := s.db.QueryRow(`SELECT server, device_id, device_name, private_key, key_store FROM device WHERE id = 1`).
		Scan(&d.Server, &d.DeviceID, &d.DeviceName, &d.Key, &d.KeyStore)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// SetDevice stores a newly enrolled (or moved) device. Everything that belonged to a previous
// device is forgotten, legacy files included.
func (s *Store) SetDevice(d Device) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := clearDevice(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO device (id, server, device_id, device_name, private_key, key_store) VALUES (1, ?, ?, ?, ?, ?)`,
		d.Server, d.DeviceID, d.DeviceName, d.Key, d.KeyStore); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.DropLegacy()
	return nil
}

// ForgetDevice deletes the device and all its state (logout, revoked) and returns what was stored,
// so the caller can also remove a key from the OS keychain.
func (s *Store) ForgetDevice() (*Device, error) {
	d, err := s.Device()
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := clearDevice(tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.DropLegacy()
	return d, nil
}

func clearDevice(tx *sql.Tx) error {
	for _, q := range []string{`DELETE FROM device`, `DELETE FROM direct_endpoint`, `DELETE FROM tunnel_confirmations`, `DELETE FROM kv`} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// Direct is the remembered direct endpoint of the server (learned over an authenticated session).
type Direct struct {
	Addr       string `json:"addr"`
	CertSHA256 string `json:"certSha256"`
}

func (d *Direct) valid() bool { return d.Addr != "" && len(d.CertSHA256) == 64 }

// Direct returns the remembered direct endpoint, if any.
func (s *Store) Direct() *Direct {
	var d Direct
	if s.db.QueryRow(`SELECT addr, cert_sha256 FROM direct_endpoint WHERE id = 1`).Scan(&d.Addr, &d.CertSHA256) != nil || !d.valid() {
		return nil
	}
	return &d
}

// SetDirect remembers (or with nil forgets) the direct endpoint.
func (s *Store) SetDirect(d *Direct) error {
	if d == nil {
		_, err := s.db.Exec(`DELETE FROM direct_endpoint`)
		return err
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO direct_endpoint (id, addr, cert_sha256) VALUES (1, ?, ?)`, d.Addr, d.CertSHA256)
	return err
}

// Confirmation records that the device owner agreed to a tunnel's local target.
type Confirmation struct {
	TunnelID    string
	Type        string
	LocalIP     string
	LocalPort   int
	ConfirmedAt time.Time
}

// ConfirmationFor is the confirmation of t's current target.
func ConfirmationFor(t *tunnelproto.Tunnel) Confirmation {
	return Confirmation{TunnelID: t.ID, Type: t.Type, LocalIP: t.LocalIP, LocalPort: t.LocalPort}
}

// Covers reports whether c confirms t as it is now: a changed type or local target needs a new
// confirmation.
func (c Confirmation) Covers(t *tunnelproto.Tunnel) bool {
	return c.TunnelID == t.ID && c.Type == t.Type && c.LocalIP == t.LocalIP && c.LocalPort == t.LocalPort
}

// Confirmations returns all confirmations by tunnel id.
func (s *Store) Confirmations() (map[string]Confirmation, error) {
	rows, err := s.db.Query(`SELECT tunnel_id, type, local_ip, local_port, confirmed_at FROM tunnel_confirmations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Confirmation{}
	for rows.Next() {
		var c Confirmation
		var at int64
		if err := rows.Scan(&c.TunnelID, &c.Type, &c.LocalIP, &c.LocalPort, &at); err != nil {
			return nil, err
		}
		c.ConfirmedAt = time.UnixMilli(at)
		out[c.TunnelID] = c
	}
	return out, rows.Err()
}

// Confirm stores (replacing) the confirmation of a tunnel.
func (s *Store) Confirm(c Confirmation) error {
	if c.ConfirmedAt.IsZero() {
		c.ConfirmedAt = time.Now()
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO tunnel_confirmations (tunnel_id, type, local_ip, local_port, confirmed_at) VALUES (?, ?, ?, ?, ?)`,
		c.TunnelID, c.Type, c.LocalIP, c.LocalPort, c.ConfirmedAt.UnixMilli())
	return err
}

// Unconfirm withdraws the confirmation of a tunnel; the device stops serving it.
func (s *Store) Unconfirm(tunnelID string) error {
	_, err := s.db.Exec(`DELETE FROM tunnel_confirmations WHERE tunnel_id = ?`, tunnelID)
	return err
}

// ApplyConfig records a configuration snapshot from the server: it becomes the last known
// configuration, confirmations of tunnels no longer in it are dropped, and right after an upgrade
// from a version without confirmations its tunnels are confirmed (they were already running).
func (s *Store) ApplyConfig(cfg *tunnelproto.Config) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var trust string
	if err := tx.QueryRow(`SELECT value FROM kv WHERE key = ?`, keyTrustUpgrade).Scan(&trust); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := time.Now().UnixMilli()
	keep := map[string]bool{}
	for i := range cfg.Tunnels {
		t := &cfg.Tunnels[i]
		keep[t.ID] = true
		if trust != "" {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO tunnel_confirmations (tunnel_id, type, local_ip, local_port, confirmed_at) VALUES (?, ?, ?, ?, ?)`,
				t.ID, t.Type, t.LocalIP, t.LocalPort, now); err != nil {
				return err
			}
		}
	}
	rows, err := tx.Query(`SELECT tunnel_id FROM tunnel_confirmations`)
	if err != nil {
		return err
	}
	var gone []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !keep[id] {
			gone = append(gone, id)
		}
	}
	rows.Close()
	for _, id := range gone {
		if _, err := tx.Exec(`DELETE FROM tunnel_confirmations WHERE tunnel_id = ?`, id); err != nil {
			return err
		}
	}
	for k, v := range map[string]string{keyLastConfig: string(raw), keyLastConfigAt: strconv.FormatInt(now, 10)} {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO kv (key, value) VALUES (?, ?)`, k, v); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM kv WHERE key = ?`, keyTrustUpgrade); err != nil {
		return err
	}
	return tx.Commit()
}

// LastConfig returns the last snapshot received from the server and when it arrived, or nil.
func (s *Store) LastConfig() (*tunnelproto.Config, time.Time, error) {
	var raw, at string
	err := s.db.QueryRow(`SELECT a.value, b.value FROM kv a, kv b WHERE a.key = ? AND b.key = ?`, keyLastConfig, keyLastConfigAt).Scan(&raw, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	var cfg tunnelproto.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, time.Time{}, err
	}
	ms, _ := strconv.ParseInt(at, 10, 64)
	return &cfg, time.UnixMilli(ms), nil
}

// CopyConfirmations copies the last configuration and the confirmations into dst (when the device
// moves to the system service, its tunnels stay confirmed).
func (s *Store) CopyConfirmations(dst *Store) error {
	cs, err := s.Confirmations()
	if err != nil {
		return err
	}
	if cfg, _, err := s.LastConfig(); err != nil {
		return err
	} else if cfg != nil {
		if err := dst.ApplyConfig(cfg); err != nil {
			return err
		}
	}
	for _, c := range cs {
		if err := dst.Confirm(c); err != nil {
			return err
		}
	}
	return nil
}
