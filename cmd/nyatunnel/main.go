// Command nyatunnel is the NyaTunnel client core and CLI. It enrolls this device with a
// NyaTunnel server and runs the tunnels assigned to it.
package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/stevennight/nyatunnel-common/deeplink"

	"nyatunnel-client/internal/agent"
	"nyatunnel-client/internal/enroll"
	"nyatunnel-client/internal/identity"
	"nyatunnel-client/internal/shared/version"
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
	if err := identity.Save(dir, id); err != nil {
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
	a := agent.New(agent.Options{Identity: id, Version: version.Version, Log: log})
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
