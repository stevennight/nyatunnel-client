package update

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// fakeClient simulates the installed client: Start writes the marker the way a starting core does.
type fakeClient struct {
	t         *testing.T
	dir       string
	installed string // version on disk
	backup    string
	connects  bool // whether a started version reaches the server
	failAt    string
	calls     []string
}

func (f *fakeClient) step(name string, fn func()) error {
	f.calls = append(f.calls, name)
	if f.failAt == name {
		return errors.New(name + " failed")
	}
	if fn != nil {
		fn()
	}
	return nil
}

func (f *fakeClient) job(to string) *Job {
	from := f.installed
	return &Job{
		From: from, To: to, StateDir: f.dir,
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		HealthTimeout: 300 * time.Millisecond,
		PollInterval:  10 * time.Millisecond,
		Stop:          func() error { return f.step("stop", nil) },
		Backup:        func() error { return f.step("backup", func() { f.backup = f.installed }) },
		Install:       func() error { return f.step("install", func() { f.installed = to }) },
		Restore:       func() error { return f.step("restore", func() { f.installed = f.backup }) },
		Start: func() error {
			return f.step("start", func() {
				if f.installed == "broken" {
					return // crashes before writing anything
				}
				if err := WriteMarker(f.dir, Marker{Version: f.installed, Started: time.Now(), Connected: f.connects}); err != nil {
					f.t.Fatal(err)
				}
			})
		},
	}
}

func newFake(t *testing.T, connected bool) *fakeClient {
	dir := t.TempDir()
	if err := WriteMarker(dir, Marker{Version: "v0.1.4", Started: time.Now().Add(-time.Hour), Connected: connected}); err != nil {
		t.Fatal(err)
	}
	return &fakeClient{t: t, dir: dir, installed: "0.1.4", connects: true}
}

func TestJobInstallsAndConfirmsTheNewVersion(t *testing.T) {
	f := newFake(t, true)
	res := f.job("0.1.5").Run(context.Background())
	if !res.OK || res.RolledBack || f.installed != "0.1.5" {
		t.Fatalf("result %+v installed %s", res, f.installed)
	}
	if got := strings.Join(f.calls, ","); got != "stop,backup,install,start" {
		t.Fatalf("calls %s", got)
	}
	if r, err := ReadResult(f.dir); err != nil || !r.OK || r.To != "0.1.5" {
		t.Fatalf("recorded %+v %v", r, err)
	}
}

func TestJobRestoresWhenTheInstallerFails(t *testing.T) {
	f := newFake(t, true)
	f.failAt = "install"
	res := f.job("0.1.5").Run(context.Background())
	if res.OK || !res.RolledBack || f.installed != "0.1.4" {
		t.Fatalf("result %+v installed %s", res, f.installed)
	}
	// The old version is started again after the restore.
	if got := strings.Join(f.calls, ","); got != "stop,backup,install,stop,restore,start" {
		t.Fatalf("calls %s", got)
	}
	if !RecentlyFailed(f.dir, "v0.1.5") || RecentlyFailed(f.dir, "0.1.6") {
		t.Fatal("a failed version must be held back for a while, other versions not")
	}
}

func TestJobRestoresWhenTheNewVersionDoesNotStart(t *testing.T) {
	f := newFake(t, true)
	res := f.job("broken").Run(context.Background())
	if res.OK || !res.RolledBack || f.installed != "0.1.4" || !strings.Contains(res.Error, "没有启动") {
		t.Fatalf("result %+v installed %s", res, f.installed)
	}
	if m, _ := ReadMarker(f.dir); m.Version != "0.1.4" {
		t.Fatalf("the old version should be running again, marker %+v", m)
	}
}

func TestJobRequiresAReconnectOnlyWhenTheOldVersionWasOnline(t *testing.T) {
	online := newFake(t, true)
	online.connects = false
	if res := online.job("0.1.5").Run(context.Background()); res.OK || !strings.Contains(res.Error, "没有连上服务器") {
		t.Fatalf("online before, offline after must roll back: %+v", res)
	}

	offline := newFake(t, false)
	offline.connects = false
	if res := offline.job("0.1.5").Run(context.Background()); !res.OK {
		t.Fatalf("an unreachable server must not block the update: %+v", res)
	}
}

func TestJobNeverRestoresAFailedBackup(t *testing.T) {
	f := newFake(t, true)
	f.failAt = "backup"
	res := f.job("0.1.5").Run(context.Background())
	if res.OK || f.installed != "0.1.4" {
		t.Fatalf("result %+v installed %s", res, f.installed)
	}
	if got := strings.Join(f.calls, ","); got != "stop,backup,stop,start" {
		t.Fatalf("calls %s", got)
	}
}

func TestJobStartsTheClientEvenIfTheUpdaterPanics(t *testing.T) {
	f := newFake(t, true)
	j := f.job("0.1.5")
	j.Install = func() error { panic("boom") }
	res := j.Run(context.Background())
	if res.OK || !strings.Contains(res.Error, "boom") {
		t.Fatalf("result %+v", res)
	}
	if f.calls[len(f.calls)-1] != "start" {
		t.Fatalf("calls %v: the client must be started", f.calls)
	}
}
