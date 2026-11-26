//go:build darwin

package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	serviceLabel  = "io.github.stevennight.nyatunnel"
	serviceDomain = "system"
	serviceTarget = serviceDomain + "/" + serviceLabel
	plistPath     = "/Library/LaunchDaemons/" + serviceLabel + ".plist"
)

// SystemDir is /Library/Application Support/NyaTunnel on macOS.
func SystemDir() string { return "/Library/Application Support/NyaTunnel" }

// BinDir is where the macOS LaunchDaemon binary lives, isolated from the desktop app bundle
// and writable only by root.
func BinDir() string { return filepath.Join(SystemDir(), "bin") }

func prepareDir() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("需要 root 权限（使用 sudo）")
	}
	if err := os.MkdirAll(SystemDir(), 0o755); err != nil {
		return err
	}
	return os.Chmod(SystemDir(), 0o755) // nyatunnel.db itself is written 0600
}

// placeBinary copies exe into BinDir (0755) and returns the installed path.
func placeBinary(exe string) (string, error) {
	target := filepath.Join(BinDir(), "nyatunnel")
	if filepath.Clean(exe) == target {
		return target, nil
	}
	if err := os.MkdirAll(BinDir(), 0o755); err != nil {
		return "", err
	}
	if err := copyBinary(exe, target); err != nil {
		return "", err
	}
	return target, nil
}

func copyBinary(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("无法写入 %s：%w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func runLaunchctl(args ...string) (string, error) {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("launchctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func renderPlist(exe, systemDir string) string {
	errLog := filepath.Join(systemDir, "launchd.err.log")
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>service</string>
    <string>run</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>NYATUNNEL_HOME</key>
    <string>%s</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>ThrottleInterval</key>
  <integer>5</integer>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, xmlEscape(serviceLabel), xmlEscape(exe), xmlEscape(systemDir), xmlEscape(errLog))
}

func writePlist(exe string) error {
	content := renderPlist(exe, SystemDir())
	tmp := plistPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, plistPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func install(exe string) error {
	if _, err := os.Stat(plistPath); err == nil {
		return fmt.Errorf("服务 %s 已存在，请先卸载", Name)
	}
	if err := writePlist(exe); err != nil {
		return err
	}
	// Ensure any stale job definition is cleared before bootstrapping.
	_, _ = runLaunchctl("bootout", serviceTarget)
	if _, err := runLaunchctl("bootstrap", serviceDomain, plistPath); err != nil {
		// Fall back to legacy load on older macOS if bootstrap fails.
		if _, lerr := runLaunchctl("load", "-w", plistPath); lerr != nil {
			_ = os.Remove(plistPath)
			return err
		}
	}
	_, _ = runLaunchctl("enable", serviceTarget)
	return nil
}

func uninstall() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("需要 root 权限（使用 sudo）")
	}
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return fmt.Errorf("服务未安装")
	}
	_, _ = runLaunchctl("bootout", serviceTarget)
	_, _ = runLaunchctl("unload", "-w", plistPath)
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.RemoveAll(BinDir())
	return nil
}

// parseLaunchctlPrint inspects `launchctl print system/<label>` output to determine whether the
// daemon is currently running. `launchctl print` works for non-root users too, unlike `launchctl list`.
func parseLaunchctlPrint(out string) (loaded bool, running bool, detail string) {
	if strings.TrimSpace(out) == "" {
		return false, false, ""
	}
	loaded = true
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "state = ") {
			detail = strings.TrimSpace(strings.TrimPrefix(line, "state = "))
			if detail == "running" {
				running = true
			}
		}
		if strings.HasPrefix(line, "pid = ") {
			pid := strings.TrimSpace(strings.TrimPrefix(line, "pid = "))
			if pid != "" && pid != "0" {
				running = true
			}
		}
	}
	return loaded, running, detail
}

func status() (Status, error) {
	if _, err := os.Stat(plistPath); err != nil {
		return Status{}, nil
	}
	out, err := exec.Command("launchctl", "print", serviceTarget).CombinedOutput()
	if err != nil {
		return Status{Installed: true, Running: false}, nil
	}
	_, running, detail := parseLaunchctlPrint(string(out))
	return Status{Installed: true, Running: running, Detail: detail}, nil
}

// Stop stops the launchd daemon and waits up to timeout for it to exit.
func Stop(timeout time.Duration) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("需要 root 权限（使用 sudo）")
	}
	if _, err := os.Stat(plistPath); err != nil {
		return fmt.Errorf("服务未安装")
	}
	_, _ = runLaunchctl("bootout", serviceTarget)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, _ := status()
		if !st.Running {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	_, _ = runLaunchctl("kill", "SIGKILL", serviceTarget)
	for i := 0; i < 20; i++ {
		st, _ := status()
		if !st.Running {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("服务没有停止")
}

// Start bootstraps and starts the launchd daemon (a running daemon is left alone).
func Start() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("需要 root 权限（使用 sudo）")
	}
	if _, err := os.Stat(plistPath); err != nil {
		return fmt.Errorf("服务未安装")
	}
	if st, err := status(); err == nil && st.Running {
		return nil
	}
	if _, err := runLaunchctl("bootstrap", serviceDomain, plistPath); err != nil {
		// If already bootstrapped (e.g. stopped without bootout), kickstart it.
		if _, kerr := runLaunchctl("kickstart", "-k", serviceTarget); kerr != nil {
			if _, lerr := runLaunchctl("load", "-w", plistPath); lerr != nil {
				return err
			}
		}
	}
	return nil
}

// parsePlistExecutable extracts the first ProgramArguments entry from a launchd plist.
func parsePlistExecutable(data []byte) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	inProgramArgs := false
	inArray := false
	var lastKey string
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "key":
				var k string
				if err := dec.DecodeElement(&k, &t); err != nil {
					return "", err
				}
				lastKey = k
				inProgramArgs = k == "ProgramArguments"
			case "array":
				if inProgramArgs {
					inArray = true
				}
			case "string":
				if inArray {
					var s string
					if err := dec.DecodeElement(&s, &t); err != nil {
						return "", err
					}
					if s != "" {
						return s, nil
					}
				}
			default:
				if lastKey == "ProgramArguments" && t.Name.Local != "array" {
					inProgramArgs = false
				}
			}
		case xml.EndElement:
			if t.Name.Local == "array" && inArray {
				inArray = false
				inProgramArgs = false
			}
		}
	}
	return "", errors.New("plist 缺少 ProgramArguments")
}

// Executable returns the binary path configured in the launchd plist.
func Executable() (string, error) {
	b, err := os.ReadFile(plistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("服务未安装")
		}
		return "", err
	}
	return parsePlistExecutable(b)
}

// SetExecutable points the launchd plist at exe (takes effect on next Start).
func SetExecutable(exe string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("需要 root 权限（使用 sudo）")
	}
	if err := writePlist(exe); err != nil {
		return err
	}
	if info, err := ReadInfo(); err == nil {
		info.Executable = exe
		_ = writeInfo(*info)
	}
	return nil
}

// IsService reports whether this process is running as the installed LaunchDaemon.
func IsService() bool {
	return os.Geteuid() == 0 && os.Getenv("NYATUNNEL_HOME") == SystemDir()
}

// RunService runs fn until SIGTERM or SIGINT is received from launchd.
func RunService(fn func(ctx context.Context) error) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return fn(ctx)
}
