import { useCurrentFrame } from "remotion";
import { Frame, Row } from "../components/Layout.tsx";
import { Headline, countUp } from "../components/Text.tsx";
import { C, FONT_MONO, FONT_SANS } from "../theme.ts";

const Tile: React.FC<{ value: string; label: string; detail: string; delay: number }> = ({ value, label, detail, delay }) => {
  const frame = useCurrentFrame();
  const o = Math.min(1, Math.max(0, (frame - delay) / 12));
  return (
    <div
      style={{
        flex: 1,
        background: C.panel,
        border: `1px solid ${C.line}`,
        borderRadius: 14,
        padding: "44px 36px",
        opacity: o,
        transform: `translateY(${(1 - o) * 20}px)`,
      }}
    >
      <div style={{ fontFamily: FONT_MONO, fontSize: 96, fontWeight: 600, color: C.accent, lineHeight: 1 }}>{value}</div>
      <div style={{ fontFamily: FONT_SANS, fontSize: 28, color: C.text, marginTop: 20 }}>{label}</div>
      <div style={{ fontFamily: FONT_SANS, fontSize: 21, color: C.dim, marginTop: 10 }}>{detail}</div>
    </div>
  );
};

export const Numbers = () => {
  const frame = useCurrentFrame();
  const pushes = Math.round(countUp(frame, 0, 20, 15, 45));
  const forkUs = Math.round(countUp(frame, 900, 171, 105, 45));
  const lost = Math.round(countUp(frame, 9, 0, 195, 30));

  return (
    <Frame>
      <Headline size={72}>Measured, not promised.</Headline>
      <Row gap={32} style={{ marginTop: 56, alignItems: "stretch" }}>
        <Tile value={`${pushes}/20`} label="concurrent pushes in 709 ms" detail="real git clients, SQLite backend, every ref landed" delay={10} />
        <Tile value={`${forkUs} µs`} label="to fork a branch" detail="one reference write, zero objects copied" delay={100} />
        <Tile value={`${lost}`} label="lost updates" detail="compare-and-swap on every ref, non-fast-forward refused" delay={190} />
      </Row>
    </Frame>
  );
};
