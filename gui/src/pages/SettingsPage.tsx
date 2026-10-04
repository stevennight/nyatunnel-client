import { useEffect, useState } from "react";
import { getVersion } from "@tauri-apps/api/app";
import { api, autoUpdateEnabled, RELEASES_URL, setAutoUpdateEnabled, type CoreState, type CoreStatus, type UpdateInfo } from "../api";
import type { InstallState } from "../App";
import { ConfirmDialog, copyText, useNotify } from "../components/ui";
import { openExternal } from "../open";
import { errorMessage, formatTime, hostOf, transportLabel } from "../util";

export function SettingsPage(props: {
  state: CoreState;
  coreStatus: CoreStatus | null;
  update: UpdateInfo | null;
  onUpdate: (u: UpdateInfo) => void;
  install: InstallState | null;
  onInstall: (version: string) => void;
  onLoggedOut: (st: CoreState) => void;
}) {
  const { state } = props;
  const notify = useNotify();
  const [guiVersion, setGuiVersion] = useState("");
  const [autostart, setAutostart] = useState<boolean | null>(null);
  const [confirmLogout, setConfirmLogout] = useState(false);
  const [busy, setBusy] = useState(false);
  const [checking, setChecking] = useState(false);
  const [updateMsg, setUpdateMsg] = useState("");
  const [autoUpdate, setAutoUpdate] = useState(autoUpdateEnabled);

  useEffect(() => {
    getVersion().then(setGuiVersion, () => setGuiVersion(""));
    api.autostartEnabled().then(setAutostart, () => setAutostart(null));
  }, []);

  async function toggleAutostart(on: boolean) {
    try {
      setAutostart(await api.setAutostart(on));
    } catch (e) {
      notify(`无法修改开机自启：${errorMessage(e)}`, "bad");
    }
  }

  async function checkUpdate() {
    setChecking(true);
    setUpdateMsg("");
    try {
      const u = await api.checkUpdate(true);
      props.onUpdate(u);
      if (!u.newer) setUpdateMsg("已是最新版本");
    } catch (e) {
      setUpdateMsg(errorMessage(e));
    } finally {
      setChecking(false);
    }
  }

  async function logout() {
    setBusy(true);
    try {
      const st = await api.logout();
      setConfirmLogout(false);
      notify("已删除本机设备密钥", "ok");
      props.onLoggedOut(st);
    } catch (e) {
      notify(errorMessage(e), "bad");
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <div className="ttl"><h2>设置</h2></div>
      <div className="fs">
        <h4>服务器</h4>
        <div className="kv">
          <span>地址</span><span className="mono">{state.server}</span>
          <span>连接</span>
          <span>
            {state.connected
              ? <><span className="dot ok" />已连接{transportLabel(state.transport) && `（${transportLabel(state.transport)}）`}{state.connectedAt && <span className="hint">（{formatTime(state.connectedAt)} 起）</span>}</>
              : <><span className="dot bad" />未连接{state.lastError && <span className="hint">：{state.lastError}</span>}</>}
          </span>
          <span>设备名</span><span>{state.deviceName}</span>
          <span>设备 ID</span><span className="mono">{state.deviceId}</span>
          <span>密钥指纹</span>
          <span className="mono">
            {state.fingerprint}{" "}
            {state.fingerprint && (
              <button className="btn link" onClick={async () => notify((await copyText(state.fingerprint ?? "")) ? "已复制指纹" : "复制失败")}>复制</button>
            )}
          </span>
          <span>配置目录</span><span className="mono">{state.configDir}</span>
        </div>
        <div className="hint" style={{ marginBottom: 8 }}>
          管理员可以在后台核对设备的密钥指纹。退出后服务器上的设备记录仍在，请让管理员在后台吊销 {hostOf(state.server)} 上的这台设备。
        </div>
        <div className="inline" style={{ marginBottom: 10 }}>
          <button className="btn d" onClick={() => setConfirmLogout(true)}>退出并删除本机密钥</button>
        </div>
      </div>
      <div className="fs">
        <h4>常规</h4>
        <label className="chk">
          <input type="checkbox" checked={autostart ?? false} disabled={autostart === null}
            onChange={(e) => toggleAutostart(e.target.checked)} />
          开机自动启动（启动后最小化到托盘）
        </label>
        <div className="hint" style={{ marginBottom: 10 }}>关闭窗口时 NyaTunnel 会继续在托盘中运行；从托盘菜单选择“退出”才会断开所有隧道。</div>
        <label className="chk">
          <input type="checkbox" checked={autoUpdate}
            onChange={(e) => {
              setAutoUpdateEnabled(e.target.checked);
              setAutoUpdate(e.target.checked);
            }} />
          自动下载并安装新版本
        </label>
        <div className="hint" style={{ marginBottom: 10 }}>
          每 6 小时检查一次。安装包会先核对发布页的 SHA256 校验和；Windows 上直接覆盖安装并重启，隧道会短暂断开。
        </div>
      </div>
      <div className="fs">
        <h4>关于</h4>
        <div className="kv">
          <span>界面版本</span><span className="mono">{guiVersion || "—"}</span>
          <span>核心版本</span><span className="mono">{state.version || props.coreStatus?.version || "—"}</span>
          <span>连接方式</span><span>WebSocket · yamux 多路复用{transportLabel(state.transport) && ` · ${transportLabel(state.transport)}`}</span>
        </div>
        {props.update?.newer && (
          <div className="infobox inline wrap" data-testid="update-available">
            <span className="spacer">发现新版本 <b>{props.update.latest.replace(/^v/, "")}</b>（当前 {props.update.current.replace(/^v/, "")}）</span>
            <button className="btn p sm" disabled={!!props.install && !props.install.error}
              onClick={() => props.onInstall(props.update?.latest ?? "")}>
              {props.install && !props.install.error ? "正在更新…" : "立即更新"}
            </button>
            <button className="btn sm" onClick={() => openExternal(props.update?.url || RELEASES_URL).catch((e) => notify(errorMessage(e), "bad"))}>
              发布页
            </button>
          </div>
        )}
        <div className="inline wrap" style={{ marginBottom: 10 }}>
          <button className="btn sm" onClick={checkUpdate} disabled={checking}>{checking ? "正在检查…" : "检查更新"}</button>
          {updateMsg && <span className="hint">{updateMsg}</span>}
          <span className="spacer" />
          <button className="btn sm" onClick={() => api.quit()}>退出 NyaTunnel</button>
        </div>
      </div>
      {confirmLogout && (
        <ConfirmDialog
          title="退出并删除本机密钥？"
          confirmLabel={busy ? "正在删除…" : "删除密钥"}
          danger
          busy={busy}
          onConfirm={logout}
          onCancel={() => setConfirmLogout(false)}
        >
          <div className="badbox">
            本机的设备密钥将被删除，所有隧道立即停止。之后需要新的注册码才能重新加入 {hostOf(state.server)}。
          </div>
          <div className="hint">服务器上的设备记录不会自动删除，请让管理员在后台吊销这台设备。</div>
        </ConfirmDialog>
      )}
    </>
  );
}
