//go:build darwin

package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// spawnDetached starts exe in its own session so it outlives the caller.
func spawnDetached(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// waitPID waits up to timeout for pid to exit; it reports whether the process is gone.
func waitPID(pid int, timeout time.Duration) bool {
	if pid <= 0 {
		return true
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// killUnder terminates every process (except this one) whose executable path starts with dir and
// waits for them to exit. It returns how many were signalled.
func killUnder(dir string) int {
	prefix := filepath.Clean(dir) + "/"
	out, err := exec.Command("ps", "-axo", "pid=,comm=").Output()
	if err != nil {
		return 0
	}
	self := os.Getpid()
	var victims []int
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sp := strings.IndexAny(line, " \t")
		if sp <= 0 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line[:sp]))
		if err != nil || pid <= 0 || pid == self {
			continue
		}
		comm := strings.TrimSpace(line[sp+1:])
		if strings.HasPrefix(comm, prefix) {
			if syscall.Kill(pid, syscall.SIGTERM) == nil {
				victims = append(victims, pid)
			}
		}
	}
	if len(victims) == 0 {
		return 0
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, pid := range victims {
		rem := time.Until(deadline)
		if rem < 0 {
			rem = 0
		}
		if !waitPID(pid, rem) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			waitPID(pid, 2*time.Second)
		}
	}
	return len(victims)
}

// installedVersion asks an installed nyatunnel binary for its version.
func installedVersion(exe string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "version").Output()
	if err != nil {
		return "", err
	}
	// "nyatunnel v0.1.4 (commit) built …"
	f := strings.Fields(string(out))
	if len(f) < 2 {
		return "", fmt.Errorf("unexpected version output %q", out)
	}
	return strings.TrimPrefix(f[1], "v"), nil
}

// checkVersion fails unless exe reports version want.
func checkVersion(exe, want string) error {
	got, err := installedVersion(exe)
	if err != nil {
		return fmt.Errorf("无法运行 %s：%w", filepath.Base(exe), err)
	}
	if !sameVersion(got, want) {
		return fmt.Errorf("安装后的版本是 %s，不是 %s", got, want)
	}
	return nil
}

// retry runs f up to n times, a second apart, while it fails.
func retry(n int, f func() error) error {
	var err error
	for i := 0; i < n; i++ {
		if err = f(); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return err
}
