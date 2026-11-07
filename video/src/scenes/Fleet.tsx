import { useCurrentFrame } from "remotion";
import { Frame, Row } from "../components/Layout.tsx";
import { Terminal, visibleLines } from "../components/Terminal.tsx";
import { Headline } from "../components/Text.tsx";
import { C, FONT_MONO } from "../theme.ts";

const AGENTS = 20;

// Each agent: fork, grep, read, commit. Stagger so the grid fills over ~3 s.
const STAGES = ["fork", "grep", "read", "commit", "pushed"];

const LOG = [
  "fleet: 20 tasks, 20 at a time",
  "  t-01  fork agent/t-01 -> 3b73ec5",
  "  t-07  grep 'func Resolve'  2 matches",
  "  t-13  commit 9f1c0aa  appended a note",
  "  t-02  commit 6a0d41e  renamed helper",
  "  t-19  commit c41b9e0  fixed typo",
  "  merge t-01 -> main  fast-forward",
  "  merge t-07 -> main  replayed",
  "20/20 succeeded, 20 merged, 0 clones",
];

export const Fleet = () => {
  const frame = useCurrentFrame();
  const log = visibleLines(LOG, frame, 20, 18);

  return (
    <Frame>
      <Headline size={72}>Twenty agents, twenty branches, one repository.</Headline>
      <Row gap={48} style={{ marginTop: 48, alignItems: "flex-start" }}>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(5, 1fr)", gap: 14, width: 860 }}>
          {Array.from({ length: AGENTS }, (_, i) => {
            const start = 15 + (i % 7) * 6 + Math.floor(i / 7) * 10;
            const stage = Math.min(STAGES.length - 1, Math.max(-1, Math.floor((frame - start) / 22)));
            const done = stage === STAGES.length - 1;
            return (
              <div
                key={i}
                style={{
                  height: 88,
                  borderRadius: 8,
                  border: `1px solid ${done ? C.accent : C.line}`,
                  background: C.panel,
                  opacity: stage < 0 ? 0.25 : 1,
                  padding: "0 16px",
                  display: "flex",
                  flexDirection: "column",
                  justifyContent: "center",
                  fontFamily: FONT_MONO,
                  fontSize: 20,
                  color: done ? C.accent : C.dim,
                }}
              >
                <span style={{ color: C.text }}>t-{String(i + 1).padStart(2, "0")}</span>
                <span>{stage < 0 ? "queued" : STAGES[stage]}</span>
              </div>
            );
          })}
        </div>
        <Terminal title="coordinator" width={760} height={470} style={{ fontSize: 23 }}>
          {log.map((l, i) => (
            <span key={i} style={{ color: i === LOG.length - 1 ? C.accent : i === 0 ? C.text : C.dim }}>
              {l}
              {"\n"}
            </span>
          ))}
        </Terminal>
      </Row>
    </Frame>
  );
};
