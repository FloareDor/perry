import { loadFont as loadMono } from "@remotion/google-fonts/JetBrainsMono";
import { loadFont as loadSans } from "@remotion/google-fonts/Inter";

const mono = loadMono("normal", { weights: ["400", "600"], subsets: ["latin"] });
const sans = loadSans("normal", { weights: ["400", "500", "600"], subsets: ["latin"] });

export const FONT_MONO = mono.fontFamily;
export const FONT_SANS = sans.fontFamily;

// One dark background, one accent. The numbers do the talking, so the
// palette stays out of the way.
export const C = {
  bg: "#0b0d10",
  panel: "#13161b",
  line: "#242933",
  text: "#e8eaed",
  dim: "#8b93a1",
  faint: "#4a5160",
  accent: "#5ee0a0",
  warn: "#ff6b6b",
  amber: "#f4c45a",
};
