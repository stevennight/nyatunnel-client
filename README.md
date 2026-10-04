# NyaTunnel Client

NyaTunnel 的客户端：把本机注册到 NyaTunnel 服务器，运行分配给本机的隧道。与服务器只保持一条 WebSocket 连接（经 443），控制消息与所有隧道流量都在其上多路复用。

客户端**不能自行添加隧道**：所有对外入口由管理员在服务端后台定义，配置通过加密的控制通道实时下发；用户只能做管理员授权的操作（暂停 / 启用、修改本地端口、申请新隧道）。

## 仓库关系

NyaTunnel 由三个独立仓库组成，通过服务端仓库的 `docs/协议.md` 对接，协议实现在公共库里：

- `nyatunnel-server`：管理后台、账号、设备会话、隧道入口。设计方案、交互原型、协议都在该仓库的 `docs/` 下
- `nyatunnel-common`：协议实现（`tunnelproto`）与深链（`deeplink`）
- `nyatunnel-client`（本仓库）

## 组成

| 部分 | 说明 |
|---|---|
| `cmd/nyatunnel` | 核心 + CLI；服务器 / NAS / Docker 上只需要它 |
| `internal/agent` | 设备会话：握手、配置快照、转发 TCP / HTTPS / UDP 流、断线重连、直连优先 |
| `internal/daemon` | 供 GUI 使用的本机回环接口 |
| `internal/service` | 系统服务（Windows 服务 / systemd / launchd） |
| `internal/identity` | 设备身份；私钥优先存系统钥匙串 |
| `internal/update` | 自更新：CLI 与桌面安装包，都先核对 Release 的 `SHA256SUMS` |
| `gui/` | Tauri 2 + React 桌面程序，Go 核心作为 sidecar 打包 |

## 命令行

```text
nyatunnel enroll <服务器> <注册码> [--name 设备名] [--yes]   # 或 nyatunnel enroll "nyatunnel://enroll?…"
nyatunnel run                     # 前台运行（Ctrl+C 退出）
nyatunnel status                  # 注册信息与隧道（只读查询，不影响正在运行的会话）
nyatunnel service install         # 安装为系统服务，开机即运行（需要管理员 / root）
nyatunnel service uninstall [--purge]
nyatunnel service status          # 含自动更新开关与上次自动更新的结果
nyatunnel service auto-update on|off   # Windows 系统服务是否自动安装新版本（默认开启）
nyatunnel update [--check]        # 从 GitHub Releases 更新
nyatunnel logout                  # 删除本机设备密钥
```

配置目录默认为系统的用户配置目录下的 `NyaTunnel`，可用 `NYATUNNEL_HOME` 指定。安装为系统服务后，设备身份移到 `%ProgramData%\NyaTunnel`（Windows）、`/etc/nyatunnel`（Linux）或 `/Library/Application Support/NyaTunnel`（macOS），仅管理员 / root 可读。Windows 上服务运行的是复制到 `%ProgramData%\NyaTunnel\bin` 的程序（同样只有管理员可改），与桌面版的安装目录互不影响。

Windows 系统服务会自己更新：启动 1 分钟后和之后每 6 小时检查一次（服务器要求升级时立即检查），下载 CLI 并核对 `SHA256SUMS`，再交给一个从副本运行的更新程序：停止服务、替换程序、启动服务，等新版本报告“已启动”（之前在线的还要“已重新连上服务器”）。两分钟内没有做到就换回旧程序并重新启动服务。全程以 SYSTEM 运行，不需要有人登录，也不会弹出 UAC。日志在 `%ProgramData%\NyaTunnel\update.log`，失败的版本一天内不再自动重试。

## Docker（服务器 / NAS）

镜像 `ghcr.io/stevennight/nyatunnel`（linux/amd64、arm64、arm/v7）。使用宿主机网络，隧道的本地目标 `127.0.0.1:8080` 就是宿主机上的服务：

```bash
docker run --rm -it -v "$PWD/data:/data" ghcr.io/stevennight/nyatunnel enroll https://tunnel.example.com XXXX-XXXX
docker run -d --name nyatunnel --restart unless-stopped --network host -v "$PWD/data:/data" ghcr.io/stevennight/nyatunnel
```

或使用 [`deploy/docker/docker-compose.yml`](deploy/docker/docker-compose.yml)。

## 开发

需要 Go（版本见 `go.mod`）。本地联调公共库时使用工作区根目录的 `go.work`。

```powershell
go test ./...
go vet ./...
go run ./cmd/nyatunnel version
go run ./cmd/nyatunnel link "nyatunnel://enroll?v=1&s=tunnel.example.net&c=K7QP-3XMD"
```

## 图形界面（GUI）

`gui/` 是 Tauri 2 + React + Vite + TypeScript 的桌面程序。它只是控制器：真正的客户端是随安装包分发的 Go 核心（Tauri sidecar `binaries/nyatunnel-<目标三元组>`）。

- 启动时以 `nyatunnel daemon --gui --exit-with-stdin` 拉起核心，环境变量 `NYATUNNEL_IPC_TOKEN` 为每次随机生成的令牌；核心在 stdout 打印 `{"event":"ready","addr":…}` 后，界面的所有请求都经 Rust 命令 `core` 转发并附上令牌——令牌不进入网页。核心异常退出时自动退避重启，界面顶部显示提示。
- 关闭窗口最小化到托盘（菜单：显示窗口 / 退出），退出时先请求核心正常关闭。
- 深链 `nyatunnel://enroll?…` 只会打开确认对话框：先向服务器取回预览（服务器、邀请人、将获得的隧道），用户点“确认加入”后才生成密钥并注册；首次连接的服务器和会切换服务器的链接都有醒目提示。`nyatunnel://open?…` 只聚焦窗口并定位隧道。第二个实例（Windows / Linux 点击链接时）会把链接交给已运行的实例。
- 页面：首次启动、我的隧道、隧道详情、申请隧道、日志、设置；核心以系统服务运行时显示“本机由系统服务运行”。
- 自动更新（设置里可关闭，默认开启）：启动时和每 6 小时检查一次 GitHub Releases，发现新版本就由核心下载本平台的安装包并核对 `SHA256SUMS`（`POST /v1/update/download`，只写入系统临时目录下的 `NyaTunnel-update`），再由 Rust 命令 `install_update` 校验路径后安装：
  - Windows：全程无人值守，界面只负责启动更新程序后退出。更新程序是捆绑的 `nyatunnel.exe` 的副本，从安装目录以外运行（`nyatunnel update apply --mode gui`），步骤如下：
    1. 等界面退出，并结束安装目录里仍在运行的进程。
    2. 备份安装目录。
    3. 以 `/S /UPDATE` 静默运行 NSIS 安装包（当前用户安装，无 UAC），**不会询问是否先卸载**（见下方自定义模板）。
    4. 核对安装后的版本，以 `--autostart` 启动新版本（回到托盘）。
    5. 等核心在配置目录的 `running.json` 里报告新版本已启动；之前在线的，还要等它重新连上服务器。
    6. 任何一步失败，或两分钟内没有做到，就恢复备份并启动旧版本，保证隧道不会因为更新而一直断开。

    结果写入配置目录的 `update-result.json`（界面会提示成功，或提示失败并说明已恢复），日志写入 `update.log`。失败的版本一天内不再自动重试。
  - Linux AppImage：替换 `$APPIMAGE` 并启动新版本；deb 与 macOS dmg 打开安装包，由用户完成。
  - 开发构建不自动安装。系统服务仍在运行桌面版目录里的 `nyatunnel.exe`（较早的安装方式）时，界面也不自动安装，等服务自动更新、把程序迁到自己的目录之后再更新（设置里仍可手动“立即更新”）。

开发需要 Node 24、Rust stable、Go，以及各平台的 [Tauri 依赖](https://v2.tauri.app/start/prerequisites/)（Windows：VS Build Tools + WebView2；Linux：`libwebkit2gtk-4.1-dev` 等，见 `.github/workflows/ci.yml`）。

```powershell
node scripts/build-sidecar.mjs   # 为本机目标构建核心 → gui/src-tauri/binaries/（每次改了 Go 代码都要重跑）
cd gui
npm ci
npm test                         # vitest：深链确认流程、权限相关的开关与编辑
npm run build                    # tsc --noEmit + vite build
npm run dev                      # tauri dev，使用独立的 .dev 标识（Vite 开发服务器 127.0.0.1:1420）
```

本地打包（Windows 示例，产物在 `gui/src-tauri/target/release/bundle/nsis/`）：

```powershell
node scripts/build-sidecar.mjs
node scripts/tauri-build.mjs --bundles nsis
```

Windows 安装包使用自定义 NSIS 模板 `gui/src-tauri/windows/installer.nsi`：复制自 Tauri 自带模板（tauri-cli v2.12.1），只改了两处（标有 `NyaTunnel:`）——已安装时不显示“先卸载旧版本？”页面而是直接覆盖安装，以及覆盖前关闭正在运行的核心 `nyatunnel.exe`。**升级 `@tauri-apps/cli` 时要重新复制上游模板并重做这两处修改。**

`scripts/build-sidecar.mjs` 接受 `--target <三元组>`（或环境变量 `TAURI_TARGET_TRIPLE`），版本取 `NYATUNNEL_VERSION` 或 `VERSION`，与 CLI 发布使用相同的 `-ldflags`。`scripts/tauri-build.mjs` 在设置 `NYATUNNEL_VERSION` 时通过临时配置注入版本，其余参数原样传给 `tauri build`。`gui/package.json` 的 `version` 必须与 `VERSION` 一致（CI 会检查）。

## 发布

1. 修改 `VERSION`，提交；
2. 推送同名标签：`git tag v0.1.0 && git push origin v0.1.0`；
3. `release.yml` 构建 windows / linux / darwin × amd64 / arm64 的 CLI（`NyaTunnel-CLI_<版本>_<系统>_<架构>.zip|tar.gz`）和 GUI 安装包（`NyaTunnel_<版本>_windows_x64.exe`（NSIS）、`NyaTunnel_<版本>_macos_arm64.dmg` / `_macos_x64.dmg`、`NyaTunnel_<版本>_linux_x64.AppImage` / `.deb`），汇总 `SHA256SUMS` 后发布到 GitHub Release，并推送 Docker 镜像 `ghcr.io/stevennight/nyatunnel`。带 `-beta.N` 后缀的标签发布为预发布。

手动运行 Release 工作流（Actions 页面的 “Run workflow”）会构建全部产物但不发布，用于发版前演练。

安装包暂未签名：Windows 首次运行可能出现 SmartScreen 提示，macOS 需要在“隐私与安全性”中允许打开。
