//go:build linux || darwin

package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// SystemDir is /etc/nyatunnel on Linux and /Library/Application Support/NyaTunnel on macOS.
func SystemDir() string {
	if runtime.GOOS == "darwin" {
		return "/Library/Application Support/NyaTunnel"
	}
	return "/etc/nyatunnel"
}

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
const plistPath = "/Library/LaunchDaemons/io.github.stevennight.nyatunnel.plist"

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, out)
	}
	return nil
}

func install(exe string) error {
	if runtime.GOOS == "darwin" {
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>io.github.stevennight.nyatunnel</string>
  <key>ProgramArguments</key><array><string>%s</string><string>run</string></array>
  <key>EnvironmentVariables</key><dict><key>NYATUNNEL_HOME</key><string>%s</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>/var/log/nyatunnel.log</string>
</dict></plist>
`, exe, SystemDir())
		if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
			return err
		}
		return run("launchctl", "load", "-w", plistPath)
	}
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
	if runtime.GOOS == "darwin" {
		_ = run("launchctl", "unload", "-w", plistPath)
		return os.Remove(plistPath)
	}
	_ = run("systemctl", "disable", "--now", "nyatunnel.service")
	if err := os.Remove(unitPath); err != nil {
		return err
	}
	return run("systemctl", "daemon-reload")
}

func status() (Status, error) {
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat(plistPath); err != nil {
			return Status{}, nil
		}
		err := exec.Command("launchctl", "list", "io.github.stevennight.nyatunnel").Run()
		return Status{Installed: true, Running: err == nil}, nil
	}
	if _, err := os.Stat(unitPath); err != nil {
		return Status{}, nil
	}
	out, _ := exec.Command("systemctl", "is-active", "nyatunnel.service").Output()
	state := strings.TrimSpace(string(out))
	return Status{Installed: true, Running: state == "active", Detail: state}, nil
}

// IsService is false: systemd and launchd run `nyatunnel run` like any process.
func IsService() bool { return false }

// RunService is only used on Windows.
func RunService(fn func(ctx context.Context) error) error { return ErrUnsupported }
