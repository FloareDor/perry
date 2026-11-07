# Launch video

A 60 second launch video for perry, built with Remotion. No narration: the
music carries the edit and the scenes are pictures rather than slides.

## The grid

Everything is cut to one tempo.

```
100 BPM  ->  beat 0.6 s  ->  18 frames at 30 fps
             bar  2.4 s  ->  72 frames
             25 bars     ->  1800 frames, 60.0 s exactly
```

Those numbers live in two places and have to agree: `src/beat.ts` for the
picture and `scripts/music.py` for the track. `src/Root.tsx` throws at
composition time if the timeline and the music disagree on the bar count.

Scenes start and end on a downbeat, elements land on beats, and the four
cuts that need to hit hardest sit on the bars where the arrangement drops
an impact: 6 (the name), 11 (the API), 15 (the fleet), 23 (the end card).

## The music

`scripts/music.py` synthesises the bed with numpy and encodes it with
ffmpeg, so there is nothing to license and the tempo is known exactly. It
is a kick, a sub, a dark pad on an A minor progression, a tick and an arp,
arranged section by section against the same bar numbers the edit uses.
The master sits at about -17 LUFS, which is quiet on purpose.

```
npm run music            # rewrite public/music/bed.mp3
```

Needs Python 3 with numpy, and ffmpeg on PATH.

## The picture

- `src/timeline.ts` is the edit: ten scenes, each with a start bar and a
  length in bars.
- `src/scenes/*.tsx` is one component per scene. Every figure on screen is
  from the benchmark section of the main README.
- `src/components/Primitives.tsx` holds the shared pieces, and
  `src/beat.ts` the helpers that turn a frame number into a beat.

## Commands

```
npm install
npm run music            # regenerate the bed
npm run dev              # open Remotion Studio
npm run render           # write out/launch.mp4
npm run typecheck
```
