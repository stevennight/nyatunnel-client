import { useEffect, useRef } from "react";
import type { LogLine } from "../api";
import { clockTime } from "../util";

function levelClass(level: string): string {
  if (level.startsWith("err")) return "e";
  if (level.startsWith("warn")) return "w";
  return "i";
}

/** Dark log panel; sticks to the bottom unless the user scrolled up. */
export function LogView(props: { lines: LogLine[]; className?: string; empty?: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const stick = useRef(true);

  useEffect(() => {
    const el = ref.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [props.lines]);

  return (
    <div
      ref={ref}
      className={`log ${props.className ?? ""}`}
      onScroll={(e) => {
        const el = e.currentTarget;
        stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
      }}
    >
      {props.lines.length === 0 && <span className="t">{props.empty ?? "暂无日志"}</span>}
      {props.lines.map((l) => (
        <div key={`${l.run ?? 0}-${l.seq}`}>
          <span className={levelClass(l.level)}>{clockTime(l.time)}</span> {l.text}
        </div>
      ))}
    </div>
  );
}
