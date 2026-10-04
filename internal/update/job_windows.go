//go:build windows

package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nyatunnel-client/internal/service"
)

// Run installs the desktop app update. It is started by the app (a copy of nyatunnel.exe outside
// the install directory), waits for the app to exit, installs silently and starts the new
// version in the tray; on any failure the previous files are restored and the old version
// started instead.
func (u GUIUpdate) Run(ctx context.Context, log *slog.Logger) Result {
	// Only ever touch a real installation: the updater kills and backs up everything in AppDir.
	for _, f := range []string{"uninstall.exe", "nyatunnel.exe", u.App} {
		if st, err := os.Stat(filepath.Join(u.AppDir, f)); err != nil || st.IsDir() || u.App == "" {
			res := Result{From: u.From, To: u.To, Error: "不是安装版的 NyaTunnel 目录，未做任何改动：" + u.AppDir, At: time.Now()}
			log.Error("refusing to update", "dir", u.AppDir, "missing", f)
			waitPID(u.WaitPID, 30*time.Second)
			_ = spawnDetached(filepath.Join(u.AppDir, u.App), "--autostart")
			_ = WriteResult(u.StateDir, res)
			return res
		}
	}
	backup := filepath.Join(UpdateDir(), "backup")
	j := &Job{
		From: u.From, To: u.To, StateDir: u.StateDir, Log: log, HealthTimeout: u.HealthTimeout,
		Stop: func() error {
			if !waitPID(u.WaitPID, 30*time.Second) {
				log.Warn("the app did not exit in time; stopping it")
			}
			if n := killUnder(u.AppDir); n > 0 {
				log.Info("stopped processes running from the install directory", "count", n)
			}
			return nil
		},
		Backup: func() error {
			if err := os.RemoveAll(backup); err != nil {
				return err
			}
			return copyTree(u.AppDir, backup)
		},
		Install: func() error {
			ictx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
			// /S silent, /UPDATE keeps shortcuts and settings, /D= the current location (last, unquoted).
			code, err := runHidden(ictx, u.Installer, fmt.Sprintf(`"%s" /S /UPDATE /D=%s`, u.Installer, u.AppDir))
			if err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("安装程序退出码 %d", code)
			}
			return checkVersion(filepath.Join(u.AppDir, "nyatunnel.exe"), u.To)
		},
		// --autostart: back in the tray, as after a reboot; nobody may be at the screen.
		Start: func() error { return spawnDetached(filepath.Join(u.AppDir, u.App), "--autostart") },
		Restore: func() error {
			return retry(10, func() error { return copyTree(backup, u.AppDir) })
		},
	}
	return j.Run(ctx)
}

// Run replaces the service binary with the verified new one, migrating the service into BinDir
// when it still runs from elsewhere, and restores the previous binary and path on failure.
func (u ServiceUpdate) Run(ctx context.Context, log *slog.Logger) Result {
	target := filepath.Join(service.BinDir(), "nyatunnel.exe")
	bak := target + ".bak"
	oldExe, err := service.Executable()
	if err != nil {
		res := Result{From: u.From, To: u.To, Error: "无法读取服务配置：" + err.Error(), At: time.Now()}
		log.Error("cannot read the service configuration", "err", err)
		_ = service.Start()
		_ = WriteResult(service.SystemDir(), res)
		return res
	}
	migrate := !strings.EqualFold(filepath.Clean(oldExe), target)
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
			if err := retry(10, func() error { return copyFile(u.New, target) }); err != nil {
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
				errs = append(errs, retry(10, func() error { return copyFile(bak, target) }))
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
// version and hands over to a detached updater, which stops the service, replaces its binary and
// restarts it. The running service calls it periodically; `nyatunnel service update` calls it
// with force, which also retries a version that failed recently. It returns the version being
// installed, or "" when there is nothing to do.
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
	newExe := filepath.Join(dir, "nyatunnel-"+r.Version+".exe")
	if err := os.WriteFile(newExe+".part", bin, 0o700); err != nil {
		return "", err
	}
	if err := os.Rename(newExe+".part", newExe); err != nil {
		return "", err
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	// The updater runs from a copy: the service's own binary is about to be replaced.
	updater := filepath.Join(dir, "nyatunnel-updater.exe")
	if err := copyFile(self, updater); err != nil {
		return "", fmt.Errorf("无法准备更新程序（可能已有更新在进行）：%w", err)
	}
	if err := spawnDetached(updater, "update", "apply", "--mode", "service", "--new", newExe, "--from", current, "--to", r.Version); err != nil {
		return "", err
	}
	return r.Version, nil
}
