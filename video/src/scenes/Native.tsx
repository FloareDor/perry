import { useCurrentFrame } from "remotion";
import { Frame, Row } from "../components/Layout.tsx";
import { Terminal, visibleLines } from "../components/Terminal.tsx";
import { FadeIn, Headline, Sub } from "../components/Text.tsx";
import { C, FONT_MONO } from "../theme.ts";

const LINES: Array<[string, string]> = [
  ["$ git push perry agent-07", C.text],
  ["Enumerating objects: 9, done.", C.dim],
  ["Compressing objects: 100% (5/5), done.", C.dim],
  ["Writing objects: 100% (5/5), 1.2 KiB, done.", C.dim],
  ["To http://perry:8080/app.git", C.dim],
  ["   3b73ec5..9f1c0aa  agent-07 -> agent-07", C.accent],
];

const STACK = [
  "git clone / push / fetch",
  "smart HTTP v1",
  "packfile reader and writer",
  "content-addressed store, CAS refs",
  "SQLite, WAL, one file",
];

export const Native = () => {
  const frame = useCurrentFrame();
  const lines = visibleLines(LINES, frame, 30, 14);
  return (
    <Frame>
      <Headline size={72}>It speaks native git.</Headline>
      <Sub>Smart HTTP, packfiles, deltas. Unmodified git clients. No forge, no wrapper around the git binary.</Sub>
      <Row gap={64} style={{ marginTop: 56, alignItems: "flex-start" }}>
        <Terminal title="agent-07" width={1040} height={360}>
          {lines.map(([text, color], i) => (
            <span key={i} style={{ color }}>
              {text}
              {"\n"}
            </span>
          ))}
        </Terminal>
        <FadeIn delay={70} style={{ fontFamily: FONT_MONO, fontSize: 26, color: C.dim, lineHeight: 1.6, paddingTop: 12 }}>
          {STACK.map((s, i) => (
            <div key={s}>
              <div style={{ color: i === 1 ? C.accent : i === 0 ? C.text : C.dim }}>{s}</div>
              {i < STACK.length - 1 ? <div style={{ paddingLeft: 40, color: C.faint }}>|</div> : null}
            </div>
          ))}
        </FadeIn>
      </Row>
    </Frame>
  );
};
