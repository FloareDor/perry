"""Generate the per-scene voiceover and word captions.

Reads the script from src/scenes.ts, speaks each line with a neural voice
pitched low and slightly slower than default, and writes

    public/voiceover/launch/<id>.mp3
    public/captions/launch/<id>.json   (word timings, Remotion Caption[])

Run:  python scripts/voiceover.py
"""

import asyncio
import json
import re
import sys
from pathlib import Path

import edge_tts

ROOT = Path(__file__).resolve().parent.parent
VOICE = "en-US-AndrewNeural"
RATE = "-6%"
PITCH = "-8Hz"


def read_scenes():
    src = (ROOT / "src" / "scenes.ts").read_text(encoding="utf-8")
    pairs = re.findall(r'id:\s*"([^"]+)",\s*narration:\s*"([^"]*)"', src, re.S)
    return pairs


async def speak(scene_id, text, out_audio, out_caps):
    tts = edge_tts.Communicate(text, VOICE, rate=RATE, pitch=PITCH, boundary="WordBoundary")
    # Word boundaries come back without punctuation. When they line up one
    # to one with the script's tokens, show the script's spelling instead.
    tokens = text.split()
    words = []
    with open(out_audio, "wb") as f:
        async for chunk in tts.stream():
            if chunk["type"] == "audio":
                f.write(chunk["data"])
            elif chunk["type"] == "WordBoundary":
                start = chunk["offset"] / 10_000
                end = start + chunk["duration"] / 10_000
                shown = tokens[len(words)] if len(tokens) > len(words) else chunk["text"]
                words.append({
                    "text": (" " if words else "") + shown,
                    "startMs": round(start),
                    "endMs": round(end),
                    "timestampMs": round((start + end) / 2),
                    "confidence": 1,
                })
    if len(words) != len(tokens):
        print(f"  {scene_id}: {len(words)} boundaries vs {len(tokens)} tokens, captions use spoken words")
    out_caps.write_text(json.dumps(words, indent=1), encoding="utf-8")
    print(f"  {scene_id:12s} {(words[-1]['endMs'] / 1000 if words else 0):5.1f}s  {len(words)} words")


async def main():
    audio_dir = ROOT / "public" / "voiceover" / "launch"
    caps_dir = ROOT / "public" / "captions" / "launch"
    audio_dir.mkdir(parents=True, exist_ok=True)
    caps_dir.mkdir(parents=True, exist_ok=True)
    scenes = read_scenes()
    if not scenes:
        sys.exit("no scenes found in src/scenes.ts")
    print(f"voice {VOICE}, rate {RATE}, pitch {PITCH}")
    for scene_id, text in scenes:
        await speak(scene_id, text, audio_dir / f"{scene_id}.mp3", caps_dir / f"{scene_id}.json")


asyncio.run(main())
