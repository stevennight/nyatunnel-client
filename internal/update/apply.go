package update

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Job installs an update unattended and never leaves the machine without a running client: the
// new version must start (and reconnect, when the old one was connected) within HealthTimeout,
// otherwise the backup is restored and the old version started again. The tunnel is often the
// only way into the machine, so every exit path ends with something running.
type Job struct {
	From, To string
	StateDir string // where the core writes running.json and the result is recorded
	Log      *slog.Logger

	Stop    func() error // stop the running client (the app, or the service)
	Backup  func() error // keep what is needed to restore the current version
	Install func() error // put the new version in place
	Start   func() error // start whatever is installed
	Restore func() error // put the backup back

	HealthTimeout time.Duration // default 2 minutes
	PollInterval  time.Duration // default 2 seconds
}

// Run performs the update and records the outcome in StateDir.
func (j *Job) Run(ctx context.Context) (res Result) {
	if j.HealthTimeout == 0 {
		j.HealthTimeout = 2 * time.Minute
	}
	if j.PollInterval == 0 {
		j.PollInterval = 2 * time.Second
	}
	res = Result{From: j.From, To: j.To}
	defer func() {
		// Whatever happens, leave something running.
		if p := recover(); p != nil {
			j.Log.Error("updater crashed; starting what is installed", "panic", p)
			_ = j.Start()
			res = Result{From: j.From, To: j.To, Error: fmt.Sprintf("更新程序出错：%v", p), At: time.Now()}
			_ = WriteResult(j.StateDir, res)
		}
	}()
	// Ask for a reconnect only when the old version was online: an offline server must not make
	// every update look broken.
	needConnected := false
	if m, err := ReadMarker(j.StateDir); err == nil && sameVersion(m.Version, j.From) {
		needConnected = m.Connected
	}
	j.Log.Info("update starting", "from", j.From, "to", j.To, "requireReconnect", needConnected)

	changed, err := j.attempt(ctx, needConnected)
	if err == nil {
		res.OK = true
		j.Log.Info("update finished", "version", j.To)
	} else {
		res.Error = err.Error()
		j.Log.Error("update failed, restoring the previous version", "err", err, "filesChanged", changed)
		res.RolledBack = j.rollback(ctx, changed)
	}
	res.At = time.Now()
	if werr := WriteResult(j.StateDir, res); werr != nil {
		j.Log.Warn("cannot record the update result", "err", werr)
	}
	return res
}

// attempt installs and checks the new version. changed reports whether installed files may have
// been touched, i.e. whether the backup has to be restored.
func (j *Job) attempt(ctx context.Context, needConnected bool) (changed bool, err error) {
	if err := j.Stop(); err != nil {
		j.Log.Warn("stopping the running client", "err", err)
	}
	if err := j.Backup(); err != nil {
		return false, fmt.Errorf("备份当前版本失败：%w", err)
	}
	if err := j.Install(); err != nil {
		return true, fmt.Errorf("安装新版本失败：%w", err)
	}
	since := time.Now()
	if err := j.Start(); err != nil {
		return true, fmt.Errorf("启动新版本失败：%w", err)
	}
	return true, j.waitHealthy(ctx, j.To, since, needConnected)
}

// rollback brings the previous version back (restoring its files when changed) and reports
// whether it is installed again.
func (j *Job) rollback(ctx context.Context, changed bool) bool {
	if err := j.Stop(); err != nil {
		j.Log.Warn("stopping the new version", "err", err)
	}
	restored := true
	if changed {
		if err := j.Restore(); err != nil {
			restored = false
			j.Log.Error("restoring the previous version", "err", err)
		}
	}
	since := time.Now()
	if err := j.Start(); err != nil {
		j.Log.Error("starting the previous version", "err", err)
		return restored
	}
	// Only logged: versions before automatic updates do not write running.json.
	if err := j.waitHealthy(ctx, j.From, since, false); err != nil {
		j.Log.Warn("previous version did not report back", "err", err)
	} else {
		j.Log.Info("previous version running again", "version", j.From)
	}
	return restored
}

func (j *Job) waitHealthy(ctx context.Context, version string, since time.Time, needConnected bool) error {
	ctx, cancel := context.WithTimeout(ctx, j.HealthTimeout)
	defer cancel()
	t := time.NewTicker(j.PollInterval)
	defer t.Stop()
	started := false
	for {
		if m, err := ReadMarker(j.StateDir); err == nil && sameVersion(m.Version, version) && !m.Started.Before(since.Add(-time.Second)) {
			started = true
			if !needConnected || m.Connected {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			if started {
				return fmt.Errorf("新版本 %s 已启动，但 %s 内没有连上服务器", version, j.HealthTimeout)
			}
			return fmt.Errorf("新版本 %s 在 %s 内没有启动", version, j.HealthTimeout)
		case <-t.C:
		}
	}
}
