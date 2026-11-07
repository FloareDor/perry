import { AbsoluteFill, interpolate, useCurrentFrame } from "remotion";
import { Headline } from "../components/Text.tsx";
import { C, FONT_MONO, FONT_SANS } from "../theme.ts";

export const Name = () => {
  const frame = useCurrentFrame();
  const o = interpolate(frame, [18, 32], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  return (
    <AbsoluteFill style={{ alignItems: "center", justifyContent: "center" }}>
      <div style={{ fontFamily: FONT_MONO, fontSize: 40, color: C.accent, letterSpacing: 6, marginBottom: 24 }}>perry</div>
      <Headline size={128} style={{ textAlign: "center" }}>
        git, for fleets.
      </Headline>
      <div style={{ fontFamily: FONT_SANS, fontSize: 36, color: C.dim, marginTop: 32, opacity: o }}>
        a git server built for machines
      </div>
    </AbsoluteFill>
  );
};
