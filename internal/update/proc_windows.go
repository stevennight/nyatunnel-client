//go:build windows

package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	createNoWindow         = 0x0800_0000
	detachedProcess        = 0x0000_0008
	createNewProcessGroup  = 0x0000_0200
	createBreakawayFromJob = 0x0100_0000
)

// spawnDetached starts exe so that it outlives the caller (and the caller's job object, when the
// system allows breaking away from it), without a console window.
func spawnDetached(exe string, args ...string) error {
	start := func(flags uint32) error {
		cmd := exec.Command(exe, args...)
		cmd.Dir = filepath.Dir(exe)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	base := uint32(detachedProcess | createNewProcessGroup | createNoWindow)
	if err := start(base | createBreakawayFromJob); err == nil {
		return nil
	}
	return start(base)
}

// runHidden runs a raw command line without a window and returns its exit code. The raw form
// matters for NSIS: /D= must come last and unquoted, which exec's argument quoting would break.
func runHidden(ctx context.Context, exe, cmdline string) (int, error) {
	cmd := exec.CommandContext(ctx, exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: cmdline, HideWindow: true, CreationFlags: createNoWindow}
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// waitPID waits up to timeout for a process to exit; it reports whether it did.
func waitPID(pid int, timeout time.Duration) bool {
	if pid <= 0 {
		return true
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true // already gone
	}
	defer windows.CloseHandle(h)
	ev, _ := windows.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
	return ev == windows.WAIT_OBJECT_0
}

// killUnder terminates every process (except this one) whose executable lives in dir and waits
// for them to exit. It returns how many were stopped.
func killUnder(dir string) int {
	dir = strings.ToLower(filepath.Clean(dir)) + `\`
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	self := uint32(os.Getpid())
	var victims []windows.Handle
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == self || e.ProcessID == 0 {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, e.ProcessID)
		if err != nil {
			continue
		}
		buf := make([]uint16, windows.MAX_LONG_PATH)
		n := uint32(len(buf))
		if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil &&
			strings.HasPrefix(strings.ToLower(windows.UTF16ToString(buf[:n])), dir) {
			if windows.TerminateProcess(h, 1) == nil {
				victims = append(victims, h)
				continue
			}
		}
		windows.CloseHandle(h)
	}
	for _, h := range victims {
		windows.WaitForSingleObject(h, 10_000)
		windows.CloseHandle(h)
	}
	return len(victims)
}

// installedVersion asks an installed nyatunnel.exe for its version.
func installedVersion(exe string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "version")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.Output()
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

// retry runs f up to n times, a second apart, while it fails (files can stay locked briefly
// after their process exited).
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
