import { useState, type FormEvent } from "react";
import { api } from "../api";
import type { EnrollRequest } from "../components/EnrollDialog";
import { errorMessage } from "../util";

/** First start: the only way in is registering with a server (no "add proxy" button). */
export function Onboarding(props: { onEnroll: (r: EnrollRequest) => void }) {
  const [server, setServer] = useState("");
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    const s = server.trim();
    const c = code.trim();
    const link = [s, c].find((v) => v.toLowerCase().startsWith("nyatunnel://"));
    if (link) {
      setBusy(true);
      try {
        const l = await api.parseLink(link);
        if (l.kind !== "enroll" || !l.code) {
          setError("这不是注册链接。请粘贴管理员发给你的邀请链接，或填写服务器地址和注册码。");
          return;
        }
        props.onEnroll({ server: l.server, code: l.code, switchServer: l.switchServer, source: "form" });
      } catch (err) {
        setError(errorMessage(err));
      } finally {
        setBusy(false);
      }
      return;
    }
    if (!s) return setError("请填写服务器地址");
    if (!c) return setError("请填写注册码");
    props.onEnroll({ server: s, code: c, source: "form" });
  }

  return (
    <div className="center">
      <div className="empty">
        <div className="big">🐱</div>
        <h3>还没有连接到服务器</h3>
        <div className="hint">隧道由管理员在后台分配。请向管理员索取邀请链接或注册码。</div>
        <div className="steps">
          <div><b>① 点击邀请链接</b>浏览器会自动唤起本客户端</div>
          <div><b>② 或粘贴注册码</b>服务器地址 + 8 位注册码，也可直接粘贴邀请链接</div>
          <div><b>③ 确认服务器</b>核对域名后完成注册</div>
        </div>
        <form className="inline wrap" style={{ justifyContent: "center" }} onSubmit={submit}>
          <input
            className="inp"
            aria-label="服务器地址"
            placeholder="https://tunnel.example.com 或 nyatunnel://…"
            value={server}
            onChange={(e) => setServer(e.target.value)}
            style={{ width: 280 }}
            autoFocus
          />
          <input
            className="inp mono"
            aria-label="注册码"
            placeholder="XXXX-XXXX"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            style={{ width: 130 }}
          />
          <button className="btn p" type="submit" disabled={busy}>注册</button>
        </form>
        {error && <div className="err" style={{ marginTop: 10 }}>{error}</div>}
        <div className="hint" style={{ marginTop: 14 }}>
          没有图形界面的机器可以用命令行：<code>nyatunnel enroll &lt;服务器&gt; &lt;注册码&gt;</code>
        </div>
      </div>
    </div>
  );
}
