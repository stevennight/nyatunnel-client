//go:build windows

package update

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeApp builds testdata/fakeapp once per test.
func fakeApp(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "fakeapp.exe")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/fakeapp")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake app: %v\n%s", err, b)
	}
	return out
}

// installFake lays out an installed 0.1.4 in a directory with a space (the /D= edge case) and
// starts its desktop app, which the updater has to stop.
func installFake(t *testing.T, bin string) (appDir, stateDir string) {
	t.Helper()
	root := t.TempDir()
	appDir = filepath.Join(root, "Nya Tunnel")
	stateDir = filepath.Join(root, "state")
	for _, d := range []string{appDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"nyatunnel.exe", "nyatunnel-gui.exe", "uninstall.exe"} {
		if err := copyFile(bin, filepath.Join(appDir, n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(appDir, "version.txt"), []byte("0.1.4"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_STATE_DIR", stateDir)
	t.Setenv("TMP", filepath.Join(root, "tmp")) // UpdateDir (and the backup) live under %TMP%
	_ = os.MkdirAll(filepath.Join(root, "tmp"), 0o755)
	if err := spawnDetached(filepath.Join(appDir, "nyatunnel-gui.exe")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { m, err := ReadMarker(stateDir); return err == nil && m.Version == "0.1.4" })
	// Mark the old version as online, so the new one has to reconnect too.
	m, _ := ReadMarker(stateDir)
	m.Started = time.Now().Add(-time.Hour)
	_ = WriteMarker(stateDir, *m)
	t.Cleanup(func() { killUnder(appDir) })
	return appDir, stateDir
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}

func runFakeUpdate(t *testing.T, bin, appDir, stateDir string) Result {
	t.Helper()
	installer := filepath.Join(t.TempDir(), "fake-installer.exe")
	if err := copyFile(bin, installer); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_NEW_VERSION", "0.1.5")
	return GUIUpdate{
		Installer: installer, AppDir: appDir, App: "nyatunnel-gui.exe",
		From: "0.1.4", To: "0.1.5", StateDir: stateDir, HealthTimeout: 5 * time.Second,
	}.Run(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func installedFake(t *testing.T, appDir string) string {
	b, err := os.ReadFile(filepath.Join(appDir, "version.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestGUIUpdateEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs helper processes")
	}
	bin := fakeApp(t)

	t.Run("installs and restarts the new version", func(t *testing.T) {
		appDir, stateDir := installFake(t, bin)
		res := runFakeUpdate(t, bin, appDir, stateDir)
		if !res.OK || installedFake(t, appDir) != "0.1.5" {
			t.Fatalf("result %+v, installed %s", res, installedFake(t, appDir))
		}
		if m, _ := ReadMarker(stateDir); m.Version != "0.1.5" || !m.Connected {
			t.Fatalf("new version not running: %+v", m)
		}
	})

	t.Run("a failing installer is undone and the old version restarted", func(t *testing.T) {
		appDir, stateDir := installFake(t, bin)
		t.Setenv("FAKE_INSTALL_EXIT", "2")
		res := runFakeUpdate(t, bin, appDir, stateDir)
		if res.OK || !res.RolledBack || !strings.Contains(res.Error, "退出码 2") {
			t.Fatalf("result %+v", res)
		}
		if v := installedFake(t, appDir); v != "0.1.4" {
			t.Fatalf("installed %s after rollback", v)
		}
		waitFor(t, func() bool {
			m, err := ReadMarker(stateDir)
			return err == nil && m.Version == "0.1.4" && time.Since(m.Started) < time.Minute
		})
	})

	t.Run("a new version that does not start is rolled back", func(t *testing.T) {
		appDir, stateDir := installFake(t, bin)
		t.Setenv("FAKE_DEAD_VERSION", "0.1.5")
		res := runFakeUpdate(t, bin, appDir, stateDir)
		if res.OK || !res.RolledBack || installedFake(t, appDir) != "0.1.4" {
			t.Fatalf("result %+v, installed %s", res, installedFake(t, appDir))
		}
		waitFor(t, func() bool {
			m, err := ReadMarker(stateDir)
			return err == nil && m.Version == "0.1.4" && time.Since(m.Started) < time.Minute
		})
	})
}

func TestGUIUpdateRefusesAPlainFolder(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	keep := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := GUIUpdate{Installer: "x.exe", AppDir: dir, App: "nyatunnel-gui.exe", From: "0.1.4", To: "0.1.5", StateDir: state}.
		Run(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if res.OK || !strings.Contains(res.Error, "不是安装版") {
		t.Fatalf("result %+v", res)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("the folder must be left alone")
	}
}
