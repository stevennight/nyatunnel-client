import { useState } from "react";
import { api, type CoreState, type Tunnel } from "../api";
import { copyText, Switch, useNotify } from "../components/ui";
import { openExternal } from "../open";
import { errorMessage, expiryLabel, isOpenable, tunnelStatus, typeLabel } from "../util";

export function lockText(t: Tunnel): string {
  const { toggle, editLocal } = t.permissions;
  if (!toggle && !editLocal) return "由管理员锁定：不可暂停、不可修改本地地址";
  if (!toggle) return "由管理员锁定：不可暂停";
  if (!editLocal) return "本地地址由管理员设定";
  return "";
}

/** Pause / resume switch; disabled unless the administrator granted `permissions.toggle`. */
export function PauseSwitch(props: { tunnel: Tunnel; onChanged: () => void }) {
  const t = props.tunnel;
  const notify = useNotify();
  const [busy, setBusy] = useState(false);
  const allowed = t.permissions.toggle && t.enabled;
  const title = !t.enabled
    ? "已被管理员停用"
    : t.permissions.toggle
      ? t.pausedByClient ? "启用隧道" : "暂停隧道"
      : "管理员没有允许暂停此隧道";

  async function change(on: boolean) {
    setBusy(true);
    try {
      await api.updateTunnel(t.id, { paused: !on });
      notify(on ? `已启用 ${t.name}` : `已暂停 ${t.name}`, "ok");
      props.onChanged();
    } catch (e) {
      notify(errorMessage(e), "bad");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Switch
      checked={t.enabled && !t.pausedByClient}
      disabled={!allowed || busy}
      label={`${t.name} 启用开关`}
      title={title}
      onChange={change}
    />
  );
}

function TunnelCard(props: {
  tunnel: Tunnel;
  error?: string;
  onDetail: () => void;
  onChanged: () => void;
}) {
  const t = props.tunnel;
  const notify = useNotify();
  const status = tunnelStatus(t, props.error);
  const expiry = expiryLabel(t.expiresAt);
  const lock = lockText(t);
  const policy = [t.display?.accessPolicy, t.display?.limits].filter(Boolean).join(" · ");

  return (
    <div className="tun" data-testid={`tunnel-${t.id}`}>
      <div style={{ minWidth: 0 }}>
        <div className="name">
          {t.name}
          <span className={t.type === "https" ? "tag b" : "tag warn"}>{typeLabel(t.type)}</span>
          {expiry && <span className={status.kind === "expired" ? "tag bad" : "tag warn"}>{expiry}</span>}
          {status.kind === "disabled" && <span className="tag n">{status.label}</span>}
        </div>
        <div className="addr">
          <b className="mono">{t.publicUrl}</b> → <span className="mono">{t.localIp}:{t.localPort}</span>
          {t.permissions.editLocal && <span title="可修改本地目标"> ✎</span>}
        </div>
        {props.error && status.kind !== "disabled" && status.kind !== "paused" && (
          <div className="lockline bad">⚠ {props.error}</div>
        )}
        {(policy || status.kind === "paused") && (
          <div className="lockline">
            {policy && <>🔒 {policy}</>}
            {policy && status.kind === "paused" && " · "}
            {status.kind === "paused" && "已由你暂停"}
          </div>
        )}
        {lock && <div className="lockline" data-testid="lock-note">🔐 {lock}</div>}
      </div>
      <div className="inline">
        <button
          className="btn sm"
          onClick={async () => notify((await copyText(t.publicUrl)) ? "已复制公网地址" : "复制失败", "ok")}
        >
          复制
        </button>
        {isOpenable(t.publicUrl) && (
          <button className="btn sm" onClick={() => openExternal(t.publicUrl).catch((e) => notify(errorMessage(e), "bad"))}>
            打开
          </button>
        )}
        <button className="btn sm" onClick={props.onDetail}>详情</button>
        <PauseSwitch tunnel={t} onChanged={props.onChanged} />
      </div>
    </div>
  );
}

export function TunnelList(props: {
  state: CoreState;
  onDetail: (id: string) => void;
  onRequest: () => void;
  onChanged: () => void;
}) {
  const { state } = props;
  return (
    <>
      <div className="ttl">
        <h2>我的隧道</h2>
        {state.configRev > 0 && <span className="hint">配置版本 {state.configRev} · 由管理员下发</span>}
      </div>
      {!state.connected && state.tunnels.length === 0 && (
        <div className="hint">正在连接服务器，连接后会显示分配给本机的隧道…</div>
      )}
      {state.connected && state.tunnels.length === 0 && (
        <div className="empty" style={{ maxWidth: "none" }}>
          <h3>还没有分配给本机的隧道</h3>
          <div className="hint">管理员在后台分配隧道后，会实时出现在这里。</div>
        </div>
      )}
      {state.tunnels.map((t) => (
        <TunnelCard
          key={t.id}
          tunnel={t}
          error={state.tunnelErrors?.[t.id]}
          onDetail={() => props.onDetail(t.id)}
          onChanged={props.onChanged}
        />
      ))}
      {state.canRequest && (
        <div className="hint" style={{ marginTop: 10 }}>
          需要新的入口？<button className="btn link" onClick={props.onRequest}>向管理员申请</button>
        </div>
      )}
    </>
  );
}
