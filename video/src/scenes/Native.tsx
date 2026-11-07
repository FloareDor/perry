import React from "react";
import { AbsoluteFill, useCurrentFrame } from "remotion";
import { C, FONT_MONO } from "../theme.ts";
import { BEAT, pop, ramp } from "../beat.ts";
import { Backdrop, Chip, Frame, SceneOut, Tag } from "../components/Primitives.tsx";

interface Line {
  at: number;
  text: string;
  color: string;
}

const B = BEAT;

const LINES: Line[] = [
  { at: 0, text: "$ git clone http://localhost:8080/r/cobra", color: C.text },
  { at: B * 0.75, text: "Cloning into 'cobra'...", color: C.dim },
  { at: B * 1.5, text: "remote: Enumerating objects: 1078, done.", color: C.dim },
  { at: B * 2.25, text: "Receiving objects: 100% (1078/1078), 456.12 KiB", color: C.dim },
  { at: B * 3, text: "", color: C.dim },
  { at: B * 3.5, text: "$ git push origin main", color: C.text },
  { at: B * 4.25, text: "remote: perry: cas ok  refs/heads/main", color: C.dim },
  { at: B * 5, text: " * [new branch]      main -> main", color: C.accent },
];

const PARTS = ["packfile v2", "ofs / ref delta", "smart http v1", "report-status"];

/**
 * Bars 8-11. Proof that the thing on the other end is a real git remote:
 * a stock clone and a stock push, with the pieces that had to be written
 * lighting up beside them.
 */
export const Native: React.FC<{ dur: number }> = ({ dur }) => {
  const frame = useCurrentFrame();

  return (
    <SceneOut durationInFrames={dur}>
      <AbsoluteFill style={{ background: C.bg }}>
        <Backdrop opacity={0.4} />
        <Frame>
          <Tag style={{ marginBottom: 34 }}>still a git remote</Tag>

          <div style={{ display: "flex", gap: 56, alignItems: "flex-start" }}>
            <div
              style={{
                flex: 1,
                background: C.panel,
                border: `1px solid ${C.line}`,
                borderRadius: 12,
                padding: "34px 40px",
                fontFamily: FONT_MONO,
                fontSize: 27,
                lineHeight: 1.72,
                minHeight: 420,
                boxShadow: "0 40px 90px rgba(0,0,0,0.5)",
              }}
            >
              {LINES.map((l, i) => (
                <div
                  key={i}
                  style={{
                    color: l.color,
                    opacity: ramp(frame, l.at, 6),
                    minHeight: 46,
                    whiteSpace: "pre",
                  }}
                >
                  {l.text}
                </div>
              ))}
            </div>

            <div style={{ display: "flex", flexDirection: "column", gap: 20, width: 330 }}>
              {PARTS.map((p, i) => {
                const at = B * 1.5 + i * B * 1.25;
                const t = pop(frame, at, 160);
                return (
                  <div
                    key={p}
                    style={{ opacity: t, transform: `translateX(${(1 - t) * 26}px)` }}
                  >
                    <Chip>{p}</Chip>
                  </div>
                );
              })}
            </div>
          </div>

          <Tag style={{ marginTop: 44, opacity: ramp(frame, B * 6.5, 14), color: C.faint }}>
            written in go. the server never shells out to git
          </Tag>
        </Frame>
      </AbsoluteFill>
    </SceneOut>
  );
};
