import { useState } from "react";
import type { LogLine } from "../api";
import { LogView } from "../components/LogView";
import { copyText, useNotify } from "../components/ui";

export function LogsPage(props: { lines: LogLine[]; onClear: () => void }) {
  const notify = useNotify();
  const [filter, setFilter] = useState("");
  const [warnOnly, setWarnOnly] = useState(false);
  const f = filter.trim().toLowerCase();
  const lines = props.lines.filter(
    (l) => (!warnOnly || l.level !== "info") && (!f || l.text.toLowerCase().includes(f))
  );
  return (
    <>
      <div className="ttl">
        <h2>日志</h2>
        <div className="inline wrap">
          <input className="inp" aria-label="筛选日志" placeholder="筛选…" value={filter}
            onChange={(e) => setFilter(e.target.value)} style={{ width: 180 }} />
          <label className="chk" style={{ margin: 0 }}>
            <input type="checkbox" checked={warnOnly} onChange={(e) => setWarnOnly(e.target.checked)} />只看警告与错误
          </label>
          <button className="btn sm" onClick={async () => {
            const text = lines.map((l) => `${l.time} ${l.level.toUpperCase()} ${l.text}`).join("\n");
            const ok = await copyText(text);
            notify(ok ? "已复制日志" : "复制失败", ok ? "ok" : "bad");
          }}>复制</button>
          <button className="btn sm" onClick={props.onClear}>清空</button>
        </div>
      </div>
      <LogView lines={lines} className="big" />
    </>
  );
}
