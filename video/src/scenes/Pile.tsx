import React from "react";
import { AbsoluteFill, useCurrentFrame } from "remotion";
import { C, FONT_MONO } from "../theme.ts";
import { BEAT, landed, pop, pulse, ramp } from "../beat.ts";
import { Backdrop, SceneOut, Tag } from "../components/Primitives.tsx";
import { CloneCard, cardRate } from "../components/CloneCard.tsx";
import { COUNT, cardPos } from "../components/grid.ts";

/** One card lands every eighth note, so the grid fills in time with the hats. */
const EVERY = BEAT / 2;

/**
 * Bars 2-5. The same card as the cold open, twenty times over. None of the
 * bars reach the end and the count in the corner keeps climbing.
 */
export const Pile: React.FC<{ dur: number }> = ({ dur }) => {
  const frame = useCurrentFrame();
  const n = landed(frame, 0, EVERY, COUNT);
  const heat = ramp(frame, EVERY * COUNT - 24, 40);

  return (
    <SceneOut durationInFrames={dur}>
      <AbsoluteFill style={{ background: C.bg }}>
        <Backdrop opacity={0.4} />

        {Array.from({ length: COUNT }, (_, i) => {
          const at = i * EVERY;
          if (frame < at) return null;
          const { x, y } = cardPos(i);
          const enter = pop(frame, at, 170);
          const progress = ramp(frame, at + 4, 420 / cardRate(i));
          return (
            <div
              key={i}
              style={{
                position: "absolute",
                left: x,
                top: y,
                opacity: enter,
                transform: `translateY(${(1 - enter) * 26}px) scale(${0.94 + enter * 0.06})`,
              }}
            >
              <CloneCard index={i} progress={progress} />
            </div>
          );
        })}

        {/* The count is the only thing that moves on the beat. */}
        <div
          style={{
            position: "absolute",
            right: 130,
            top: 120,
            fontFamily: FONT_MONO,
            fontWeight: 700,
            fontSize: 108,
            color: C.text,
            transform: `scale(${pulse(frame, 0.05, 4, EVERY)})`,
            transformOrigin: "right top",
          }}
        >
          {String(n).padStart(2, "0")}
          <span style={{ color: C.faint }}> / 20</span>
        </div>

        <Tag style={{ position: "absolute", left: 130, top: 150 }}>one repository</Tag>
        <Tag style={{ position: "absolute", left: 130, bottom: 130, color: C.dim }}>
          {(n * 7.97).toFixed(0)} MB cloned before a single line is read
        </Tag>

        {/* The frame warms up as the grid fills. Nothing is on fire, it is
            just getting heavy. */}
        <AbsoluteFill
          style={{
            opacity: heat * 0.5,
            background: `radial-gradient(ellipse at center, transparent 35%, ${C.warn}22 100%)`,
          }}
        />
      </AbsoluteFill>
    </SceneOut>
  );
};
