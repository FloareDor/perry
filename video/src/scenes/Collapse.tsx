import React from "react";
import { AbsoluteFill, useCurrentFrame } from "remotion";
import { C } from "../theme.ts";
import { ease, ramp } from "../beat.ts";
import { Backdrop } from "../components/Primitives.tsx";
import { CARD_H, CARD_W, CloneCard, cardRate } from "../components/CloneCard.tsx";
import { CENTER_X, CENTER_Y, COUNT, cardPos } from "../components/grid.ts";

/**
 * Bar 5. The pile falls into a single point, and the music drops out with
 * it. One bar, no text: this is the breath before the name.
 */
export const Collapse: React.FC<{ dur: number }> = ({ dur }) => {
  const frame = useCurrentFrame();
  const p = ease(frame, 2, 42, 2.4);
  const dot = ramp(frame, 40, 10);

  return (
    <AbsoluteFill style={{ background: C.bg }}>
      <Backdrop opacity={0.4 * (1 - p)} />

      {Array.from({ length: COUNT }, (_, i) => {
        const { x, y } = cardPos(i);
        const dx = (CENTER_X - (x + CARD_W / 2)) * p;
        const dy = (CENTER_Y - (y + CARD_H / 2)) * p;
        return (
          <div
            key={i}
            style={{
              position: "absolute",
              left: x,
              top: y,
              opacity: Math.max(0, 1 - Math.pow(p, 1.3)),
              transform: `translate(${dx}px, ${dy}px) scale(${1 - 0.92 * p})`,
              filter: `blur(${p * 6}px)`,
            }}
          >
            <CloneCard index={i} progress={0.3 + 0.3 * cardRate(i)} />
          </div>
        );
      })}

      {/* What is left of twenty clones. */}
      <AbsoluteFill style={{ alignItems: "center", justifyContent: "center" }}>
        <div
          style={{
            width: 18,
            height: 18,
            borderRadius: 999,
            background: C.accent,
            opacity: dot,
            transform: `scale(${0.4 + dot})`,
            boxShadow: `0 0 ${60 * dot}px ${C.accent}`,
          }}
        />
      </AbsoluteFill>
    </AbsoluteFill>
  );
};
