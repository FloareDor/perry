import React from "react";
import { AbsoluteFill, useCurrentFrame } from "remotion";
import { C, FONT_SANS } from "../theme.ts";
import { BEAT, pop } from "../beat.ts";
import { Backdrop, Frame, SceneOut, Tag } from "../components/Primitives.tsx";

const STATS = [
  { value: "11x", label: "faster to start\na task" },
  { value: "423x", label: "fewer bytes\non the wire" },
  { value: "40", label: "pushes at once,\nnothing lost" },
  { value: "1", label: "binary, one\nSQLite file" },
];

/** Bars 21-23. Four figures, one per beat. */
export const Numbers: React.FC<{ dur: number }> = ({ dur }) => {
  const frame = useCurrentFrame();

  return (
    <SceneOut durationInFrames={dur}>
      <AbsoluteFill style={{ background: C.bg }}>
        <Backdrop opacity={0.5} />
        <Frame>
          <Tag style={{ marginBottom: 70 }}>measured on a 400-file repository</Tag>
          <div style={{ display: "flex", gap: 34 }}>
            {STATS.map((s, i) => {
              const t = pop(frame, i * BEAT, 130);
              return (
                <div
                  key={s.value}
                  style={{
                    flex: 1,
                    padding: "48px 40px",
                    background: C.panel,
                    border: `1px solid ${C.line}`,
                    borderRadius: 14,
                    opacity: t,
                    transform: `translateY(${(1 - t) * 30}px)`,
                  }}
                >
                  <div
                    style={{
                      fontFamily: FONT_SANS,
                      fontWeight: 700,
                      fontSize: 110,
                      letterSpacing: -5,
                      color: C.accent,
                      lineHeight: 1,
                    }}
                  >
                    {s.value}
                  </div>
                  <div
                    style={{
                      marginTop: 26,
                      fontFamily: FONT_SANS,
                      fontSize: 29,
                      lineHeight: 1.4,
                      color: C.dim,
                      whiteSpace: "pre-line",
                    }}
                  >
                    {s.label}
                  </div>
                </div>
              );
            })}
          </div>
        </Frame>
      </AbsoluteFill>
    </SceneOut>
  );
};
