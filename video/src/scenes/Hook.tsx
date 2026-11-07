import { interpolate, useCurrentFrame } from "remotion";
import { Frame } from "../components/Layout.tsx";
import { Terminal } from "../components/Terminal.tsx";
import { Headline } from "../components/Text.tsx";
import { C } from "../theme.ts";

// A clone of a 400 file repository, as measured on the benchmark machine.
const TOTAL_OBJECTS = 1078;
const TOTAL_MB = 7.97;
const SECONDS = 1.31;

export const Hook = () => {
  const frame = useCurrentFrame();
  const p = interpolate(frame, [20, 150], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  const objects = Math.floor(p * TOTAL_OBJECTS);
  const mb = (p * TOTAL_MB).toFixed(2);
  const bar = "#".repeat(Math.floor(p * 40)).padEnd(40, " ");
  const done = p >= 1;

  return (
    <Frame>
      <Headline size={72}>Every agent starts by cloning the whole repository.</Headline>
      <Terminal title="agent-01" width={1680} height={300} style={{ marginTop: 56 }}>
        <span style={{ color: C.dim }}>$ </span>git clone https://forge.example/acme/app.git{"\n"}
        Cloning into 'app'...{"\n"}
        <span style={{ color: C.dim }}>
          Receiving objects: {String(Math.floor(p * 100)).padStart(3)}% ({objects}/{TOTAL_OBJECTS}), {mb} MiB  [{bar}]
        </span>
        {"\n"}
        {done ? (
          <span style={{ color: C.amber }}>
            done in {SECONDS}s, {TOTAL_MB} MB, for one task that touches one file
          </span>
        ) : (
          " "
        )}
      </Terminal>
    </Frame>
  );
};
