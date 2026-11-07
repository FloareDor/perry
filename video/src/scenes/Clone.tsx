import React from "react";
import { AbsoluteFill, useCurrentFrame } from "remotion";
import { C, FONT_MONO } from "../theme.ts";
import { BEAT, ramp } from "../beat.ts";
import { Backdrop, Bar, Caret, Frame, Mono, SceneOut, Tag } from "../components/Primitives.tsx";

const CMD = "$ git clone https://github.com/spf13/cobra";

/**
 * Bars 0-2. One command, one bar that does not finish inside the scene.
 * Nothing else on screen, because the whole video is an argument against
 * this line and it should be allowed to sit there.
 */
export const Clone: React.FC<{ dur: number }> = ({ dur }) => {
  const frame = useCurrentFrame();

  const typed = Math.floor(ramp(frame, BEAT, 34) * CMD.length);
  const started = frame >= BEAT * 3;
  const progress = started ? ramp(frame, BEAT * 3, 420) : 0;

  const elapsed = started ? ((frame - BEAT * 3) / 30) * 1000 : 0;

  return (
    <SceneOut durationInFrames={dur}>
      <AbsoluteFill style={{ background: C.bg }}>
        <Backdrop opacity={0.5} />
        <Frame style={{ justifyContent: "center", alignItems: "flex-start" }}>
          <div style={{ width: 1660 }}>
            <Mono size={42} color={C.text} style={{ letterSpacing: -0.5 }}>
              {CMD.slice(0, typed)}
              {typed >= CMD.length ? null : <Caret height={40} />}
            </Mono>

            <div style={{ opacity: started ? 1 : 0, marginTop: 56 }}>
              <Bar value={progress} width={1660} height={12} color={C.faint} track={C.panel} />
              <div
                style={{
                  marginTop: 22,
                  display: "flex",
                  justifyContent: "space-between",
                  fontFamily: FONT_MONO,
                  fontSize: 26,
                  color: C.dim,
                }}
              >
                <span>{(7.97 * progress).toFixed(2)} MB of 7.97 MB</span>
                <span>{(elapsed / 1000).toFixed(2)} s</span>
              </div>
            </div>
          </div>
        </Frame>
        <Tag style={{ position: "absolute", left: 130, bottom: 130 }}>
          every task starts here
        </Tag>
      </AbsoluteFill>
    </SceneOut>
  );
};
