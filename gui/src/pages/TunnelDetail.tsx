import { useEffect, useState, type FormEvent } from "react";
import { api, type LogLine, type Tunnel } from "../api";
import { copyText, useNotify } from "../components/ui";
import { LogView } from "../components/LogView";
import { openExternal } from "../open";
import { errorMessage, expiryLabel, formatTime, isLoopback, isOpenable, isValidPort, tunnelStatus, typeLabel } from "../util";
import { lockText, PauseSwitch } from "./TunnelList";

function LocalTargetForm(props: { tunnel: Tunnel; onChanged: () => void }) {
  const t = props.tunnel;
  const notify = useNotify();
  const [ip, setIp] = useState(t.localIp);
  const [port, setPort] = useState(String(t.localPort));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  // Follow server-side changes while the form is untouched.
  useEffect(() => {
    setIp(t.localIp);
    setPort(String(t.localPort));
  }, [t.localIp, t.localPort]);

  const portNum = Number(port);
  const ipTrim = ip.trim();
  const ipError = !ipTrim
    ? "请填写本地地址"
    : t.permissions.loopbackOnly && !isLoopback(ipTrim)
      ? "该隧道只允许本机回环地址"
      : "";
  const portError = isValidPort(portNum) ? "" : "端口应为 1–65535";
  const dirty = ipTrim !== t.localIp || portNum !== t.localPort;

  async function save(e: FormEvent) {
    e.preventDefault();
    if (ipError || portError || !dirty) return;
    setBusy(true);
    setError("");
    try {
      const change: { localIp?: string; localPort?: number } = {};
      if (ipTrim !== t.localIp) change.localIp = ipTrim;
      if (portNum !== t.localPort) change.localPort = portNum;
      await api.updateTunnel(t.id, change);
      notify("本地目标已更新", "ok");
      props.onChanged();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="card form" onSubmit={save} data-testid="local-edit">
      <div className="k" style={{ marginBottom: 6 }}>本地目标</div>
      <div className="inline">
        <input
          className="inp mono"
          aria-label="本地地址"
          value={ip}
          aria-invalid={Boolean(ipError)}
          onChange={(e) => setIp(e.target.value)}
        />
        <span>:</span>
        <input
          className="inp mono"
          aria-label="本地端口"
          inputMode="numeric"
          value={port}
          aria-invalid={Boolean(portError)}
          onChange={(e) => setPort(e.target.value.replace(/[^\d]/g, ""))}
          style={{ width: 90 }}
        />
      </div>
      {t.permissions.loopbackOnly && (
        <div className="hint" style={{ marginTop: 6 }}>该隧道只允许本机回环地址（127.0.0.1、::1、localhost）。</div>
      )}
      {(ipError || portError) && <div className="err" style={{ marginTop: 6 }}>{ipError || portError}</div>}
      {error && <div className="err" style={{ marginTop: 6 }}>{error}</div>}
      <div className="inline" style={{ marginTop: 10 }}>
        <button className="btn p sm" type="submit" disabled={busy || !dirty || Boolean(ipError || portError)}>
          {busy ? "正在保存…" : "保存"}
        </button>
        {dirty && (
          <button type="button" className="btn sm" onClick={() => { setIp(t.localIp); setPort(String(t.localPort)); }}>
            还原
          </button>
        )}
      </div>
      <div className="hint" style={{ marginTop: 8 }}>修改会上报服务器并记录审计日志。</div>
    </form>
  );
}

export function TunnelDetail(props: {
  tunnel: Tunnel;
  error?: string;
  logs: LogLine[];
  onBack: () => void;
  onChanged: () => void;
}) {
  const t = props.tunnel;
  const notify = useNotify();
  const status = tunnelStatus(t, props.error);
  const expiry = expiryLabel(t.expiresAt);
  const lock = lockText(t);
  const related = props.logs.filter((l) => l.text.includes(t.name) || l.text.includes(t.id)).slice(-200);

  return (
    <>
      <div className="ttl">
        <h2>
          <button className="btn link" onClick={props.onBack} aria-label="返回" style={{ fontSize: 16 }}>←</button>{" "}
          {t.name} <span className={t.type === "https" ? "tag b" : "tag warn"}>{typeLabel(t.type)}</span>
        </h2>
        <div className="inline">
          <span className="hint">{status.kind === "ok" ? "运行中" : status.kind === "error" ? "异常" : status.label}</span>
          <PauseSwitch tunnel={t} onChanged={props.onChanged} />
        </div>
      </div>
      {props.error && <div className="badbox">⚠ {props.error}</div>}
      <div className="grid2">
        <div className="card">
          <div className="k">公网入口（只读）</div>
          <div className="mono selectable" style={{ margin: "4px 0 6px" }}><b>{t.publicUrl}</b></div>
          <div className="inline" style={{ marginBottom: 10 }}>
            <button className="btn sm" onClick={async () => notify((await copyText(t.publicUrl)) ? "已复制公网地址" : "复制失败")}>复制</button>
            {isOpenable(t.publicUrl) && (
              <button className="btn sm" onClick={() => openExternal(t.publicUrl).catch((e) => notify(errorMessage(e), "bad"))}>在浏览器中打开</button>
            )}
          </div>
          <div className="k">访问策略（只读）</div>
          <div style={{ margin: "4px 0 10px" }}>{t.display?.accessPolicy || "无"}</div>
          <div className="k">限制（只读）</div>
          <div style={{ margin: "4px 0 10px" }}>{t.display?.limits || "无"}</div>
          <div className="k">到期</div>
          <div style={{ marginTop: 4 }}>{t.expiresAt ? `${formatTime(t.expiresAt)}（${expiry}）` : "长期"}</div>
        </div>
        {t.permissions.editLocal ? (
          <LocalTargetForm tunnel={t} onChanged={props.onChanged} />
        ) : (
          <div className="card" data-testid="local-readonly">
            <div className="k" style={{ marginBottom: 6 }}>本地目标（只读）</div>
            <div className="mono selectable"><b>{t.localIp}:{t.localPort}</b></div>
            <div className="hint" style={{ marginTop: 8 }}>管理员没有允许本机修改本地目标。</div>
          </div>
        )}
      </div>
      {lock && <div className="lockline" style={{ marginTop: 10 }}>🔐 {lock}</div>}
      <div className="k" style={{ margin: "14px 0 6px" }}>相关日志</div>
      <LogView lines={related} className="small" empty="暂无与此隧道相关的日志" />
    </>
  );
}
