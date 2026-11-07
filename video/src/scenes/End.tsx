import React from "react";
import { AbsoluteFill, interpolate, useCurrentFrame } from "remotion";
import { C, FONT_MONO, FONT_SANS } from "../theme.ts";
import { BEAT, pop, ramp } from "../beat.ts";
import { Backdrop, Tag } from "../components/Primitives.tsx";

/** Bars 23-25. The last impact, the address, and then nothing. */
export const End: React.FC<{ dur: number }> = ({ dur }) => {
  const frame = useCurrentFrame();

  const ring = ramp(frame, 0, 30);
  const mark = pop(frame, 0, 120);
  const url = ramp(frame, BEAT * 1.5, 14);
  const meta = ramp(frame, BEAT * 2.5, 14);
  const out = interpolate(frame, [dur - 34, dur - 4], [1, 0], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });

  return (
    <AbsoluteFill style={{ background: C.bg, opacity: out }}>
      <Backdrop opacity={0.6} />

      <AbsoluteFill style={{ alignItems: "center", justifyContent: "center" }}>
        <div
          style={{
            width: 40 + ring * 1700,
            height: 40 + ring * 1700,
            borderRadius: 999,
            border: `2px solid ${C.accent}`,
            opacity: Math.max(0, 1 - ring) * 0.4,
          }}
        />
      </AbsoluteFill>

      <AbsoluteFill
        style={{ alignItems: "center", justifyContent: "center", flexDirection: "column" }}
      >
        <div
          style={{
            fontFamily: FONT_SANS,
            fontWeight: 700,
            fontSize: 150,
            letterSpacing: -8,
            color: C.text,
            opacity: mark,
            transform: `scale(${0.88 + mark * 0.12})`,
          }}
        >
          perry
        </div>

        <div
          style={{
            fontFamily: FONT_MONO,
            fontSize: 42,
            color: C.accent,
            opacity: url,
            marginTop: 22,
          }}
        >
          github.com/FloareDor/perry
        </div>

        <Tag style={{ marginTop: 46, color: C.faint, opacity: meta }}>
          go · sqlite · mit
        </Tag>
      </AbsoluteFill>
    </AbsoluteFill>
  );
};
