//go:build windows

// fakeapp plays the parts of an installed NyaTunnel for the updater tests, depending on its file
// name: the installer, the core (`nyatunnel.exe version`) and the desktop app. Its version is
// version.txt next to it.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

func main() {
	self, _ := os.Executable()
	dir := filepath.Dir(self)
	name := strings.ToLower(filepath.Base(self))
	switch {
	case strings.Contains(name, "installer"):
		os.Exit(install(self))
	case name == "nyatunnel.exe":
		fmt.Println("nyatunnel v" + version(dir) + " (fake)")
	default: // the desktop app
		v := version(dir)
		if v == os.Getenv("FAKE_DEAD_VERSION") {
			os.Exit(1) // crashes on start
		}
		b, _ := json.Marshal(map[string]any{"version": v, "pid": os.Getpid(), "started": time.Now(), "connected": true})
		_ = os.WriteFile(filepath.Join(os.Getenv("FAKE_STATE_DIR"), "running.json"), b, 0o600)
		time.Sleep(2 * time.Minute)
	}
}

func version(dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "version.txt"))
	return strings.TrimSpace(string(b))
}

// install mimics the NSIS installer: `/S /UPDATE /D=<dir>` with /D= last and unquoted.
func install(self string) int {
	raw := windows.UTF16PtrToString(windows.GetCommandLine())
	i := strings.Index(raw, "/D=")
	if i < 0 || !strings.Contains(raw, " /S ") || !strings.Contains(raw, " /UPDATE ") {
		return 9
	}
	dir := raw[i+3:]
	// A half-done install that then fails must be undone by the updater.
	if err := os.WriteFile(filepath.Join(dir, "version.txt"), []byte(os.Getenv("FAKE_NEW_VERSION")), 0o644); err != nil {
		return 8
	}
	if code, _ := strconv.Atoi(os.Getenv("FAKE_INSTALL_EXIT")); code != 0 {
		return code
	}
	for _, n := range []string{"nyatunnel.exe", "nyatunnel-gui.exe"} {
		b, err := os.ReadFile(self)
		if err != nil || os.WriteFile(filepath.Join(dir, n), b, 0o755) != nil {
			return 7
		}
	}
	return 0
}
