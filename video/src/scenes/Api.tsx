import { interpolate, useCurrentFrame } from "remotion";
import { Frame, Row } from "../components/Layout.tsx";
import { Terminal, visibleLines } from "../components/Terminal.tsx";
import { Headline, countUp } from "../components/Text.tsx";
import { C, FONT_MONO, FONT_SANS } from "../theme.ts";

const GREP: Array<[string, string]> = [
  ["$ perry grep 'func Resolve' app", C.text],
  ["internal/api/api.go:41: func Resolve(ctx context.Context,", C.dim],
  ["internal/api/api_test.go:88: func TestResolve(t *testing.T) {", C.dim],
  ["2 matches in 400 files, 118ms, 19.3 KB", C.accent],
];

const Stat: React.FC<{ label: string; value: string; color: string }> = ({ label, value, color }) => (
  <div style={{ fontFamily: FONT_MONO, marginTop: 20 }}>
    <div style={{ fontSize: 64, fontWeight: 600, color }}>{value}</div>
    <div style={{ fontSize: 24, color: C.dim, fontFamily: FONT_SANS }}>{label}</div>
  </div>
);

export const Api = () => {
  const frame = useCurrentFrame();
  const cloneP = interpolate(frame, [40, 260], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  const bar = "#".repeat(Math.floor(cloneP * 32)).padEnd(32, " ");
  const grep = visibleLines(GREP, frame, 40, 10);
  const ratio = countUp(frame, 0, 423, 130, 40);

  return (
    <Frame>
      <Headline size={72}>Agents don't need a clone. They grep.</Headline>
      <Row gap={48} style={{ marginTop: 48, alignItems: "flex-start" }}>
        <div>
          <Terminal title="clone" width={816} height={250}>
            <span style={{ color: C.dim }}>$ </span>git clone .../app.git{"\n"}
            <span style={{ color: C.dim }}>
              Receiving objects: {String(Math.floor(cloneP * 100)).padStart(3)}% [{bar}]
            </span>
          </Terminal>
          <Stat label="7.97 MB, 1078 objects" value="1.31 s" color={C.amber} />
        </div>
        <div>
          <Terminal title="perry api" width={816} height={250}>
            {grep.map(([t, c], i) => (
              <span key={i} style={{ color: c }}>
                {t}
                {"\n"}
              </span>
            ))}
          </Terminal>
          <Stat label={`19.3 KB, ${Math.round(ratio)}x fewer bytes than the clone`} value="118 ms" color={C.accent} />
        </div>
      </Row>
    </Frame>
  );
};
