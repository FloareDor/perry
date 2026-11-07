import React from "react";
import { AbsoluteFill, useCurrentFrame } from "remotion";
import { C, FONT_MONO } from "../theme.ts";
import { BAR, BEAT, pop, ramp } from "../beat.ts";
import { Backdrop, Chip, SceneOut, Tag } from "../components/Primitives.tsx";

const N = 20;
const TRUNK_Y = 560;
const X0 = 180;
const X1 = 1780;
const CONFLICT = 13; // one agent lands on a file another agent already touched

const START = BEAT;
const STAGGER = 5;
const DRAW = 54;

interface Branch {
  i: number;
  depart: number;
  rejoin: number;
  y: number;
  at: number;
  conflict: boolean;
}

const BRANCHES: Branch[] = Array.from({ length: N }, (_, i) => {
  const side = i % 2 === 0 ? -1 : 1;
  const rank = Math.floor(i / 2);
  return {
    i,
    depart: 330 + i * 8,
    rejoin: 1230 + i * 14,
    y: TRUNK_Y + side * (46 + rank * 42),
    at: START + i * STAGGER,
    conflict: i === CONFLICT,
  };
});

function pathFor(b: Branch): string {
  const a = b.depart;
  const z = b.rejoin;
  return [
    `M ${a} ${TRUNK_Y}`,
    `C ${a + 70} ${TRUNK_Y} ${a + 70} ${b.y} ${a + 140} ${b.y}`,
    `L ${z - 140} ${b.y}`,
    `C ${z - 70} ${b.y} ${z - 70} ${TRUNK_Y} ${z} ${TRUNK_Y}`,
  ].join(" ");
}

/**
 * Bars 15-19. Twenty branches peel off one trunk and come back. Each
 * rejoin flashes the ref node, which is a compare-and-swap landing. One
 * of them touches a file another agent already changed and goes amber
 * instead of quietly winning.
 */
export const Fleet: React.FC<{ dur: number }> = ({ dur }) => {
  const frame = useCurrentFrame();
  const trunk = ramp(frame, 0, 22);

  const landedCount = BRANCHES.filter((b) => frame >= b.at + DRAW).length;

  return (
    <SceneOut durationInFrames={dur}>
      <AbsoluteFill style={{ background: C.bg }}>
        <Backdrop opacity={0.35} />

        <svg width={1920} height={1080} style={{ position: "absolute", inset: 0 }}>
          <line
            x1={X0}
            y1={TRUNK_Y}
            x2={X0 + (X1 - X0) * trunk}
            y2={TRUNK_Y}
            stroke={C.line}
            strokeWidth={4}
          />

          {BRANCHES.map((b) => {
            if (frame < b.at) return null;
            const p = ramp(frame, b.at, DRAW);
            const colour = b.conflict ? C.amber : C.accentDim;
            const flat = p > 0.22 && p < 0.84;
            const dotX = b.depart + 140 + (b.rejoin - 280 - b.depart) * ((p - 0.22) / 0.62);
            return (
              <g key={b.i}>
                <path
                  d={pathFor(b)}
                  fill="none"
                  stroke={colour}
                  strokeWidth={2.5}
                  pathLength={1}
                  strokeDasharray={1}
                  strokeDashoffset={1 - p}
                  opacity={0.85}
                />
                {flat ? (
                  <circle cx={dotX} cy={b.y} r={7} fill={b.conflict ? C.amber : C.accent} />
                ) : null}
              </g>
            );
          })}

          {/* A ref node flashes on the trunk every time a push lands. */}
          {BRANCHES.map((b) => {
            const land = b.at + DRAW;
            if (frame < land) return null;
            const f = ramp(frame, land, 12);
            const colour = b.conflict ? C.amber : C.accent;
            return (
              <g key={`n${b.i}`}>
                <circle
                  cx={b.rejoin}
                  cy={TRUNK_Y}
                  r={6 + (1 - f) * 22}
                  fill="none"
                  stroke={colour}
                  strokeWidth={2}
                  opacity={(1 - f) * 0.8}
                />
                <circle cx={b.rejoin} cy={TRUNK_Y} r={6} fill={colour} />
              </g>
            );
          })}
        </svg>

        <Tag style={{ position: "absolute", left: 130, top: 120 }}>
          twenty agents, one repository
        </Tag>

        <div
          style={{
            position: "absolute",
            right: 130,
            top: 112,
            fontFamily: FONT_MONO,
            fontSize: 44,
            color: C.dim,
            textAlign: "right",
          }}
        >
          pushed{" "}
          <span style={{ color: C.accent, fontWeight: 700 }}>
            {String(landedCount).padStart(2, "0")}
          </span>
          <span style={{ color: C.faint }}> / 20</span>
        </div>

        <div
          style={{
            position: "absolute",
            left: 130,
            bottom: 128,
            display: "flex",
            gap: 20,
            opacity: ramp(frame, BAR * 2, 14),
          }}
        >
          <Chip>compare-and-swap on every ref</Chip>
          <Chip>709 ms for all twenty</Chip>
        </div>

        <div
          style={{
            position: "absolute",
            right: 130,
            bottom: 128,
            opacity: pop(frame, BRANCHES[CONFLICT].at + DRAW, 160),
          }}
        >
          <Chip color={C.amber}>merge queue: two agents, one file</Chip>
        </div>
      </AbsoluteFill>
    </SceneOut>
  );
};
