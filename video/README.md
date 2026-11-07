# Launch video

A 65 second launch video for perry, built with Remotion. Nine scenes, one
spoken line each, with the picture timed to the voice.

## How it is put together

- `src/scenes.ts` is the script: one id and one narration line per scene.
- `scripts/voiceover.py` reads that script and writes one MP3 per scene to
  `public/voiceover/launch/`, plus a word-timing JSON per scene to
  `public/captions/launch/` for the lower-third captions. The voice is a
  neural voice pitched down and slowed a little.
- `public/voiceover/bed.mp3` is a low drone synthesized with ffmpeg (three
  sines under 160 Hz, mean level -27 dB) that plays under the narration at
  a third of its volume.
- `src/Root.tsx` measures each MP3 at composition time and sizes every
  scene to its line plus a short pause, so the total length and every cut
  follow the audio. If a scene's MP3 is missing, its length is estimated
  from the word count and the scene plays silent.
- `src/scenes/*.tsx` is one component per scene. The numbers shown are
  the ones in the main README's benchmark section.

## Commands

```
npm install
npm run voiceover        # regenerate the narration after editing scenes.ts
npm run dev              # open Remotion Studio
npm run render           # write out/launch.mp4
```

The voiceover script needs Python 3 and `pip install edge-tts`.
