import { useCallback, useEffect, useRef, useState } from "react";
import { api, RELEASES_URL, type CoreState, type UpdateInfo } from "./api";
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

export function App() {
  const notify = useNotify();
  const coreStatus = useCoreStatus();
  const running = coreStatus?.state === "running";
  const { state, setState, refresh } = useCoreState(running);
  const { lines, clear } = useLogs(running);
  const [page, setPage] = useState<Page>({ name: "tunnels" });
  const [enroll, setEnroll] = useState<EnrollRequest | null>(null);
  const [update, setUpdate] = useState<UpdateInfo | null>(null);
  const updateChecked = useRef(false);

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

  // One quiet update check per start; failures are ignored here (设置 shows them on demand).
  useEffect(() => {
    if (!running || !state || updateChecked.current) return;
    updateChecked.current = true;
    api.checkUpdate().then(setUpdate, () => undefined);
  }, [running, state]);

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
      {state?.notice && (
        <div className="banner warn" role="alert">
          <span className="spacer">{state.notice}</span>
          {/升级|更新/.test(state.notice) && (
            <button className="btn sm" onClick={() => openExternal(RELEASES_URL).catch(() => undefined)}>下载新版本</button>
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
            <div className="hint">{coreStatus?.state === "error" ? coreStatus.error : "正在启动本地核心服务"}</div>
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
          <div className="logo"><i />NyaTunnel</div>
          {nav("tunnels", "我的隧道")}
          {state.canRequest && nav("request", "申请隧道")}
          {nav("logs", "日志")}
          {nav("settings", update?.newer ? "设置 · 有新版本" : "设置")}
          <div className="who" data-testid="connection">
            {state.connected
              ? <><span className="dot ok" />已连接{transportLabel(state.transport) && ` · ${transportLabel(state.transport)}`}</>
              : <><span className="dot warn" />正在连接…</>}
            <br />
            <b>{hostOf(state.server)}</b>
            <br />
            本机：{state.deviceName}
            {!state.connected && state.lastError && <div className="err" style={{ marginTop: 4 }}>{state.lastError}</div>}
          </div>
        </nav>
        <main className="main">
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
