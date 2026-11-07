import { C, FONT_MONO } from "../theme.ts";

export const Terminal: React.FC<{
  title?: string;
  width?: number;
  height?: number;
  children: React.ReactNode;
  style?: React.CSSProperties;
}> = ({ title = "", width = 1100, height = 520, children, style }) => (
  <div
    style={{
      width,
      height,
      background: C.panel,
      border: `1px solid ${C.line}`,
      borderRadius: 12,
      overflow: "hidden",
      fontFamily: FONT_MONO,
      fontSize: 28,
      color: C.text,
      boxShadow: "0 30px 80px rgba(0,0,0,0.45)",
      ...style,
    }}
  >
    <div
      style={{
        height: 48,
        borderBottom: `1px solid ${C.line}`,
        display: "flex",
        alignItems: "center",
        padding: "0 18px",
        gap: 10,
        color: C.dim,
        fontSize: 20,
      }}
    >
      <span style={{ width: 12, height: 12, borderRadius: 6, background: C.faint }} />
      <span style={{ width: 12, height: 12, borderRadius: 6, background: C.faint }} />
      <span style={{ width: 12, height: 12, borderRadius: 6, background: C.faint }} />
      <span style={{ marginLeft: 12 }}>{title}</span>
    </div>
    <div style={{ padding: "24px 30px", lineHeight: 1.55, whiteSpace: "pre" }}>{children}</div>
  </div>
);

/** Reveals lines one at a time, `every` frames apart, starting at `from`. */
export function visibleLines<T>(lines: T[], frame: number, from: number, every: number): T[] {
  const n = Math.max(0, Math.floor((frame - from) / every) + 1);
  return frame < from ? [] : lines.slice(0, n);
}
