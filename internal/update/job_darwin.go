//go:build darwin

package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"nyatunnel-client/internal/service"
)

// Run installs the macOS desktop app update unattended. It is started by the app (a copy of the
// bundled nyatunnel binary outside the .app bundle), waits for the app to exit, mounts the
// verified DMG, backs up and replaces the .app bundle, and relaunches it with --autostart.
// If the new version fails to start or reconnect within HealthTimeout, the backup is restored
// and the old version started instead.
func (u GUIUpdate) Run(ctx context.Context, log *slog.Logger) Result {
	appBundle := filepath.Clean(u.AppDir)
	sidecarRel := filepath.Join("Contents", "MacOS", "nyatunnel")
	infoPlist := filepath.Join(appBundle, "Contents", "Info.plist")
	sidecarPath := filepath.Join(appBundle, sidecarRel)
	if !strings.HasSuffix(appBundle, ".app") || !isFile(infoPlist) || !isFile(sidecarPath) {
		res := Result{From: u.From, To: u.To, Error: "不是安装版的 NyaTunnel.app，未做任何改动：" + appBundle, At: time.Now()}
		log.Error("refusing to update", "appBundle", appBundle)
		waitPID(u.WaitPID, 30*time.Second)
		_ = launchAppBundle(appBundle)
		_ = WriteResult(u.StateDir, res)
		return res
	}

	backup := filepath.Join(UpdateDir(), "backup.app")
	j := &Job{
		From: u.From, To: u.To, StateDir: u.StateDir, Log: log, HealthTimeout: u.HealthTimeout,
		Stop: func() error {
			if !waitPID(u.WaitPID, 30*time.Second) {
				log.Warn("the app did not exit in time; stopping it")
			}
			if n := killUnder(appBundle); n > 0 {
				log.Info("stopped processes running from the app bundle", "count", n)
			}
			return nil
		},
		Backup: func() error {
			if err := os.RemoveAll(backup); err != nil {
				return err
			}
			return copyAppBundle(ctx, appBundle, backup)
		},
		Install: func() error {
			return installFromDMG(ctx, u.Installer, appBundle, u.App, u.To)
		},
		Start: func() error {
			return launchAppBundle(appBundle)
		},
		Restore: func() error {
			return retry(5, func() error {
				return replaceAppBundle(ctx, backup, appBundle)
			})
		},
	}
	return j.Run(ctx)
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// copyAppBundle duplicates a macOS .app bundle preserving permissions, symlinks, and bundle metadata.
func copyAppBundle(ctx context.Context, src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "ditto", src, dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ditto %s -> %s: %v: %s", src, dst, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// replaceAppBundle replaces dstApp with a copy of srcApp safely via sibling staging directories.
func replaceAppBundle(ctx context.Context, srcApp, dstApp string) error {
	parent := filepath.Dir(dstApp)
	base := filepath.Base(dstApp)
	staged := filepath.Join(parent, "."+base+".new")
	prev := filepath.Join(parent, "."+base+".old")

	_ = os.RemoveAll(staged)
	_ = os.RemoveAll(prev)
	if err := copyAppBundle(ctx, srcApp, staged); err != nil {
		return err
	}
	// Clear quarantine xattr if present so Gatekeeper does not block unattended relaunch.
	_ = exec.CommandContext(ctx, "xattr", "-dr", "com.apple.quarantine", staged).Run()

	if _, err := os.Stat(dstApp); err == nil {
		if err := os.Rename(dstApp, prev); err != nil {
			_ = os.RemoveAll(staged)
			return fmt.Errorf("无法移走旧版本：%w", err)
		}
	}
	if err := os.Rename(staged, dstApp); err != nil {
		_ = os.Rename(prev, dstApp)
		_ = os.RemoveAll(staged)
		return fmt.Errorf("无法替换新版本：%w", err)
	}
	_ = os.RemoveAll(prev)
	return nil
}

func installFromDMG(ctx context.Context, dmgPath, dstApp, preferredApp, wantVersion string) error {
	ictx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	mnt, err := os.MkdirTemp(UpdateDir(), "mnt-*")
	if err != nil {
		return fmt.Errorf("创建挂载目录失败：%w", err)
	}
	defer os.RemoveAll(mnt)

	out, err := exec.CommandContext(ictx, "hdiutil", "attach", "-nobrowse", "-readonly", "-noverify", "-mountpoint", mnt, dmgPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("挂载安装包失败：%v: %s", err, strings.TrimSpace(string(out)))
	}
	defer func() {
		_ = exec.Command("hdiutil", "detach", "-force", mnt).Run()
	}()

	srcApp, err := findAppBundle(mnt, preferredApp)
	if err != nil {
		return err
	}
	srcSidecar := filepath.Join(srcApp, "Contents", "MacOS", "nyatunnel")
	if err := checkVersion(srcSidecar, wantVersion); err != nil {
		return err
	}
	if err := replaceAppBundle(ictx, srcApp, dstApp); err != nil {
		return err
	}
	return checkVersion(filepath.Join(dstApp, "Contents", "MacOS", "nyatunnel"), wantVersion)
}

func findAppBundle(dir, preferred string) (string, error) {
	if preferred != "" && strings.HasSuffix(preferred, ".app") {
		cand := filepath.Join(dir, preferred)
		if st, err := os.Stat(cand); err == nil && st.IsDir() {
			return cand, nil
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".app") {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", errors.New("安装包中找不到 .app 程序")
}

func launchAppBundle(appBundle string) error {
	out, err := exec.Command("open", "-n", "-a", appBundle, "--args", "--autostart").CombinedOutput()
	if err == nil {
		return nil
	}
	return fmt.Errorf("启动应用失败：%v: %s", err, strings.TrimSpace(string(out)))
}

// Run replaces the LaunchDaemon binary with the verified new one, migrating the service into
// BinDir when it still runs from elsewhere, and restores the previous binary and path on failure.
func (u ServiceUpdate) Run(ctx context.Context, log *slog.Logger) Result {
	target := filepath.Join(service.BinDir(), "nyatunnel")
	bak := target + ".bak"
	oldExe, err := service.Executable()
	if err != nil {
		res := Result{From: u.From, To: u.To, Error: "无法读取服务配置：" + err.Error(), At: time.Now()}
		log.Error("cannot read the service configuration", "err", err)
		_ = service.Start()
		_ = WriteResult(service.SystemDir(), res)
		return res
	}
	migrate := filepath.Clean(oldExe) != target
	_, statErr := os.Stat(target)
	hadTarget := statErr == nil && !migrate
	j := &Job{
		From: u.From, To: u.To, StateDir: service.SystemDir(), Log: log,
		Stop: func() error { return service.Stop(30 * time.Second) },
		Backup: func() error {
			if hadTarget {
				return copyFile(target, bak)
			}
			return nil
		},
		Install: func() error {
			if err := retry(10, func() error {
				if err := copyFile(u.New, target); err != nil {
					return err
				}
				return os.Chmod(target, 0o755)
			}); err != nil {
				return err
			}
			if err := checkVersion(target, u.To); err != nil {
				return err
			}
			if migrate {
				log.Info("moving the service into its own directory", "from", oldExe, "to", target)
				return service.SetExecutable(target)
			}
			return nil
		},
		Start: service.Start,
		Restore: func() error {
			var errs []error
			if hadTarget {
				errs = append(errs, retry(10, func() error {
					if err := copyFile(bak, target); err != nil {
						return err
					}
					return os.Chmod(target, 0o755)
				}))
			}
			if migrate {
				errs = append(errs, service.SetExecutable(oldExe))
			}
			return errors.Join(errs...)
		},
	}
	return j.Run(ctx)
}

// StartServiceUpdate downloads and verifies the latest CLI when it is newer than the service's
// version and hands over to a detached updater, which stops the LaunchDaemon, replaces its binary
// and restarts it. Returns the version being installed, or "" when there is nothing to do.
func StartServiceUpdate(ctx context.Context, force bool) (string, error) {
	exe, err := service.Executable()
	if err != nil {
		return "", err
	}
	current, err := installedVersion(exe)
	if err != nil {
		return "", fmt.Errorf("无法读取服务的版本：%w", err)
	}
	r, err := Latest(ctx)
	if err != nil {
		return "", err
	}
	if !Newer(r.Version, current) || (!force && RecentlyFailed(service.SystemDir(), r.Version)) {
		return "", nil
	}
	dir := filepath.Join(service.SystemDir(), "update")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := CLIAsset(r.Version)
	archive, err := fetchVerified(ctx, r, name, 200<<20)
	if err != nil {
		return "", err
	}
	bin, err := extract(name, archive)
	if err != nil {
		return "", err
	}
	newExe := filepath.Join(dir, "nyatunnel-"+r.Version)
	if err := os.WriteFile(newExe+".part", bin, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(newExe+".part", newExe); err != nil {
		return "", err
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	updater := filepath.Join(dir, "nyatunnel-updater")
	if err := copyFile(self, updater); err != nil {
		return "", fmt.Errorf("无法准备更新程序（可能已有更新在进行）：%w", err)
	}
	if err := os.Chmod(updater, 0o755); err != nil {
		return "", err
	}
	if err := spawnDetached(updater, "update", "apply", "--mode", "service", "--new", newExe, "--from", current, "--to", r.Version); err != nil {
		return "", err
	}
	return r.Version, nil
}
