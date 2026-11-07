import React from "react";
import { AbsoluteFill, interpolate, useCurrentFrame } from "remotion";
import { C, FONT_MONO, FONT_SANS } from "../theme.ts";
import { BAR, BEAT, pop, ramp } from "../beat.ts";
import { Backdrop, Bar, Frame, SceneOut, Tag } from "../components/Primitives.tsx";

const CALLS = [
  { verb: "GET", path: "/files?glob=**/*.go", result: "412 paths", size: "6.1 KB" },
  { verb: "GET", path: "/grep?q=ParseFlags&ref=main", result: "9 hits", size: "4.8 KB" },
  { verb: "GET", path: "/file?ref=main&path=command.go", result: "1 blob", size: "8.4 KB" },
];

const SWITCH = BAR; // the page turn lands on bar 12
const START = SWITCH + BEAT;
const LANE = 1320;

/**
 * Bars 11-15. First what an agent actually asks for, then the same work
 * raced against a clone. The right-hand lane is finished before the
 * left-hand one is a fifth of the way through, which is the whole point.
 */
export const Api: React.FC<{ dur: number }> = ({ dur }) => {
  const frame = useCurrentFrame();

  const pageOne = interpolate(frame, [SWITCH - 10, SWITCH], [1, 0], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });
  const pageTwo = ramp(frame, SWITCH, 10);

  const perry = ramp(frame, START, 22);
  const clone = ramp(frame, START, 190);
  const done = perry >= 1;

  return (
    <SceneOut durationInFrames={dur}>
      <AbsoluteFill style={{ background: C.bg }}>
        <Backdrop opacity={0.4} />

        {/* Page one: three requests, one per beat. */}
        <AbsoluteFill style={{ opacity: pageOne, pointerEvents: "none" }}>
          <Frame>
            <Tag style={{ marginBottom: 54 }}>an agent does not want a clone</Tag>
            {CALLS.map((c, i) => {
              const t = pop(frame, i * BEAT, 150);
              return (
                <div
                  key={c.path}
                  style={{
                    display: "flex",
                    alignItems: "baseline",
                    gap: 28,
                    padding: "26px 0",
                    borderBottom: `1px solid ${C.line}`,
                    fontFamily: FONT_MONO,
                    fontSize: 38,
                    opacity: t,
                    transform: `translateX(${(1 - t) * 34}px)`,
                  }}
                >
                  <span style={{ color: C.accent, fontWeight: 700 }}>{c.verb}</span>
                  <span style={{ color: C.text }}>{c.path}</span>
                  <span style={{ marginLeft: "auto", color: C.dim, fontSize: 30 }}>
                    {c.result}
                  </span>
                  <span style={{ color: C.faint, fontSize: 30, width: 130, textAlign: "right" }}>
                    {c.size}
                  </span>
                </div>
              );
            })}
          </Frame>
        </AbsoluteFill>

        {/* Page two: the same work, raced against a clone. */}
        <AbsoluteFill style={{ opacity: pageTwo }}>
          <Frame>
            <Lane
              label="git clone"
              sub="the whole repository, then look"
              value={clone}
              time={`${((1310 * clone) / 1000).toFixed(2)} s`}
              bytes={`${(7.97 * clone).toFixed(2)} MB`}
              color={C.faint}
              finished={clone >= 1}
            />
            <div style={{ height: 96 }} />
            <Lane
              label="perry"
              sub="three requests, no working tree"
              value={perry}
              time={`${Math.round(118 * perry)} ms`}
              bytes={`${(19.3 * perry).toFixed(1)} KB`}
              color={C.accent}
              finished={done}
            />

            <div
              style={{
                display: "flex",
                gap: 28,
                marginTop: 92,
                opacity: ramp(frame, SWITCH + BAR, 14),
              }}
            >
              <Ratio value="11x" label="faster to start" />
              <Ratio value="423x" label="fewer bytes" />
            </div>
          </Frame>
        </AbsoluteFill>
      </AbsoluteFill>
    </SceneOut>
  );
};

const Lane: React.FC<{
  label: string;
  sub: string;
  value: number;
  time: string;
  bytes: string;
  color: string;
  finished: boolean;
}> = ({ label, sub, value, time, bytes, color, finished }) => (
  <div>
    <div style={{ display: "flex", alignItems: "baseline", gap: 20, marginBottom: 20 }}>
      <div style={{ fontFamily: FONT_SANS, fontWeight: 600, fontSize: 46, color }}>{label}</div>
      <div style={{ fontFamily: FONT_SANS, fontSize: 26, color: C.faint }}>{sub}</div>
      <div
        style={{
          marginLeft: "auto",
          fontFamily: FONT_MONO,
          fontSize: 40,
          color: finished ? color : C.dim,
          fontWeight: finished ? 700 : 400,
        }}
      >
        {time}
        <span style={{ color: C.faint, fontWeight: 400 }}>{"   "}{bytes}</span>
      </div>
    </div>
    <Bar value={value} width={LANE} height={16} color={color} track={C.panel} glow={finished} />
  </div>
);

const Ratio: React.FC<{ value: string; label: string }> = ({ value, label }) => (
  <div
    style={{
      padding: "20px 34px",
      border: `1px solid ${C.accentDim}`,
      background: `${C.accent}12`,
      borderRadius: 12,
      display: "flex",
      alignItems: "baseline",
      gap: 18,
    }}
  >
    <span style={{ fontFamily: FONT_SANS, fontWeight: 700, fontSize: 56, color: C.accent }}>
      {value}
    </span>
    <span style={{ fontFamily: FONT_SANS, fontSize: 28, color: C.dim }}>{label}</span>
  </div>
);
