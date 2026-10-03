//go:build windows

package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// SystemDir is %ProgramData%\NyaTunnel.
func SystemDir() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "NyaTunnel")
}

// prepareDir creates the directory and limits it to SYSTEM and Administrators.
func prepareDir() error {
	dir := SystemDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	out, err := exec.Command("icacls", dir, "/inheritance:r", "/grant:r", "*S-1-5-18:(OI)(CI)F", "*S-1-5-32-544:(OI)(CI)F", "/grant", "*S-1-5-32-545:(RX)").CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls: %v: %s", err, out)
	}
	return nil
}

// AllowInfoRead makes service.json readable by users (the directory ACL only grants traverse/list).
func allowInfoRead() {
	_ = exec.Command("icacls", InfoPath(), "/grant", "*S-1-5-32-545:R").Run()
}

func install(exe string) error {
	allowInfoRead()
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("需要管理员权限：%w", err)
	}
	defer m.Disconnect()
	if s, err := m.OpenService(Name); err == nil {
		s.Close()
		return fmt.Errorf("服务 %s 已存在，请先卸载", Name)
	}
	s, err := m.CreateService(Name, exe, mgr.Config{
		DisplayName: "NyaTunnel", Description: Description, StartType: mgr.StartAutomatic, DelayedAutoStart: true,
	}, "service", "run")
	if err != nil {
		return err
	}
	defer s.Close()
	// Restart after failures: 5 s, 30 s, then every minute.
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: time.Minute},
	}, 86400)
	return s.Start()
}

func uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("需要管理员权限：%w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return fmt.Errorf("服务未安装")
	}
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State != svc.Stopped {
		_, _ = s.Control(svc.Stop)
		for i := 0; i < 50; i++ {
			if st, err := s.Query(); err != nil || st.State == svc.Stopped {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	return s.Delete()
}

func status() (Status, error) {
	m, err := mgr.Connect()
	if err != nil {
		// Non-admins cannot connect with full rights; fall back to the info file.
		if _, ierr := ReadInfo(); ierr == nil {
			return Status{Installed: true, Detail: "无法查询运行状态（需要管理员权限）"}, nil
		}
		return Status{}, err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return Status{}, nil
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return Status{Installed: true}, err
	}
	return Status{Installed: true, Running: st.State == svc.Running}, nil
}

// IsService reports whether the process was started by the service manager.
func IsService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

// RunService runs fn under the service manager until it is stopped.
func RunService(fn func(ctx context.Context) error) error {
	return svc.Run(Name, &handler{fn: fn})
}

type handler struct {
	fn func(ctx context.Context) error
}

func (h *handler) Execute(args []string, req <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.fn(ctx) }()
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		case err := <-done:
			cancel()
			if err != nil {
				return true, 1 // non-zero exit: the recovery actions restart us
			}
			return false, 0
		}
	}
}
