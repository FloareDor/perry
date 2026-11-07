import { useCurrentFrame } from "remotion";
import { Frame } from "../components/Layout.tsx";
import { Headline, Sub } from "../components/Text.tsx";
import { C, FONT_MONO } from "../theme.ts";

const AGENTS = 20;

/** Twenty agents; each one starts a clone, then most of them queue. */
export const Problem = () => {
  const frame = useCurrentFrame();

  return (
    <Frame>
      <Headline size={72}>Twenty agents. Twenty clones. One lock.</Headline>
      <Sub>Every working tree is a full copy. Every push waits on the one before it.</Sub>
      <div style={{ display: "grid", gridTemplateColumns: "repeat(10, 1fr)", gap: 16, marginTop: 64 }}>
        {Array.from({ length: AGENTS }, (_, i) => {
          const appear = 10 + i * 4;
          const stalled = frame > appear + 60 && i % 3 !== 0;
          const visible = frame >= appear;
          return (
            <div
              key={i}
              style={{
                height: 96,
                borderRadius: 8,
                border: `1px solid ${stalled ? C.warn : C.line}`,
                background: C.panel,
                opacity: visible ? 1 : 0,
                display: "flex",
                flexDirection: "column",
                justifyContent: "center",
                padding: "0 18px",
                fontFamily: FONT_MONO,
                fontSize: 20,
                color: stalled ? C.warn : C.dim,
              }}
            >
              <span style={{ color: C.text }}>agent-{String(i + 1).padStart(2, "0")}</span>
              <span>{stalled ? "waiting for lock" : "cloning..."}</span>
            </div>
          );
        })}
      </div>
    </Frame>
  );
};
