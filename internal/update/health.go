package update

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Marker is what a running core reports about itself in its state directory (running.json). The
// updater reads it to confirm that a freshly installed version really started and, when the old
// one was online, reconnected to the server.
type Marker struct {
	Version   string    `json:"version"`
	PID       int       `json:"pid"`
	Started   time.Time `json:"started"`
	Connected bool      `json:"connected"`
}

func markerPath(dir string) string { return filepath.Join(dir, "running.json") }

// WriteMarker records m in dir.
func WriteMarker(dir string, m Marker) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(m)
	tmp := markerPath(dir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, markerPath(dir))
}

// ReadMarker returns the last marker written in dir.
func ReadMarker(dir string) (*Marker, error) {
	b, err := os.ReadFile(markerPath(dir))
	if err != nil {
		return nil, err
	}
	var m Marker
	return &m, json.Unmarshal(b, &m)
}

// TrackHealth keeps the marker in dir current: written once now, then whenever the connection
// state reported on states changes, until ctx ends. A nil states channel only writes the marker.
func TrackHealth[S any](ctx context.Context, dir, version string, states <-chan S, connected func(S) bool) {
	m := Marker{Version: version, PID: os.Getpid(), Started: time.Now()}
	_ = WriteMarker(dir, m)
	if states == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case s, ok := <-states:
			if !ok {
				return
			}
			if c := connected(s); c != m.Connected {
				m.Connected = c
				_ = WriteMarker(dir, m)
			}
		}
	}
}

// Result is the outcome of the last automatic update, kept in the state directory
// (update-result.json) so the new (or restored) app can tell the user what happened.
type Result struct {
	From       string    `json:"from"`
	To         string    `json:"to"`
	OK         bool      `json:"ok"`
	RolledBack bool      `json:"rolledBack,omitempty"`
	Error      string    `json:"error,omitempty"`
	At         time.Time `json:"at"`
}

func resultPath(dir string) string { return filepath.Join(dir, "update-result.json") }

// WriteResult records r in dir.
func WriteResult(dir string, r Result) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	return os.WriteFile(resultPath(dir), b, 0o644)
}

// ReadResult returns the outcome of the last automatic update in dir, if any.
func ReadResult(dir string) (*Result, error) {
	b, err := os.ReadFile(resultPath(dir))
	if err != nil {
		return nil, err
	}
	var r Result
	return &r, json.Unmarshal(b, &r)
}

func sameVersion(a, b string) bool {
	return strings.TrimPrefix(strings.TrimSpace(a), "v") == strings.TrimPrefix(strings.TrimSpace(b), "v")
}
