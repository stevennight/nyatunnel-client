// Package service installs the client core as an operating-system service (Windows service,
// systemd unit, launchd daemon), so tunnels keep running without anyone signed in.
//
// The service has its own configuration directory (SystemDir). Installing moves the current
// identity there, readable only by administrators / root; a small non-secret service.json next
// to it lets the desktop app show which device the service runs.
package service

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Name is the service name on every platform.
const Name = "NyaTunnel"

// Description appears in the service manager.
const Description = "NyaTunnel 内网穿透客户端"

// ErrUnsupported is returned on platforms without service support.
var ErrUnsupported = errors.New("this platform does not support installing a service")

// Info is the public description of the installed service (no secrets).
type Info struct {
	Server     string `json:"server"`
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName"`
	Executable string `json:"executable"`
}

// InfoPath is service.json in the system directory.
func InfoPath() string { return filepath.Join(SystemDir(), "service.json") }

// ReadInfo returns the installed service's description, if any.
func ReadInfo() (*Info, error) {
	b, err := os.ReadFile(InfoPath())
	if err != nil {
		return nil, err
	}
	var i Info
	return &i, json.Unmarshal(b, &i)
}

func writeInfo(i Info) error {
	b, _ := json.MarshalIndent(i, "", "  ")
	return os.WriteFile(InfoPath(), b, 0o644)
}

// Status describes the service.
type Status struct {
	Installed bool
	Running   bool
	Detail    string
}

// Install registers the service to run `<exe> run` with NYATUNNEL_HOME=SystemDir and starts it.
func Install(exe string, info Info) error {
	if err := prepareDir(); err != nil {
		return err
	}
	info.Executable = exe
	if err := writeInfo(info); err != nil {
		return err
	}
	return install(exe)
}

// Uninstall stops and removes the service. The identity stays in SystemDir unless purge is set.
func Uninstall(purge bool) error {
	if err := uninstall(); err != nil {
		return err
	}
	_ = os.Remove(InfoPath())
	if purge {
		return os.RemoveAll(SystemDir())
	}
	return nil
}

// QueryStatus reports whether the service is installed and running.
func QueryStatus() (Status, error) { return status() }
