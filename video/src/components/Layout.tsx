import { AbsoluteFill } from "remotion";

/** Everything sits on a 120px margin; scenes do not touch the frame edge. */
export const Frame: React.FC<{ children: React.ReactNode; style?: React.CSSProperties }> = ({ children, style }) => (
  <AbsoluteFill style={{ padding: 120, display: "flex", flexDirection: "column", justifyContent: "center", ...style }}>
    {children}
  </AbsoluteFill>
);

export const Row: React.FC<{ children: React.ReactNode; gap?: number; style?: React.CSSProperties }> = ({ children, gap = 48, style }) => (
  <div style={{ display: "flex", flexDirection: "row", gap, alignItems: "center", ...style }}>{children}</div>
);
