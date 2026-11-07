import React from "react";
import { Composition } from "remotion";
import { Perry } from "./Perry.tsx";
import { BAR, DURATION, FPS } from "./beat.ts";
import { TIMELINE } from "./timeline.ts";

const last = TIMELINE[TIMELINE.length - 1];
const timelineFrames = (last.at + last.length) * BAR;

if (timelineFrames !== DURATION) {
  throw new Error(
    `timeline is ${timelineFrames} frames but the music is ${DURATION}; ` +
      "keep src/timeline.ts and scripts/music.py on the same number of bars",
  );
}

export const Root: React.FC = () => (
  <Composition
    id="Launch"
    component={Perry}
    width={1920}
    height={1080}
    fps={FPS}
    durationInFrames={DURATION}
  />
);
