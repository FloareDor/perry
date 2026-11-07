/**
 * The grid the whole video is cut on.
 *
 * These mirror scripts/music.py. 100 BPM at 30 fps puts a beat on exactly 18
 * frames and a bar on 72, so every cut and every animation lands on the
 * music without anything drifting over a minute.
 */
import { interpolate, spring } from "remotion";

export const FPS = 30;
export const BPM = 100;
export const BEAT = 18;
export const BAR = 72;
export const BARS = 25;
export const DURATION = BAR * BARS;

export const bars = (count: number) => Math.round(count * BAR);
export const beats = (count: number) => Math.round(count * BEAT);

/**
 * A short swell on every beat. Used sparingly, on one element per scene, so
 * the picture breathes with the track instead of twitching.
 */
export function pulse(frame: number, amount = 0.04, tau = 5, period = BEAT) {
  const since = ((frame % period) + period) % period;
  return 1 + amount * Math.exp(-since / tau);
}

/** A settled spring that starts at `at`. Everything enters with this. */
export function pop(frame: number, at: number, stiffness = 140) {
  return spring({
    frame: frame - at,
    fps: FPS,
    config: { damping: 200, stiffness },
  });
}

/** Linear 0..1 ramp over `length` frames starting at `at`, clamped. */
export function ramp(frame: number, at: number, length: number) {
  return interpolate(frame, [at, at + length], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });
}

/** Ease-out ramp, for counters and bars that should decelerate. */
export function ease(frame: number, at: number, length: number, power = 3) {
  return 1 - Math.pow(1 - ramp(frame, at, length), power);
}

/** How many items should be on screen if one lands every `every` frames. */
export function landed(frame: number, at: number, every: number, max: number) {
  if (frame < at) return 0;
  return Math.min(max, Math.floor((frame - at) / every) + 1);
}
