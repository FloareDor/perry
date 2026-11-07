import React from "react";
import { AbsoluteFill, useCurrentFrame } from "remotion";
import { C, FONT_MONO, FONT_SANS } from "../theme.ts";
import { BEAT, ease, ramp } from "../beat.ts";
import { Backdrop, Frame, SceneOut, Tag } from "../components/Primitives.tsx";

const MAX_KB = 2252; // 2.2 MB, the same objects with no ordering at all
const MAX_PX = 1180;
const px = (kb: number) => (kb / MAX_KB) * MAX_PX;

const ROWS = [
  { label: "unordered", kb: MAX_KB, text: "2.20 MB", color: C.faint, at: 0 },
  { label: "git 2.35", kb: 537, text: "537 KB", color: C.dim, at: BEAT },
  { label: "perry", kb: 456, text: "456 KB", color: C.accent, at: BEAT * 2 },
];

/**
 * Bars 19-21. The same 1078 objects, three ways. Perry's bar stops short
 * of the dashed line, which is where git's own packer lands.
 */
export const Pack: React.FC<{ dur: number }> = ({ dur }) => {
  const frame = useCurrentFrame();

  return (
    <SceneOut durationInFrames={dur}>
      <AbsoluteFill style={{ background: C.bg }}>
        <Backdrop opacity={0.4} />
        <Frame>
          <Tag style={{ marginBottom: 56 }}>one packfile, 1078 objects</Tag>

          <div style={{ position: "relative" }}>
            {ROWS.map((r) => {
              const g = ease(frame, r.at, 34);
              return (
                <div
                  key={r.label}
                  style={{ display: "flex", alignItems: "center", gap: 32, marginBottom: 38 }}
                >
                  <div
                    style={{
                      width: 230,
                      fontFamily: FONT_SANS,
                      fontSize: 34,
                      color: r.color === C.accent ? C.text : C.dim,
                      fontWeight: r.color === C.accent ? 600 : 400,
                      textAlign: "right",
                    }}
                  >
                    {r.label}
                  </div>
                  <div
                    style={{
                      width: px(r.kb) * g,
                      height: 58,
                      background: r.color,
                      borderRadius: 6,
                      opacity: r.color === C.accent ? 1 : 0.5,
                      boxShadow: r.color === C.accent ? `0 0 40px ${C.accentGlow}` : undefined,
                    }}
                  />
                  <div
                    style={{
                      fontFamily: FONT_MONO,
                      fontSize: 32,
                      color: r.color === C.accent ? C.accent : C.dim,
                      opacity: ramp(frame, r.at + 24, 10),
                    }}
                  >
                    {r.text}
                  </div>
                </div>
              );
            })}

            {/* Where git lands, so the last bar has something to beat. */}
            <div
              style={{
                position: "absolute",
                left: 262 + px(537),
                top: -14,
                height: 320,
                borderLeft: `2px dashed ${C.line}`,
                opacity: ramp(frame, BEAT * 2, 14),
              }}
            />
          </div>

          <Tag style={{ marginTop: 34, color: C.faint, opacity: ramp(frame, BEAT * 3.5, 14) }}>
            objects sorted by path hash, so a ten-object window finds a base
          </Tag>
        </Frame>
      </AbsoluteFill>
    </SceneOut>
  );
};
