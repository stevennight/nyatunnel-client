import { useState, type FormEvent } from "react";
import { api, type TunnelRequestBody } from "../api";
import { useNotify } from "../components/ui";
import { errorMessage, isValidPort } from "../util";

const empty: TunnelRequestBody = { type: "https", subdomain: "", localIp: "127.0.0.1", localPort: 0, duration: "24h", reason: "" };

/** Asks the administrator for a new tunnel; the client never creates one itself. */
export function RequestPage(props: { canRequest: boolean; connected: boolean }) {
  const notify = useNotify();
  const [form, setForm] = useState<TunnelRequestBody>(empty);
  const [port, setPort] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [sent, setSent] = useState(0);

  if (!props.canRequest) {
    return (
      <>
        <div className="ttl"><h2>申请新隧道</h2></div>
        <div className="empty" style={{ maxWidth: "none" }}>
          <h3>管理员没有为本设备开放申请</h3>
          <div className="hint">如需新的入口，请直接联系管理员。</div>
        </div>
      </>
    );
  }

  const set = <K extends keyof TunnelRequestBody>(k: K, v: TunnelRequestBody[K]) => setForm((f) => ({ ...f, [k]: v }));

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    const localPort = Number(port);
    if (!form.localIp.trim()) return setError("请填写本地地址");
    if (!port) return setError("请填写本地端口：本机上被转发的服务监听的端口（公网端口由服务器自动分配）");
    if (!isValidPort(localPort)) return setError("本地端口应为 1–65535");
    if (form.type === "https" && form.subdomain && !/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(form.subdomain)) {
      return setError("子域只能包含小写字母、数字和连字符");
    }
    setBusy(true);
    try {
      await api.request({
        ...form,
        subdomain: form.type === "https" ? form.subdomain : "",
        localIp: form.localIp.trim(),
        localPort,
        reason: form.reason.trim()
      });
      setSent((n) => n + 1);
      notify("申请已提交，等待管理员审批", "ok");
      setForm(empty);
      setPort("");
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="form" onSubmit={submit}>
      <div className="ttl"><h2>申请新隧道</h2></div>
      <div className="row">
        <label className="l" htmlFor="rq-type">类型</label>
        <select id="rq-type" className="inp" value={form.type} onChange={(e) => set("type", e.target.value)}>
          <option value="https">HTTPS 网站</option>
          <option value="tcp">TCP</option>
          <option value="udp">UDP</option>
          <option value="tcpudp">TCP+UDP（同一端口）</option>
        </select>
      </div>
      {form.type === "https" && (
        <div className="row">
          <label className="l" htmlFor="rq-sub">期望子域</label>
          <div className="inline">
            <input id="rq-sub" className="inp" value={form.subdomain} placeholder="preview" style={{ width: 160 }}
              onChange={(e) => set("subdomain", e.target.value.toLowerCase())} />
            <span className="hint">管理员可能调整</span>
          </div>
        </div>
      )}
      <div className="row">
        <label className="l" htmlFor="rq-ip">本地地址</label>
        <div className="inline">
          <input id="rq-ip" className="inp mono" value={form.localIp} onChange={(e) => set("localIp", e.target.value)} />
          <span>:</span>
          <input className="inp mono" aria-label="本地端口" inputMode="numeric" placeholder="端口" value={port}
            onChange={(e) => setPort(e.target.value.replace(/[^\d]/g, ""))} style={{ width: 90 }} />
        </div>
      </div>
      <div className="row">
        <label className="l" htmlFor="rq-dur">需要多久</label>
        <select id="rq-dur" className="inp" value={form.duration}
          onChange={(e) => set("duration", e.target.value as TunnelRequestBody["duration"])}>
          <option value="24h">24 小时</option>
          <option value="7d">7 天</option>
          <option value="30d">30 天</option>
          <option value="">长期</option>
        </select>
      </div>
      <div className="row">
        <label className="l" htmlFor="rq-reason">用途说明</label>
        <textarea id="rq-reason" className="inp" rows={3} maxLength={500} value={form.reason}
          onChange={(e) => set("reason", e.target.value)} />
      </div>
      <div className="row">
        <span />
        <div className="inline wrap">
          <button className="btn p" type="submit" disabled={busy || !props.connected}>
            {busy ? "正在提交…" : "提交申请"}
          </button>
          {!props.connected && <span className="hint">未连接到服务器</span>}
          {sent > 0 && <span className="okt">本次已提交 {sent} 个申请，批准后会自动出现在“我的隧道”。</span>}
          {error && <span className="err">{error}</span>}
        </div>
      </div>
    </form>
  );
}
