import React from "react";
import { AbsoluteFill, interpolate, useCurrentFrame } from "remotion";
import { C, FONT_MONO, FONT_SANS } from "../theme.ts";
import { BEAT, ramp } from "../beat.ts";

/** Every scene sits on the same margin and never touches the frame edge. */
export const Frame: React.FC<{
  children: React.ReactNode;
  style?: React.CSSProperties;
}> = ({ children, style }) => (
  <AbsoluteFill
    style={{
      padding: 130,
      display: "flex",
      flexDirection: "column",
      justifyContent: "center",
      ...style,
    }}
  >
    {children}
  </AbsoluteFill>
);

/**
 * A faint grid behind everything. It drifts a pixel or two so the frame is
 * never completely still, which keeps flat colour from looking like a freeze.
 */
export const Backdrop: React.FC<{ opacity?: number }> = ({ opacity = 1 }) => {
  const frame = useCurrentFrame();
  const drift = (frame * 0.12) % 60;
  return (
    <AbsoluteFill
      style={{
        opacity: 0.5 * opacity,
        backgroundImage: `linear-gradient(${C.line} 1px, transparent 1px), linear-gradient(90deg, ${C.line} 1px, transparent 1px)`,
        backgroundSize: "60px 60px",
        backgroundPosition: `${drift}px ${drift}px`,
        maskImage: "radial-gradient(ellipse at center, black 10%, transparent 72%)",
        WebkitMaskImage: "radial-gradient(ellipse at center, black 10%, transparent 72%)",
      }}
    />
  );
};

/** A small uppercase mono label. The only chrome any scene gets. */
export const Tag: React.FC<{
  children: React.ReactNode;
  color?: string;
  style?: React.CSSProperties;
}> = ({ children, color = C.dim, style }) => (
  <div
    style={{
      fontFamily: FONT_MONO,
      fontSize: 22,
      letterSpacing: 3,
      textTransform: "uppercase",
      color,
      ...style,
    }}
  >
    {children}
  </div>
);

export const Mono: React.FC<{
  children: React.ReactNode;
  size?: number;
  color?: string;
  weight?: number;
  style?: React.CSSProperties;
}> = ({ children, size = 30, color = C.text, weight = 400, style }) => (
  <div style={{ fontFamily: FONT_MONO, fontSize: size, color, fontWeight: weight, ...style }}>
    {children}
  </div>
);

/** The one large line a scene is allowed. */
export const Title: React.FC<{
  children: React.ReactNode;
  size?: number;
  color?: string;
  style?: React.CSSProperties;
}> = ({ children, size = 96, color = C.text, style }) => (
  <div
    style={{
      fontFamily: FONT_SANS,
      fontWeight: 600,
      fontSize: size,
      letterSpacing: -3,
      lineHeight: 1.04,
      color,
      ...style,
    }}
  >
    {children}
  </div>
);

export const Fade: React.FC<{
  at?: number;
  length?: number;
  children: React.ReactNode;
  style?: React.CSSProperties;
}> = ({ at = 0, length = 10, children, style }) => {
  const frame = useCurrentFrame();
  return <div style={{ opacity: ramp(frame, at, length), ...style }}>{children}</div>;
};

/** A caret that blinks on the beat rather than on a timer of its own. */
export const Caret: React.FC<{ color?: string; height?: number }> = ({
  color = C.accent,
  height = 34,
}) => {
  const frame = useCurrentFrame();
  const on = frame % (BEAT * 2) < BEAT;
  return (
    <span
      style={{
        display: "inline-block",
        width: 14,
        height,
        background: color,
        opacity: on ? 1 : 0.15,
        verticalAlign: "-5px",
        marginLeft: 6,
      }}
    />
  );
};

/** A horizontal progress bar. `value` is 0..1. */
export const Bar: React.FC<{
  value: number;
  width: number;
  height?: number;
  color?: string;
  track?: string;
  glow?: boolean;
  style?: React.CSSProperties;
}> = ({ value, width, height = 10, color = C.accent, track = C.line, glow, style }) => (
  <div
    style={{
      width,
      height,
      background: track,
      borderRadius: height / 2,
      overflow: "hidden",
      ...style,
    }}
  >
    <div
      style={{
        width: `${Math.max(0, Math.min(1, value)) * 100}%`,
        height: "100%",
        background: color,
        borderRadius: height / 2,
        boxShadow: glow ? `0 0 24px ${color}` : undefined,
      }}
    />
  </div>
);

/** A rounded chip, used for endpoints, feature names and verdicts. */
export const Chip: React.FC<{
  children: React.ReactNode;
  color?: string;
  active?: boolean;
  size?: number;
  style?: React.CSSProperties;
}> = ({ children, color = C.accent, active = true, size = 26, style }) => (
  <div
    style={{
      fontFamily: FONT_MONO,
      fontSize: size,
      padding: `${size * 0.42}px ${size * 0.8}px`,
      borderRadius: 999,
      border: `1px solid ${active ? color : C.line}`,
      color: active ? color : C.faint,
      background: active ? `${color}14` : "transparent",
      whiteSpace: "nowrap",
      ...style,
    }}
  >
    {children}
  </div>
);

export function fmtMs(ms: number): string {
  return ms >= 1000 ? `${(ms / 1000).toFixed(2)} s` : `${Math.round(ms)} ms`;
}

export function fmtBytes(b: number): string {
  if (b >= 1024 * 1024) return `${(b / 1024 / 1024).toFixed(2)} MB`;
  if (b >= 1024) return `${(b / 1024).toFixed(1)} KB`;
  return `${Math.round(b)} B`;
}

/** Fades a whole scene out over its last few frames so cuts are not hard. */
export const SceneOut: React.FC<{
  durationInFrames: number;
  children: React.ReactNode;
  length?: number;
}> = ({ durationInFrames, children, length = 7 }) => {
  const frame = useCurrentFrame();
  const opacity = interpolate(
    frame,
    [durationInFrames - length, durationInFrames],
    [1, 0],
    { extrapolateLeft: "clamp", extrapolateRight: "clamp" },
  );
  return <AbsoluteFill style={{ opacity }}>{children}</AbsoluteFill>;
};
