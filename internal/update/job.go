package update

import (
	"os"
	"path/filepath"
	"time"

	"nyatunnel-client/internal/service"
)

// GUIUpdate is an installer run for the per-user desktop app.
type GUIUpdate struct {
	Installer string // verified installer in UpdateDir
	AppDir    string // install directory of the running app
	App       string // the app's executable name inside AppDir
	WaitPID   int    // the app process that started the updater
	From, To  string
	StateDir  string // the core's state directory (running.json)

	HealthTimeout time.Duration // how long the new version gets to report back (default 2 minutes)
}

// ServiceUpdate replaces the system service's binary.
type ServiceUpdate struct {
	New      string // verified new nyatunnel executable
	From, To string
}

// RecentlyFailed reports whether an automatic update to version failed in the last day; automatic
// attempts then wait instead of interrupting the tunnels again every few hours.
func RecentlyFailed(stateDir, version string) bool {
	r, err := ReadResult(stateDir)
	return err == nil && !r.OK && sameVersion(r.To, version) && time.Since(r.At) < 24*time.Hour
}

func autoUpdateOffPath() string { return filepath.Join(service.SystemDir(), "auto-update-off") }

// ServiceAutoUpdate reports whether the system service installs new releases by itself (default on).
func ServiceAutoUpdate() bool {
	_, err := os.Stat(autoUpdateOffPath())
	return err != nil
}

// SetServiceAutoUpdate turns automatic service updates on or off.
func SetServiceAutoUpdate(on bool) error {
	if on {
		if err := os.Remove(autoUpdateOffPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(autoUpdateOffPath(), []byte("automatic updates are turned off\n"), 0o644)
}
