import { AbsoluteFill, Sequence, staticFile } from "remotion";
import { Audio } from "@remotion/media";
import type { Caption } from "@remotion/captions";
import { C } from "./theme.ts";
import { Subtitles } from "./components/Subtitles.tsx";
import { Hook } from "./scenes/Hook.tsx";
import { Problem } from "./scenes/Problem.tsx";
import { Name } from "./scenes/Name.tsx";
import { Native } from "./scenes/Native.tsx";
import { Api } from "./scenes/Api.tsx";
import { Fleet } from "./scenes/Fleet.tsx";
import { Numbers } from "./scenes/Numbers.tsx";
import { Open } from "./scenes/Open.tsx";
import { Cta } from "./scenes/Cta.tsx";

export interface SceneTiming {
  id: string;
  from: number;
  durationInFrames: number;
  /** Path under public/, or null when the voiceover has not been generated. */
  audio: string | null;
  captions: Caption[] | null;
}

export type LaunchProps = {
  timings: SceneTiming[];
};

const SCENE_COMPONENTS: Record<string, React.FC> = {
  "01-hook": Hook,
  "02-problem": Problem,
  "03-name": Name,
  "04-native": Native,
  "05-api": Api,
  "06-fleet": Fleet,
  "07-numbers": Numbers,
  "08-open": Open,
  "09-cta": Cta,
};

// Scenes whose text is already the whole screen.
const NO_CAPTIONS = new Set(["03-name", "09-cta"]);

export const Launch: React.FC<LaunchProps> = ({ timings }) => (
  <AbsoluteFill style={{ backgroundColor: C.bg }}>
    {/* A low drone, nothing above 160 Hz, well under the voice. */}
    <Audio src={staticFile("voiceover/bed.mp3")} volume={0.35} loop />
    {timings.map((t) => {
      const Scene = SCENE_COMPONENTS[t.id];
      if (!Scene) return null;
      return (
        <Sequence
          key={t.id}
          name={t.id}
          from={t.from}
          durationInFrames={t.durationInFrames}
          premountFor={15}
        >
          <Scene />
          {t.audio ? <Audio src={staticFile(t.audio)} /> : null}
          {t.captions && !NO_CAPTIONS.has(t.id) ? <Subtitles captions={t.captions} /> : null}
        </Sequence>
      );
    })}
  </AbsoluteFill>
);
