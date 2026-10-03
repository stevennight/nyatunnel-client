# NyaTunnel Client

NyaTunnel 的客户端：把本机注册到 NyaTunnel 服务器，运行分配给本机的隧道。与服务器只保持一条 WebSocket 连接（经 443），控制消息与所有隧道流量都在其上多路复用。

客户端**不能自行添加隧道**：所有对外入口由管理员在服务端后台定义，配置通过加密的控制通道实时下发；用户只能做管理员授权的操作（暂停 / 启用、修改本地端口、申请新隧道）。

## 仓库关系

NyaTunnel 由三个独立仓库组成，通过服务端仓库的 `docs/协议.md` 对接，协议实现在公共库里：

- `nyatunnel-server`：管理后台、账号、设备会话、隧道入口。设计方案、交互原型、协议都在该仓库的 `docs/` 下
- `nyatunnel-common`：协议实现（`tunnelproto`）与深链（`deeplink`）
- `nyatunnel-client`（本仓库）

## 组成

| 部分 | 说明 | 状态 |
|---|---|---|
| `cmd/nyatunnel` | 核心 + CLI：`enroll`、`run`、`status`、`service …`；服务器 / NAS 上只需要它 | 骨架（`version`、`link`） |
| `internal/deeplink` | `nyatunnel://` 深链的严格解析与校验 | ✅ |
| `gui/` | Tauri 2 + React 桌面界面：托盘、深链、单实例、自动更新；Go 核心作为 sidecar 打包，GUI 通过本地 IPC 控制它 | M2 |

## 当前状态

**M0 骨架**：CLI 骨架、深链解析、CI（三平台测试 + 六个目标的交叉编译）、Release（CLI 压缩包 + `SHA256SUMS`）。

## 开发

需要 Go（版本见 `go.mod`）。

```powershell
go test ./...
go vet ./...
go run ./cmd/nyatunnel version
go run ./cmd/nyatunnel link "nyatunnel://enroll?v=1&s=tunnel.example.net&c=K7QP-3XMD"
```

## 发布

1. 修改 `VERSION`，提交；
2. 推送同名标签：`git tag v0.1.0 && git push origin v0.1.0`；
3. `release.yml` 构建 windows / linux / darwin × amd64 / arm64 的 CLI（`NyaTunnel-CLI_<版本>_<系统>_<架构>.zip|tar.gz`）并附 `SHA256SUMS` 发布到 GitHub Release。带 `-beta.N` 后缀的标签发布为预发布。

GUI 安装包（Windows NSIS、macOS dmg、Linux AppImage / deb）将在 M2–M3 加入同一个工作流。
