import { interpolate, spring, useCurrentFrame, useVideoConfig } from "remotion";
import { C, FONT_SANS } from "../theme.ts";

/** The one big line a scene is built around. Rises and fades in once. */
export const Headline: React.FC<{
  children: React.ReactNode;
  delay?: number;
  size?: number;
  color?: string;
  style?: React.CSSProperties;
}> = ({ children, delay = 0, size = 88, color = C.text, style }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const t = spring({ frame: frame - delay, fps, config: { damping: 200, stiffness: 120 } });
  return (
    <div
      style={{
        fontFamily: FONT_SANS,
        fontWeight: 600,
        fontSize: size,
        letterSpacing: -2,
        lineHeight: 1.08,
        color,
        opacity: t,
        transform: `translateY(${(1 - t) * 24}px)`,
        ...style,
      }}
    >
      {children}
    </div>
  );
};

export const Sub: React.FC<{ children: React.ReactNode; delay?: number; style?: React.CSSProperties }> = ({
  children,
  delay = 8,
  style,
}) => {
  const frame = useCurrentFrame();
  const opacity = interpolate(frame, [delay, delay + 14], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  return (
    <div style={{ fontFamily: FONT_SANS, fontSize: 36, color: C.dim, opacity, marginTop: 24, ...style }}>
      {children}
    </div>
  );
};

/** Fades a block in; used for anything that is not the headline. */
export const FadeIn: React.FC<{ children: React.ReactNode; delay?: number; style?: React.CSSProperties }> = ({
  children,
  delay = 0,
  style,
}) => {
  const frame = useCurrentFrame();
  const opacity = interpolate(frame, [delay, delay + 12], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  return <div style={{ opacity, ...style }}>{children}</div>;
};

export function countUp(frame: number, from: number, to: number, start: number, length: number): number {
  const p = interpolate(frame, [start, start + length], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  const eased = 1 - Math.pow(1 - p, 3);
  return from + (to - from) * eased;
}
