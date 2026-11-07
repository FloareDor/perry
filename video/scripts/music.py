"""Synthesise the music bed for the launch video.

The video has no voiceover, so the music carries the edit: every scene cut
lands on a downbeat. That only works if the track has a tempo the composition
knows about, so the track is generated here rather than licensed, and the
constants below are mirrored in src/beat.ts.

    100 BPM -> beat 0.6 s -> bar 2.4 s -> 25 bars -> 60.0 s

Nothing is loud and nothing is bright. The mix sits low and leaves the
numbers on screen to do the talking.

Run:  python scripts/music.py
"""

import math
import subprocess
import sys
import wave
from pathlib import Path

import numpy as np

ROOT = Path(__file__).resolve().parent.parent
OUT_DIR = ROOT / "public" / "music"

SR = 44100
BPM = 100
BEAT = 60.0 / BPM
BAR = 4 * BEAT
BARS = 25
DURATION = BARS * BAR

rng = np.random.default_rng(7)


# --- helpers ---------------------------------------------------------------

def n(seconds):
    return int(round(seconds * SR))


def t_axis(seconds):
    return np.arange(n(seconds)) / SR


def add(buf, at, sig, gain=1.0):
    """Mix sig into buf starting at `at` seconds, clipped to the buffer."""
    i = n(at)
    if i >= len(buf):
        return
    j = min(len(buf), i + len(sig))
    buf[i:j] += sig[: j - i] * gain


def lowpass(x, cutoff, order=2.0):
    """Butterworth-shaped taper applied in the frequency domain. Crude, but
    the material never goes near the top of the spectrum anyway."""
    spec = np.fft.rfft(x)
    freqs = np.fft.rfftfreq(len(x), 1.0 / SR)
    mag = 1.0 / np.sqrt(1.0 + (freqs / max(cutoff, 1.0)) ** (2 * order))
    return np.fft.irfft(spec * mag, n=len(x))


def highpass(x, cutoff, order=2.0):
    spec = np.fft.rfft(x)
    freqs = np.fft.rfftfreq(len(x), 1.0 / SR)
    r = (freqs / max(cutoff, 1.0)) ** (2 * order)
    mag = np.sqrt(r / (1.0 + r))
    return np.fft.irfft(spec * mag, n=len(x))


def note(semitones_from_a2):
    """A2 is 110 Hz and the root of the whole track."""
    return 110.0 * (2.0 ** (semitones_from_a2 / 12.0))


# --- voices ----------------------------------------------------------------

def kick():
    t = t_axis(0.46)
    pitch = 44.0 + 90.0 * np.exp(-t * 30.0)
    phase = 2 * np.pi * np.cumsum(pitch) / SR
    body = np.sin(phase) * np.exp(-t / 0.13)
    click = rng.normal(0, 1, len(t)) * np.exp(-t / 0.004) * 0.25
    return lowpass(body + click, 220.0) * 0.9


def sub(freq, length):
    t = t_axis(length)
    s = np.sin(2 * np.pi * freq * t) + 0.3 * np.sin(4 * np.pi * freq * t)
    env = np.ones_like(t)
    a, r = n(0.02), n(0.08)
    env[:a] = np.linspace(0, 1, a)
    env[-r:] = np.linspace(1, 0, r)
    return s * env * 0.5


def pad(freqs, length, cutoff):
    """A stack of slightly detuned sines, filtered dark, with a slow swell."""
    t = t_axis(length)
    out = np.zeros_like(t)
    for i, f in enumerate(freqs):
        for d in (-0.14, 0.0, 0.11):
            out += np.sin(2 * np.pi * (f + d) * t + i * 1.7) / (1.6 + i * 0.7)
        out += 0.18 * np.sin(2 * np.pi * f * 2 * t + i) / (1.6 + i * 0.7)
    out = lowpass(out, cutoff)
    env = np.ones_like(t)
    a, r = n(min(0.5, length * 0.3)), n(min(0.6, length * 0.35))
    env[:a] = np.linspace(0, 1, a) ** 2
    env[-r:] = np.linspace(1, 0, r) ** 1.5
    return out * env * 0.3


def tick(bright=1.0):
    t = t_axis(0.07)
    s = rng.normal(0, 1, len(t)) * np.exp(-t / 0.012)
    return highpass(s, 5200.0 * bright) * 0.22


def pluck(freq, length=0.42):
    t = t_axis(length)
    s = np.sin(2 * np.pi * freq * t) + 0.35 * np.sin(4 * np.pi * freq * t + 0.6)
    s += 0.12 * np.sin(6 * np.pi * freq * t)
    return lowpass(s * np.exp(-t / 0.11), 2800.0) * 0.3


def riser(length):
    t = t_axis(length)
    p = t / length
    noise = rng.normal(0, 1, len(t))
    swept = np.zeros_like(noise)
    # Sweep the cutoff by crossfading a few fixed filters. Cheaper than a
    # time-varying one and inaudible as a seam under a riser.
    cuts = [400, 900, 1800, 3600, 7000]
    for i, c in enumerate(cuts):
        w = np.clip(1.0 - abs(p * (len(cuts) - 1) - i), 0, 1)
        swept += highpass(lowpass(noise, c), 300.0) * w
    return swept * (p ** 2.2) * 0.16


def impact():
    t = t_axis(2.2)
    boom = np.sin(2 * np.pi * (52.0 - 14 * np.exp(-t * 2)) * t) * np.exp(-t / 0.55)
    air = highpass(rng.normal(0, 1, len(t)) * np.exp(-t / 0.22), 900.0) * 0.18
    return lowpass(boom, 320.0) * 0.8 + air


# --- arrangement -----------------------------------------------------------

# One chord per bar, cycling A minor - F - C - G.
PROGRESSION = [
    [note(0), note(3), note(7)],     # Am:  A2 C3 E3
    [note(-4), note(0), note(3)],    # F:   F2 A2 C3
    [note(3), note(7), note(10)],    # C:   C3 E3 G3
    [note(-2), note(2), note(5)],    # G:   G2 B2 D3
]

ARP = [note(12), note(19), note(15), note(22), note(19), note(15)]

# Each bar declares how busy it is. The scene cuts in src/timeline.ts line up
# with the section boundaries here.
#   kick: none | half (beats 1 and 3) | four
#   hats: 0 none, 1 eighths, 2 sixteenths
SECTIONS = [
    # bars 0-1   cold open: one clone, nothing else
    dict(bars=(0, 2), kick="none", hats=0, sub=False, arp=False, cut=600),
    # bars 2-4   the pile: twenty clones stack up
    dict(bars=(2, 5), kick="half", hats=1, sub=True, arp=False, cut=900),
    # bar 5      collapse: everything drops out under a riser
    dict(bars=(5, 6), kick="none", hats=0, sub=False, arp=False, cut=400),
    # bars 6-7   the name
    dict(bars=(6, 8), kick="half", hats=0, sub=True, arp=False, cut=1400),
    # bars 8-10  native git
    dict(bars=(8, 11), kick="half", hats=1, sub=True, arp=False, cut=1800),
    # bars 11-14 the api race
    dict(bars=(11, 15), kick="four", hats=2, sub=True, arp=True, cut=2400),
    # bars 15-18 the fleet, peak
    dict(bars=(15, 19), kick="four", hats=2, sub=True, arp=True, cut=3200),
    # bars 19-20 packfile
    dict(bars=(19, 21), kick="half", hats=1, sub=True, arp=False, cut=1600),
    # bars 21-22 the numbers
    dict(bars=(21, 23), kick="four", hats=1, sub=True, arp=True, cut=2600),
    # bars 23-24 end card
    dict(bars=(23, 25), kick="none", hats=0, sub=True, arp=False, cut=900),
]

# Bars that open with a low impact, each one preceded by a half-bar riser.
IMPACT_BARS = [6, 11, 15, 23]


def section_for(bar):
    for s in SECTIONS:
        if s["bars"][0] <= bar < s["bars"][1]:
            return s
    return SECTIONS[-1]


def build():
    buf = np.zeros(n(DURATION + 2.5))
    k = kick()

    for bar in range(BARS):
        s = section_for(bar)
        bar_t = bar * BAR
        freqs = PROGRESSION[bar % len(PROGRESSION)]

        # Pad: one sustained chord per bar, overlapping into the next.
        add(buf, bar_t, pad(freqs, BAR * 1.25, s["cut"]), 0.85)

        # Sub: follows the root, ducked under each kick.
        if s["sub"]:
            voice = sub(freqs[0] / 2.0, BAR)
            tt = np.arange(len(voice)) / SR
            duck = 1.0 - 0.6 * np.exp(-((tt % BEAT) / 0.09))
            add(buf, bar_t, voice * duck, 0.55)

        for beat in range(4):
            bt = bar_t + beat * BEAT

            if s["kick"] == "four" or (s["kick"] == "half" and beat in (0, 2)):
                add(buf, bt, k, 0.62 if beat == 0 else 0.5)

            if s["hats"] >= 1:
                add(buf, bt + BEAT / 2, tick(), 0.5)
            if s["hats"] >= 2:
                add(buf, bt + BEAT / 4, tick(1.25), 0.26)
                add(buf, bt + 3 * BEAT / 4, tick(1.25), 0.26)

            if s["arp"]:
                step = (bar * 4 + beat) % len(ARP)
                add(buf, bt, pluck(ARP[step]), 0.4)
                add(buf, bt + BEAT / 2, pluck(ARP[(step + 2) % len(ARP)]), 0.22)

        if bar in IMPACT_BARS:
            add(buf, bar_t, impact(), 0.9)

        if bar + 1 in IMPACT_BARS:
            add(buf, bar_t + BAR * 0.5, riser(BAR * 0.5), 1.0)

    # Fade the last bar out so the end card lands on silence.
    tail = n(BAR * 0.9)
    buf[n(DURATION) - tail : n(DURATION)] *= np.linspace(1, 0, tail) ** 1.6
    buf[n(DURATION) :] = 0.0

    # Soft-knee limiter, then set the level low. This plays under a video
    # nobody asked to be loud.
    buf = np.tanh(buf * 0.8) / 0.8
    peak = float(np.max(np.abs(buf))) or 1.0
    return buf[: n(DURATION)] / peak * 0.52


def write_wav(path, pcm):
    with wave.open(str(path), "wb") as w:
        w.setnchannels(pcm.shape[1])
        w.setsampwidth(2)
        w.setframerate(SR)
        w.writeframes(pcm.tobytes())


def main():
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    mono = build()

    # A touch of width: delay one side by a few milliseconds.
    d = n(0.009)
    stereo = np.stack([mono, np.concatenate([np.zeros(d), mono[:-d]])], axis=1)

    wav = OUT_DIR / "bed.wav"
    write_wav(wav, (np.clip(stereo, -1, 1) * 32767).astype("<i2"))

    mp3 = OUT_DIR / "bed.mp3"
    subprocess.run(
        ["ffmpeg", "-y", "-loglevel", "error", "-i", str(wav),
         "-codec:a", "libmp3lame", "-b:a", "192k", str(mp3)],
        check=True,
    )
    wav.unlink()
    print("wrote %s (%.1fs, %d BPM, %d bars)" % (mp3, DURATION, BPM, BARS))
    return 0


if __name__ == "__main__":
    sys.exit(main())
