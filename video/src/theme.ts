import { loadFont as loadMono } from "@remotion/google-fonts/JetBrainsMono";
import { loadFont as loadSans } from "@remotion/google-fonts/Inter";

const mono = loadMono("normal", { weights: ["400", "500", "700"], subsets: ["latin"] });
const sans = loadSans("normal", { weights: ["400", "500", "600", "700"], subsets: ["latin"] });

export const FONT_MONO = mono.fontFamily;
export const FONT_SANS = sans.fontFamily;

// Near-black, one green, two warning colours. Everything else is a grey.
// The palette stays out of the way because the shapes are doing the work.
export const C = {
  bg: "#07090b",
  panel: "#0e1216",
  panelHi: "#141a20",
  line: "#1b222a",
  text: "#f0f4f3",
  dim: "#7b8793",
  faint: "#38424d",
  accent: "#22c55e",
  accentDim: "#16a34a",
  accentGlow: "rgba(34, 197, 94, 0.35)",
  warn: "#f87171",
  amber: "#fbbf24",
};
