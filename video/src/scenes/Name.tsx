import React from "react";
import { AbsoluteFill, useCurrentFrame } from "remotion";
import { C, FONT_SANS } from "../theme.ts";
import { BEAT, pop, pulse, ramp } from "../beat.ts";
import { Backdrop, SceneOut, Tag } from "../components/Primitives.tsx";

/**
 * Bars 6-8. The first impact in the track lands on frame 0 of this scene,
 * so the ring, the wordmark and the drum all arrive together.
 */
export const Name: React.FC<{ dur: number }> = ({ dur }) => {
  const frame = useCurrentFrame();

  const ring = ramp(frame, 0, 28);
  const mark = pop(frame, 0, 110);
  const rule = ramp(frame, BEAT, 20);
  const sub = ramp(frame, BEAT * 1.5, 16);

  return (
    <SceneOut durationInFrames={dur}>
      <AbsoluteFill style={{ background: C.bg }}>
        <Backdrop opacity={0.6} />

        {/* The shockwave from the point the pile collapsed into. */}
        <AbsoluteFill style={{ alignItems: "center", justifyContent: "center" }}>
          <div
            style={{
              width: 40 + ring * 1900,
              height: 40 + ring * 1900,
              borderRadius: 999,
              border: `2px solid ${C.accent}`,
              opacity: Math.max(0, 1 - ring) * 0.55,
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
              fontSize: 230,
              letterSpacing: -12,
              color: C.text,
              opacity: mark,
              lineHeight: 1.1,
              transform: `scale(${(0.82 + mark * 0.18) * pulse(frame, 0.012, 5)})`,
            }}
          >
            perry
          </div>

          <div
            style={{
              width: rule * 560,
              height: 3,
              background: C.accent,
              boxShadow: `0 0 30px ${C.accent}`,
              marginTop: 62,
            }}
          />

          <Tag style={{ marginTop: 40, opacity: sub, color: C.dim }}>
            a git server for coding agents
          </Tag>
        </AbsoluteFill>
      </AbsoluteFill>
    </SceneOut>
  );
};
