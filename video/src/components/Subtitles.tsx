import { useCurrentFrame, useVideoConfig } from "remotion";
import type { Caption } from "@remotion/captions";
import { C, FONT_SANS } from "../theme.ts";

/**
 * Lower-third captions. The whole line is shown and the word being spoken
 * brightens, which is readable without jumping around.
 */
export const Subtitles: React.FC<{ captions: Caption[] }> = ({ captions }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const nowMs = (frame / fps) * 1000;

  return (
    <div
      style={{
        position: "absolute",
        left: 0,
        right: 0,
        bottom: 64,
        textAlign: "center",
        fontFamily: FONT_SANS,
        fontSize: 30,
        fontWeight: 500,
        lineHeight: 1.4,
        padding: "0 240px",
      }}
    >
      {captions.map((c, i) => {
        const spoken = nowMs >= c.startMs;
        return (
          <span key={i} style={{ color: spoken ? C.text : C.faint, transition: "none" }}>
            {c.text}
          </span>
        );
      })}
    </div>
  );
};
