import { useState } from "react";
import { api, type CoreState, type Tunnel } from "../api";
import { ConfirmDialog, copyText, Switch, useNotify } from "../components/ui";
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

/**
 * Confirms a tunnel on this device (协议 §4.6). The dialog spells out what becomes reachable: the
 * point is that the user, not the server, agrees to expose this local target.
 */
export function ConfirmTunnelButton(props: { tunnel: Tunnel; onChanged: () => void; className?: string }) {
  const t = props.tunnel;
  const notify = useNotify();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const target = `${t.localIp}:${t.localPort}`;

  async function confirm() {
    setBusy(true);
    try {
      await api.confirmTunnel(t.id);
      notify(`已确认 ${t.name}`, "ok");
      setOpen(false);
      props.onChanged();
    } catch (e) {
      notify(errorMessage(e), "bad");
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <button className={props.className ?? "btn p sm"} onClick={() => setOpen(true)}>确认接通…</button>
      {open && (
        <ConfirmDialog title={`确认接通 ${t.name}`} confirmLabel="确认接通" busy={busy} onConfirm={confirm} onCancel={() => setOpen(false)}>
          <div className="kv" style={{ margin: "4px 0 10px" }}>
            <span>类型</span><span>{typeLabel(t.type)}</span>
            <span>公网入口</span><span className="mono selectable">{t.publicUrl}</span>
            <span>本地目标</span><span className="mono selectable"><b>{target}</b></span>
            <span>访问策略</span><span>{t.display?.accessPolicy || "无"}</span>
          </div>
          <div className="warnbox">
            确认后，能访问上面公网入口的人（受访问策略限制）就能连到本机可以访问的 <b className="mono">{target}</b>。
            只确认你认识、并且确实要公开的服务。管理员以后修改类型或本地目标时，需要你重新确认。
          </div>
        </ConfirmDialog>
      )}
    </>
  );
}

function TunnelCard(props: {
  tunnel: Tunnel;
  error?: string;
  confirmed: boolean;
  onDetail: () => void;
  onChanged: () => void;
}) {
  const t = props.tunnel;
  const notify = useNotify();
  const status = tunnelStatus(t, props.error, props.confirmed);
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
          {!props.confirmed && <span className="tag warn">待确认</span>}
        </div>
        <div className="addr">
          <b className="mono">{t.publicUrl}</b> → <span className="mono">{t.localIp}:{t.localPort}</span>
          {t.permissions.editLocal && <span title="可修改本地目标"> ✎</span>}
        </div>
        {!props.confirmed && (
          <div className="lockline" data-testid="unconfirmed-note">⚠ 需要你在本机确认后才会接通</div>
        )}
        {props.error && status.kind !== "disabled" && status.kind !== "paused" && status.kind !== "unconfirmed" && (
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
        {!props.confirmed && <ConfirmTunnelButton tunnel={t} onChanged={props.onChanged} />}
        <button
          className="btn sm"
          onClick={async () => {
            const ok = await copyText(t.publicUrl);
            notify(ok ? "已复制公网地址" : "复制失败，请在详情页手动选中复制", ok ? "ok" : "bad");
          }}
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
  const pending = state.tunnels.filter((t) => !state.confirmed?.[t.id]).length;
  return (
    <>
      <div className="ttl">
        <h2>我的隧道</h2>
        {state.configRev > 0 && <span className="hint">配置版本 {state.configRev} · 由管理员下发</span>}
      </div>
      {pending > 0 && (
        <div className="warnbox" data-testid="pending-banner">
          有 {pending} 条隧道等待你确认。管理员下发或修改的隧道要在本机确认后才会接通，请核对本地目标后再确认。
        </div>
      )}
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
          confirmed={Boolean(state.confirmed?.[t.id])}
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
