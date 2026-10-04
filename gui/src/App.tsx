import { useCallback, useEffect, useRef, useState } from "react";
import { api, autoUpdateEnabled, RELEASES_URL, type CoreState, type UpdateInfo } from "./api";
import { EnrollDialog, type EnrollRequest } from "./components/EnrollDialog";
import { useNotify } from "./components/ui";
import { useCoreState, useCoreStatus, useDeepLinks, useLogs } from "./hooks";
import { openExternal } from "./open";
import { LogsPage } from "./pages/LogsPage";
import { Onboarding } from "./pages/Onboarding";
import { RequestPage } from "./pages/RequestPage";
import { ServiceScreen } from "./pages/ServiceScreen";
import { SettingsPage } from "./pages/SettingsPage";
import { TunnelDetail } from "./pages/TunnelDetail";
import { TunnelList } from "./pages/TunnelList";
import { errorMessage, hostOf, transportLabel } from "./util";

type Page =
  | { name: "tunnels" }
  | { name: "detail"; id: string }
  | { name: "request" }
  | { name: "logs" }
  | { name: "settings" };

/** An update being installed, or the reason the last attempt failed. */
export interface InstallState {
  version: string;
  error?: string;
}

const UPDATE_INTERVAL = 6 * 60 * 60 * 1000;

export function App() {
  const notify = useNotify();
  const coreStatus = useCoreStatus();
  const running = coreStatus?.state === "running";
  const { state, setState, error: stateError, refresh } = useCoreState(running);
  const { lines, clear } = useLogs(running);
  const [page, setPage] = useState<Page>({ name: "tunnels" });
  const [enroll, setEnroll] = useState<EnrollRequest | null>(null);
  const [update, setUpdate] = useState<UpdateInfo | null>(null);
  const [install, setInstall] = useState<InstallState | null>(null);
  const installing = useRef(false);

  const stateRef = useRef<CoreState | null>(state);
  stateRef.current = state;
  const enrollRef = useRef(enroll);
  enrollRef.current = enroll;

  const handleLink = useCallback(
    async (url: string) => {
      try {
        const l = await api.parseLink(url);
        const st = stateRef.current ?? (await api.state());
        if (l.kind === "enroll" && l.code) {
          if (!st.enrolled && st.service) {
            notify("本机由系统服务运行，已忽略注册链接。", "bad");
            return;
          }
          // Never let a second link replace a dialog the user is looking at.
          if (enrollRef.current) {
            notify("已有一个待确认的注册请求，新的链接已忽略。", "bad");
            return;
          }
          setEnroll({ server: l.server, code: l.code, switchServer: l.switchServer, source: "link" });
          return;
        }
        if (l.kind === "open") {
          // `open` only focuses the window (done by Rust) and selects the tunnel on this server.
          if (!st.enrolled || l.switchServer) {
            notify(`链接指向 ${hostOf(l.server)}，不是本机当前的服务器，已忽略。`);
            return;
          }
          if (l.tunnelId && st.tunnels.some((t) => t.id === l.tunnelId)) {
            setPage({ name: "detail", id: l.tunnelId });
          } else {
            setPage({ name: "tunnels" });
          }
        }
      } catch (e) {
        notify(`无法处理链接：${errorMessage(e)}`, "bad");
      }
    },
    [notify]
  );

  useDeepLinks(running && state !== null, handleLink);

  const installUpdate = useCallback(
    async (version: string) => {
      if (installing.current) return;
      installing.current = true;
      setInstall({ version });
      try {
        const kind = await api.installUpdate();
        // Windows and AppImage exit here and come back as the new version.
        if (kind === "dmg" || kind === "deb") {
          setInstall(null);
          notify("已打开新版本安装包，请按提示完成安装。", "ok");
        }
      } catch (e) {
        setInstall({ version, error: errorMessage(e) });
      } finally {
        installing.current = false;
      }
    },
    [notify]
  );

  // Quiet update checks at start and every 6 hours; failures are ignored here (设置 shows them on
  // demand). A newer release is installed right away unless the user turned that off, the build
  // is a development one, or a system service runs the core (the installer could not replace it).
  const ready = running && state !== null;
  useEffect(() => {
    if (!ready) return;
    const check = () =>
      api.checkUpdate().then((u) => {
        setUpdate(u);
        const dev = import.meta.env.DEV || /dev/.test(u.current);
        if (u.newer && autoUpdateEnabled() && !dev && !stateRef.current?.service?.running) {
          void installUpdate(u.latest);
        }
      }, () => undefined);
    void check();
    const t = setInterval(check, UPDATE_INTERVAL);
    return () => clearInterval(t);
  }, [ready, installUpdate]);

  // Leave pages that no longer apply.
  useEffect(() => {
    if (page.name === "request" && state && !state.canRequest) setPage({ name: "tunnels" });
  }, [page, state]);

  const banners = (
    <>
      {coreStatus?.state === "error" && (
        <div className="banner bad" role="alert">
          ⚠ NyaTunnel 核心异常{coreStatus.error ? `：${coreStatus.error}` : ""}。正在自动重启（第 {coreStatus.restarts} 次）…
        </div>
      )}
      {state && coreStatus && coreStatus.state !== "running" && coreStatus.state !== "error" && (
        <div className="banner warn" role="status">
          NyaTunnel 核心正在重新启动，下面显示的是重启前的状态，暂时无法操作…
        </div>
      )}
      {state && running && stateError && (
        <div className="banner bad" role="alert">
          无法读取核心状态：{stateError}。下面显示的是最后一次读取到的状态。
        </div>
      )}
      {install && !install.error && (
        <div className="banner warn" role="status" data-testid="installing">
          正在下载并安装 NyaTunnel {install.version.replace(/^v/, "")}，完成后会自动重启…
        </div>
      )}
      {install?.error && (
        <div className="banner bad" role="alert">
          <span className="spacer">自动更新失败：{install.error}</span>
          <button className="btn sm" onClick={() => installUpdate(install.version)}>重试</button>
          <button className="btn sm" onClick={() => openExternal(update?.url || RELEASES_URL).catch(() => undefined)}>手动下载</button>
        </div>
      )}
      {state?.notice && (
        <div className="banner warn" role="alert">
          <span className="spacer">{state.notice}</span>
          {/升级|更新/.test(state.notice) && !install && update?.newer && (
            <button className="btn sm" onClick={() => installUpdate(update.latest)}>立即更新</button>
          )}
        </div>
      )}
    </>
  );

  const dialog = enroll && (
    <EnrollDialog
      request={enroll}
      current={state}
      onCancel={() => setEnroll(null)}
      onDone={(st) => {
        setEnroll(null);
        setState(st);
        setPage({ name: "tunnels" });
        notify("注册成功，正在连接服务器…", "ok");
        refresh();
      }}
    />
  );

  if (!state) {
    return (
      <div className="page-wrap">
        {banners}
        <div className="center">
          <div className="empty">
            <div className="big">🐱</div>
            <h3>{coreStatus?.state === "error" ? "核心未能启动" : "正在启动 NyaTunnel…"}</h3>
            <div className="hint">{coreStatus?.state === "error" ? "正在自动重试，原因见上方提示。" : "正在启动本地核心服务"}</div>
          </div>
        </div>
      </div>
    );
  }

  if (!state.enrolled) {
    return (
      <div className="page-wrap">
        {banners}
        {state.service ? <ServiceScreen service={state.service} /> : <Onboarding onEnroll={setEnroll} />}
        {dialog}
      </div>
    );
  }

  const detail = page.name === "detail" ? state.tunnels.find((t) => t.id === page.id) : undefined;
  const nav = (name: Page["name"], label: string) => (
    <button
      className={`it ${page.name === name || (name === "tunnels" && page.name === "detail") ? "on" : ""}`}
      onClick={() => setPage({ name } as Page)}
    >
      {label}
    </button>
  );

  return (
    <div className="page-wrap">
      {banners}
      <div className="shell">
        <nav className="nav">
          <div className="logo"><img src="/logo.svg" alt="" />NyaTunnel</div>
          <div className="nav-items">
            {nav("tunnels", "我的隧道")}
            {state.canRequest && nav("request", "申请隧道")}
            {nav("logs", "日志")}
            {nav("settings", update?.newer ? "设置 · 有新版本" : "设置")}
          </div>
          <div className="who" data-testid="connection">
            {!running
              ? <><span className="dot n" />核心未运行</>
              : state.connected
              ? <><span className="dot ok" />已连接{transportLabel(state.transport) && ` · ${transportLabel(state.transport)}`}</>
              : <><span className="dot warn" />正在连接…</>}
            <br />
            <b>{hostOf(state.server)}</b>
            <br />
            本机：{state.deviceName}
            {!state.connected && state.lastError && <div className="err" style={{ marginTop: 4 }}>{state.lastError}</div>}
          </div>
        </nav>
        <main className={`main${page.name === "logs" ? " fill" : ""}`}>
          {page.name === "tunnels" && (
            <TunnelList
              state={state}
              onDetail={(id) => setPage({ name: "detail", id })}
              onRequest={() => setPage({ name: "request" })}
              onChanged={refresh}
            />
          )}
          {page.name === "detail" && detail && (
            <TunnelDetail
              tunnel={detail}
              error={state.tunnelErrors?.[detail.id]}
              logs={lines}
              onBack={() => setPage({ name: "tunnels" })}
              onChanged={refresh}
            />
          )}
          {page.name === "detail" && !detail && (
            <div className="empty" style={{ maxWidth: "none" }}>
              <h3>该隧道已不再分配给本机</h3>
              <button className="btn" onClick={() => setPage({ name: "tunnels" })}>返回我的隧道</button>
            </div>
          )}
          {page.name === "request" && <RequestPage canRequest={state.canRequest} connected={state.connected} />}
          {page.name === "logs" && <LogsPage lines={lines} onClear={clear} />}
          {page.name === "settings" && (
            <SettingsPage
              state={state}
              coreStatus={coreStatus}
              update={update}
              onUpdate={setUpdate}
              install={install}
              onInstall={installUpdate}
              onLoggedOut={(st) => {
                setState(st);
                setPage({ name: "tunnels" });
              }}
            />
          )}
        </main>
      </div>
      {dialog}
    </div>
  );
}
