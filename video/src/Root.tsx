import { CalculateMetadataFunction, Composition, staticFile } from "remotion";
import type { Caption } from "@remotion/captions";
import { Launch, type LaunchProps, type SceneTiming } from "./Launch.tsx";
import { COMPOSITION_ID, SCENES, estimateSeconds } from "./scenes.ts";
import { getAudioDuration } from "./get-audio-duration.ts";

export const FPS = 30;

// A short breath after each line, so scenes do not cut on the last syllable.
const TAIL_SECONDS = 0.6;

async function exists(url: string): Promise<boolean> {
  try {
    const res = await fetch(url, { method: "HEAD" });
    return res.ok;
  } catch {
    return false;
  }
}

async function loadCaptions(url: string): Promise<Caption[] | null> {
  try {
    const res = await fetch(url);
    if (!res.ok) return null;
    return (await res.json()) as Caption[];
  } catch {
    return null;
  }
}

const calculateMetadata: CalculateMetadataFunction<LaunchProps> = async () => {
  const timings: SceneTiming[] = [];
  let from = 0;

  for (const scene of SCENES) {
    const audioPath = `voiceover/${COMPOSITION_ID}/${scene.id}.mp3`;
    const audioUrl = staticFile(audioPath);
    const hasAudio = await exists(audioUrl);

    const seconds = hasAudio
      ? await getAudioDuration(audioUrl)
      : estimateSeconds(scene.narration);
    const durationInFrames = Math.ceil((seconds + TAIL_SECONDS) * FPS);

    const captions = hasAudio
      ? await loadCaptions(staticFile(`captions/${COMPOSITION_ID}/${scene.id}.json`))
      : null;

    timings.push({
      id: scene.id,
      from,
      durationInFrames,
      audio: hasAudio ? audioPath : null,
      captions,
    });
    from += durationInFrames;
  }

  return {
    durationInFrames: from,
    props: { timings },
  };
};

export const Root = () => (
  <Composition
    id="Launch"
    component={Launch}
    width={1920}
    height={1080}
    fps={FPS}
    durationInFrames={FPS * 60}
    defaultProps={{ timings: [] }}
    calculateMetadata={calculateMetadata}
  />
);
