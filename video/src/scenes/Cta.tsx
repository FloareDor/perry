import { AbsoluteFill, interpolate, useCurrentFrame } from "remotion";
import { Headline } from "../components/Text.tsx";
import { C, FONT_MONO, FONT_SANS } from "../theme.ts";

export const Cta = () => {
  const frame = useCurrentFrame();
  const o = interpolate(frame, [25, 40], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  const o2 = interpolate(frame, [55, 70], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  return (
    <AbsoluteFill style={{ alignItems: "center", justifyContent: "center" }}>
      <div style={{ fontFamily: FONT_MONO, fontSize: 36, color: C.accent, letterSpacing: 6, marginBottom: 28 }}>perry</div>
      <Headline size={96} style={{ textAlign: "center" }}>
        Point your agents at Perry.
      </Headline>
      <div style={{ fontFamily: FONT_MONO, fontSize: 52, color: C.text, marginTop: 56, opacity: o }}>
        github.com/perry-dev/perry
      </div>
      <div style={{ fontFamily: FONT_MONO, fontSize: 30, color: C.dim, marginTop: 24, opacity: o2 }}>
        go install github.com/perry-dev/perry/cmd/perry@latest
      </div>
      <div style={{ fontFamily: FONT_SANS, fontSize: 24, color: C.faint, marginTop: 72, opacity: o2 }}>
        MIT licensed. Go server, TypeScript fleet coordinator.
      </div>
    </AbsoluteFill>
  );
};
