import { useCurrentFrame } from "remotion";
import { Frame } from "../components/Layout.tsx";
import { Terminal, visibleLines } from "../components/Terminal.tsx";
import { Headline, Sub } from "../components/Text.tsx";
import { C } from "../theme.ts";

const LINES: Array<[string, string]> = [
  ["$ go install github.com/perry-dev/perry/cmd/perry@latest", C.text],
  ["$ perry serve -data ./perry.db", C.text],
  ["perry: listening on :8080", C.dim],
  ["perry: repositories in ./perry.db (sqlite, wal)", C.dim],
  ["", C.dim],
  ["$ git remote add perry http://localhost:8080/app.git", C.text],
  ["$ git push perry main", C.text],
  [" * [new branch]      main -> main", C.accent],
];

export const Open = () => {
  const frame = useCurrentFrame();
  const lines = visibleLines(LINES, frame, 25, 16);
  return (
    <Frame>
      <Headline size={72}>Open source. One binary.</Headline>
      <Sub>Pure Go, no cgo. SQLite on a laptop is the same store you run in production.</Sub>
      <Terminal title="" width={1680} height={420} style={{ marginTop: 56 }}>
        {lines.map(([t, c], i) => (
          <span key={i} style={{ color: c }}>
            {t}
            {"\n"}
          </span>
        ))}
      </Terminal>
    </Frame>
  );
};
