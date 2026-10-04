// Command nyatunnel is the NyaTunnel client core and CLI. It enrolls this device with a
// NyaTunnel server and runs the tunnels assigned to it.
package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/stevennight/nyatunnel-common/deeplink"

	"nyatunnel-client/internal/agent"
	"nyatunnel-client/internal/daemon"
	"nyatunnel-client/internal/enroll"
	"nyatunnel-client/internal/identity"
	"nyatunnel-client/internal/logbuf"
	"nyatunnel-client/internal/service"
	"nyatunnel-client/internal/shared/version"
	"nyatunnel-client/internal/update"
)

const usage = `用法: nyatunnel <命令> [参数]

命令:
  enroll <服务器> <注册码>   将本机注册到 NyaTunnel 服务器
         [--name 设备名] [--yes]
  enroll <nyatunnel://…>     用邀请链接注册
  run                        连接服务器并运行分配给本机的隧道（前台运行，Ctrl+C 退出）
  status                     显示本机注册信息与隧道
  logout                     删除本机的设备密钥（服务器上的设备需由管理员吊销）
  link <nyatunnel://…>       解析并校验一个深链
  daemon                     供图形界面使用的后台模式（本机回环 HTTP 接口）
  service install            安装为系统服务（开机即运行，无需登录；需要管理员 / root）
  service uninstall [--purge] 卸载系统服务（--purge 同时删除服务使用的设备密钥）
  service status             查看系统服务状态
  service auto-update on|off 系统服务是否自动安装新版本（默认开启，仅 Windows）
  service update             立即把系统服务更新到最新版本（失败自动恢复，仅 Windows）
  update [--check]           检查并安装新版本（从 GitHub Releases 下载并校验 SHA256SUMS）
  version                    显示版本

环境变量:
  NYATUNNEL_HOME             配置目录（默认为系统的用户配置目录下的 NyaTunnel）
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "nyatunnel", version.String())
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	case "link":
		return cmdLink(rest, stdout, stderr)
	case "enroll":
		return cmdEnroll(rest, stdin, stdout, stderr)
	case "run":
		return cmdRun(stderr)
	case "status":
		return cmdStatus(stdout, stderr)
	case "logout":
		return cmdLogout(stdout, stderr)
	case "daemon":
		return cmdDaemon(rest, stdin, stdout, stderr)
	case "service":
		return cmdService(rest, stdout, stderr)
	case "update":
		return cmdUpdate(rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "未知命令 %q\n\n%s", cmd, usage)
		return 2
	}
}

func cmdLink(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "用法: nyatunnel link <nyatunnel://…>")
		return 2
	}
	v, err := deeplink.Parse(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch l := v.(type) {
	case *deeplink.Enroll:
		fmt.Fprintf(stdout, "注册: 服务器 %s，注册码 %s\n", l.Server, l.Code)
	case *deeplink.Open:
		fmt.Fprintf(stdout, "打开: 服务器 %s，隧道 %s\n", l.Server, l.TunnelID)
	}
	return 0
}

func configDir(stderr io.Writer) (string, bool) {
	dir, err := identity.Dir()
	if err != nil {
		fmt.Fprintln(stderr, "无法确定配置目录:", err)
		return "", false
	}
	return dir, true
}

func cmdEnroll(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "设备名（默认使用邀请里的建议名或主机名）")
	yes := fs.Bool("yes", false, "不询问，直接确认")
	// Flags may come after the positional arguments.
	var positional []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return 2
		}
		args = fs.Args()
		if len(args) > 0 {
			positional = append(positional, args[0])
			args = args[1:]
		}
	}

	var server, code string
	switch len(positional) {
	case 1:
		l, err := deeplink.Parse(positional[0])
		e, ok := l.(*deeplink.Enroll)
		if err != nil || !ok {
			fmt.Fprintln(stderr, "不是有效的注册链接:", err)
			return 2
		}
		server, code = "https://"+e.Server, e.Code
	case 2:
		var err error
		if server, err = identity.NormalizeServer(positional[0]); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if code, err = deeplink.NormalizeCode(positional[1]); err != nil {
			fmt.Fprintln(stderr, "注册码格式不正确（应为 XXXX-XXXX）")
			return 2
		}
	default:
		fmt.Fprintln(stderr, "用法: nyatunnel enroll <服务器> <注册码> [--name 设备名] [--yes]")
		return 2
	}

	dir, ok := configDir(stderr)
	if !ok {
		return 1
	}
	if old, err := identity.Load(dir); err == nil {
		fmt.Fprintf(stdout, "本机已注册到 %s（设备 %s）。继续将切换到新的服务器 / 账号。\n", old.Server, old.DeviceID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p, err := enroll.GetPreview(ctx, server, code)
	if err != nil {
		fmt.Fprintln(stderr, "无法获取邀请信息:", err)
		return 1
	}
	host, _ := deeplink.ServerHost(server)
	fmt.Fprintf(stdout, "服务器:   %s（%s）\n", host, p.ServerName)
	fmt.Fprintf(stdout, "归属用户: %s\n", p.Owner)
	if len(p.Tunnels) > 0 {
		fmt.Fprintln(stdout, "将获得的隧道:")
		for _, t := range p.Tunnels {
			fmt.Fprintf(stdout, "  %-20s %s\n", t.Name, t.PublicURL)
		}
	}
	fmt.Fprintln(stdout, "注册后，该服务器的管理员可以通过本机把本机端口暴露到公网。请确认这是你信任的服务器。")

	deviceName := *name
	if deviceName == "" {
		deviceName = p.DeviceNameHint
	}
	if deviceName == "" {
		deviceName, _ = os.Hostname()
	}
	if !*yes {
		fmt.Fprintf(stdout, "以设备名 %q 加入？[y/N] ", deviceName)
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(stdout, "已取消。")
			return 1
		}
	}

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	deviceID, err := enroll.Claim(ctx, server, code, deviceName, version.Version, pub, false)
	if err != nil {
		fmt.Fprintln(stderr, "注册失败:", err)
		return 1
	}
	id := &identity.Identity{Server: server, DeviceID: deviceID, DeviceName: deviceName, PrivateKey: priv}
	if err := identity.SaveUser(dir, id); err != nil {
		fmt.Fprintln(stderr, "注册成功，但保存密钥失败:", err)
		return 1
	}
	fmt.Fprintf(stdout, "注册成功：设备 %s（%s）。运行 `nyatunnel run` 开始工作。\n", deviceID, id.Fingerprint())
	return 0
}

func loadIdentity(stderr io.Writer) (string, *identity.Identity, bool) {
	dir, ok := configDir(stderr)
	if !ok {
		return "", nil, false
	}
	id, err := identity.Load(dir)
	if errors.Is(err, identity.ErrNotEnrolled) {
		fmt.Fprintln(stderr, "本机尚未注册，请先运行 `nyatunnel enroll <服务器> <注册码>`。")
		return "", nil, false
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return "", nil, false
	}
	return dir, id, true
}

func cmdRun(stderr io.Writer) int {
	dir, id, ok := loadIdentity(stderr)
	if !ok {
		return 1
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runAgent(ctx, dir, id, log)
}

// runAgent runs the device until ctx ends; the exit code tells service managers what happened
// (3: revoked, do not restart).
func runAgent(ctx context.Context, dir string, id *identity.Identity, log *slog.Logger) int {
	a := agent.New(agent.Options{Identity: id, Version: version.Version, Log: log, Dir: dir})
	// running.json tells an updater that this version started and connected.
	go update.TrackHealth(ctx, dir, version.Version, a.Watch(), func(s agent.State) bool { return s.Connected })
	go func() {
		for st := range a.Watch() {
			if st.Config == nil {
				continue
			}
			for _, t := range st.Config.Tunnels {
				if msg := st.TunnelErrors[t.ID]; msg != "" {
					log.Warn("tunnel problem", "tunnel", t.Name, "error", msg)
				}
			}
		}
	}()
	err := a.Run(ctx)
	switch {
	case errors.Is(err, agent.ErrRevoked):
		_ = identity.Remove(dir)
		log.Error("服务器拒绝了本设备（已被吊销或所属账号被禁用），已删除本机密钥")
		return 3
	case errors.Is(err, agent.ErrUpgradeRequired):
		log.Error("服务器要求更新客户端版本")
		return 4
	case errors.Is(err, context.Canceled):
		return 0
	}
	log.Error("stopped", "err", err)
	return 1
}

func cmdStatus(stdout, stderr io.Writer) int {
	dir, id, ok := loadIdentity(stderr)
	if !ok {
		return 1
	}
	fmt.Fprintf(stdout, "服务器:   %s\n设备:     %s（%s）\n密钥指纹: %s\n配置目录: %s\n\n", id.Server, id.DeviceName, id.DeviceID, id.Fingerprint(), dir)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg, err := agent.New(agent.Options{Identity: id, Version: version.Version}).Once(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "连接服务器失败:", err)
		return 1
	}
	if len(cfg.Tunnels) == 0 {
		fmt.Fprintln(stdout, "没有分配给本机的隧道。")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "名称\t公网地址\t本地目标\t状态")
	now := time.Now()
	for _, t := range cfg.Tunnels {
		state := "启用"
		switch {
		case !t.Enabled:
			state = "已被管理员停用"
		case t.ExpiresAt != nil && !now.Before(*t.ExpiresAt):
			state = "已过期"
		case t.PausedByClient:
			state = "已暂停"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s:%d\t%s\n", t.Name, t.PublicURL, t.LocalIP, t.LocalPort, state)
	}
	tw.Flush()
	return 0
}

func cmdLogout(stdout, stderr io.Writer) int {
	dir, ok := configDir(stderr)
	if !ok {
		return 1
	}
	if err := identity.Remove(dir); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "已删除本机设备密钥。请让管理员在后台吊销这台设备。")
	return 0
}

// cmdDaemon serves the GUI. The token comes from NYATUNNEL_IPC_TOKEN (the GUI generates it); the
// chosen address is announced as one JSON line on stdout. With --exit-with-stdin the daemon stops
// when its parent closes stdin, so a crashed GUI never leaves it behind.
func cmdDaemon(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "127.0.0.1:0", "loopback address for the GUI API")
	gui := fs.Bool("gui", false, "started by the desktop app")
	exitWithStdin := fs.Bool("exit-with-stdin", false, "exit when stdin is closed")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	token := os.Getenv("NYATUNNEL_IPC_TOKEN")
	if len(token) < 16 {
		fmt.Fprintln(stderr, "NYATUNNEL_IPC_TOKEN must be set (at least 16 characters)")
		return 2
	}
	host, _, err := net.SplitHostPort(*listen)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		fmt.Fprintln(stderr, "--listen must be a loopback address")
		return 2
	}
	dir, ok := configDir(stderr)
	if !ok {
		return 1
	}

	logs := logbuf.New(500)
	log := slog.New(logs.Handler(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *exitWithStdin {
		go func() {
			io.Copy(io.Discard, stdin)
			stop()
		}()
	}
	d := &daemon.Daemon{Dir: dir, Version: version.Version, GUI: *gui, Log: log, Logs: logs}
	if err := d.Start(ctx); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer d.Close()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	srv := &http.Server{Handler: d.Handler(token, stop), ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	json.NewEncoder(stdout).Encode(map[string]string{"event": "ready", "addr": ln.Addr().String(), "version": version.Version})
	log.Info("daemon ready", "addr", ln.Addr().String(), "config", dir)
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
	return 0
}

func cmdUpdate(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "apply" {
		return cmdUpdateApply(args[1:], stderr)
	}
	checkOnly := len(args) > 0 && args[0] == "--check"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	r, err := update.Latest(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "检查更新失败:", err)
		return 1
	}
	current := strings.TrimPrefix(version.Version, "v")
	if !update.Newer(r.Version, current) {
		fmt.Fprintf(stdout, "已是最新版本（%s）。\n", current)
		return 0
	}
	fmt.Fprintf(stdout, "发现新版本 %s（当前 %s）：%s\n", r.Version, current, r.URL)
	if checkOnly {
		return 0
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := update.InstallCLI(ctx, r, exe); err != nil {
		fmt.Fprintln(stderr, "更新失败:", err)
		return 1
	}
	fmt.Fprintln(stdout, "已更新。正在运行的 `nyatunnel run` 或系统服务需要重启后才会使用新版本。")
	return 0
}

// cmdUpdateApply is the unattended updater. The desktop app and the system service start it from
// a copy of this binary outside the directory being replaced; it installs the new version, waits
// until it runs, and restores the old one otherwise.
func cmdUpdateApply(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("update apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", "", "gui or service")
	installer := fs.String("installer", "", "gui: verified installer")
	appDir := fs.String("app-dir", "", "gui: install directory")
	app := fs.String("app", "", "gui: app executable name")
	waitPID := fs.Int("wait-pid", 0, "gui: app process to wait for")
	newExe := fs.String("new", "", "service: verified new executable")
	from := fs.String("from", "", "current version")
	to := fs.String("to", "", "new version")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var stateDir string
	switch *mode {
	case "gui":
		dir, err := identity.Dir()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		stateDir = dir
	case "service":
		stateDir = service.SystemDir()
	default:
		fmt.Fprintln(stderr, "--mode must be gui or service")
		return 2
	}
	logFile, err := openLog(filepath.Join(stateDir, "update.log"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer logFile.Close()
	log := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo})).With("mode", *mode)
	ctx := context.Background()
	var res update.Result
	if *mode == "gui" {
		res = update.GUIUpdate{Installer: *installer, AppDir: *appDir, App: *app, WaitPID: *waitPID, From: *from, To: *to, StateDir: stateDir}.Run(ctx, log)
	} else {
		res = update.ServiceUpdate{New: *newExe, From: *from, To: *to}.Run(ctx, log)
	}
	if !res.OK {
		return 1
	}
	return 0
}

// serviceAutoUpdate checks for a new release a minute after the service starts and then every
// six hours, and hands over to the updater when there is one (see update.StartServiceUpdate).
func serviceAutoUpdate(ctx context.Context, log *slog.Logger) {
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = 6 * time.Hour
		tryServiceUpdate(ctx, log)
	}
}

func tryServiceUpdate(ctx context.Context, log *slog.Logger) {
	if !update.ServiceAutoUpdate() {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	v, err := update.StartServiceUpdate(cctx, false)
	switch {
	case err != nil:
		log.Warn("automatic update", "err", err)
	case v != "":
		log.Info("installing update; the service restarts shortly", "version", v)
	}
}

func cmdService(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "用法: nyatunnel service install | uninstall [--purge] | status | update | auto-update on|off")
		return 2
	}
	switch args[0] {
	case "install":
		dir, id, ok := loadIdentity(stderr)
		if !ok {
			return 1
		}
		exe, err := os.Executable()
		if err == nil {
			exe, err = filepath.Abs(exe)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		// The service gets its own copy of the identity in the system directory; the user's copy
		// is removed so two processes never fight over the same device session.
		if err := service.Install(exe, service.Info{Server: id.Server, DeviceID: id.DeviceID, DeviceName: id.DeviceName}); err != nil {
			fmt.Fprintln(stderr, "安装失败:", err)
			return 1
		}
		if err := identity.Save(service.SystemDir(), id); err != nil {
			fmt.Fprintln(stderr, "保存服务密钥失败:", err)
			_ = service.Uninstall(false)
			return 1
		}
		if err := identity.Remove(dir); err != nil {
			fmt.Fprintln(stderr, "警告：无法删除用户目录中的密钥:", err)
		}
		fmt.Fprintf(stdout, "已安装并启动系统服务 %s（配置目录 %s）。\n", service.Name, service.SystemDir())
		return 0
	case "uninstall":
		purge := len(args) > 1 && args[1] == "--purge"
		if err := service.Uninstall(purge); err != nil {
			fmt.Fprintln(stderr, "卸载失败:", err)
			return 1
		}
		if purge {
			fmt.Fprintln(stdout, "已卸载系统服务并删除其设备密钥。请让管理员在后台吊销这台设备。")
		} else {
			fmt.Fprintf(stdout, "已卸载系统服务。设备密钥仍在 %s，可用 --purge 删除。\n", service.SystemDir())
		}
		return 0
	case "status":
		st, err := service.QueryStatus()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !st.Installed {
			fmt.Fprintln(stdout, "系统服务未安装。")
			return 0
		}
		state := "已停止"
		if st.Running {
			state = "运行中"
		}
		fmt.Fprintf(stdout, "系统服务 %s：%s %s\n", service.Name, state, st.Detail)
		if info, err := service.ReadInfo(); err == nil {
			fmt.Fprintf(stdout, "服务器 %s，设备 %s（%s）\n", info.Server, info.DeviceName, info.DeviceID)
		}
		auto := "开启"
		if !update.ServiceAutoUpdate() {
			auto = "关闭"
		}
		fmt.Fprintf(stdout, "自动更新：%s\n", auto)
		if r, err := update.ReadResult(service.SystemDir()); err == nil {
			if r.OK {
				fmt.Fprintf(stdout, "上次自动更新：%s 从 %s 更新到 %s\n", r.At.Local().Format("2006-01-02 15:04"), r.From, r.To)
			} else {
				after := "未能恢复旧版本，请检查服务"
				if r.RolledBack {
					after = "已恢复为 " + r.From
				}
				fmt.Fprintf(stdout, "上次自动更新失败：%s 更新到 %s 时 %s（%s）\n", r.At.Local().Format("2006-01-02 15:04"), r.To, r.Error, after)
			}
		}
		return 0
	case "update":
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		v, err := update.StartServiceUpdate(ctx, true)
		if err != nil {
			fmt.Fprintln(stderr, "更新失败（需要管理员权限）:", err)
			return 1
		}
		if v == "" {
			fmt.Fprintln(stdout, "系统服务已是最新版本。")
			return 0
		}
		fmt.Fprintf(stdout, "正在把系统服务更新到 %s：服务会重启一次，失败时自动恢复旧版本。结果见 `nyatunnel service status`。\n", v)
		return 0
	case "auto-update":
		if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
			fmt.Fprintln(stderr, "用法: nyatunnel service auto-update on|off")
			return 2
		}
		if err := update.SetServiceAutoUpdate(args[1] == "on"); err != nil {
			fmt.Fprintln(stderr, "设置失败（需要管理员权限）:", err)
			return 1
		}
		if args[1] == "on" {
			fmt.Fprintln(stdout, "已开启：系统服务会自动安装新版本，失败时自动恢复旧版本。")
		} else {
			fmt.Fprintln(stdout, "已关闭系统服务的自动更新。")
		}
		return 0
	case "run":
		// Started by the Windows service manager.
		return runAsService(stderr)
	default:
		fmt.Fprintf(stderr, "未知子命令 %q\n", args[0])
		return 2
	}
}

func runAsService(stderr io.Writer) int {
	dir := service.SystemDir()
	logFile, err := openServiceLog(dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer logFile.Close()
	log := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo}))
	code := 0
	err = service.RunService(func(ctx context.Context) error {
		go serviceAutoUpdate(ctx, log)
		id, err := identity.Load(dir)
		if err != nil {
			log.Error("cannot load identity", "dir", dir, "err", err)
			return err
		}
		code = runAgent(ctx, dir, id, log)
		if code == 4 {
			// The server refuses this version: update now instead of waiting for the next check.
			tryServiceUpdate(ctx, log)
		}
		if code != 0 && code != 3 {
			return fmt.Errorf("agent stopped with code %d", code)
		}
		return nil
	})
	if err != nil {
		log.Error("service", "err", err)
		return 1
	}
	return code
}

// openServiceLog appends to nyatunnel.log in dir, starting over beyond 5 MB.
func openServiceLog(dir string) (*os.File, error) {
	return openLog(filepath.Join(dir, "nyatunnel.log"))
}

// openLog appends to path, starting over beyond 5 MB.
func openLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if st, err := os.Stat(path); err == nil && st.Size() > 5<<20 {
		_ = os.Rename(path, path+".old")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}
