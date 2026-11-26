//go:build linux

package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SystemDir is /etc/nyatunnel on Linux.
func SystemDir() string { return "/etc/nyatunnel" }

func prepareDir() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("需要 root 权限（使用 sudo）")
	}
	if err := os.MkdirAll(SystemDir(), 0o755); err != nil {
		return err
	}
	return os.Chmod(SystemDir(), 0o755) // identity.json itself is written 0600
}

const unitPath = "/etc/systemd/system/nyatunnel.service"

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, out)
	}
	return nil
}

func install(exe string) error {
	unit := fmt.Sprintf(`[Unit]
Description=%s
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s run
Environment=NYATUNNEL_HOME=%s
Restart=always
RestartSec=5
# Exit code 3 means the device was revoked: do not restart forever.
RestartPreventExitStatus=3
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=%s
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
`, Description, exe, SystemDir(), SystemDir())
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	return run("systemctl", "enable", "--now", "nyatunnel.service")
}

func uninstall() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("需要 root 权限（使用 sudo）")
	}
	_ = run("systemctl", "disable", "--now", "nyatunnel.service")
	if err := os.Remove(unitPath); err != nil {
		return err
	}
	return run("systemctl", "daemon-reload")
}

func status() (Status, error) {
	if _, err := os.Stat(unitPath); err != nil {
		return Status{}, nil
	}
	out, _ := exec.Command("systemctl", "is-active", "nyatunnel.service").Output()
	state := strings.TrimSpace(string(out))
	return Status{Installed: true, Running: state == "active", Detail: state}, nil
}

// IsService is false: systemd runs `nyatunnel run` like any process.
func IsService() bool { return false }

// RunService is only used on Windows and macOS.
func RunService(fn func(ctx context.Context) error) error { return ErrUnsupported }

// The service runs the binary it was installed from; automatic service updates are Windows/macOS-only.

func BinDir() string                         { return filepath.Join(SystemDir(), "bin") }
func placeBinary(exe string) (string, error) { return exe, nil }
func Stop(time.Duration) error               { return ErrUnsupported }
func Start() error                           { return ErrUnsupported }
func Executable() (string, error)            { return "", ErrUnsupported }
func SetExecutable(string) error             { return ErrUnsupported }
