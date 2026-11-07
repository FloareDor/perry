/**
 * The script. Each scene is one spoken line and one visual.
 *
 * Voiceover is generated per scene into public/voiceover/launch/<id>.mp3
 * and the composition takes its length from those files, so the picture
 * never drifts from the voice.
 */
export interface Scene {
  id: string;
  narration: string;
}

export const COMPOSITION_ID = "launch";

export const SCENES: Scene[] = [
  {
    id: "01-hook",
    narration:
      "Every coding agent starts the same way. It clones the repository. All of it. Every time.",
  },
  {
    id: "02-problem",
    narration:
      "Run twenty agents and that is twenty clones, twenty working trees, and twenty pushes fighting over one lock.",
  },
  {
    id: "03-name",
    narration: "Perry is a git server built for machines.",
  },
  {
    id: "04-native",
    narration:
      "It speaks native git. Clone, push, fetch. The same packfiles, the same smart HTTP, no wrapper around the git binary.",
  },
  {
    id: "05-api",
    narration:
      "But agents don't need a clone. They grep, and they read one file at one revision. Perry serves that over HTTP in about a hundred milliseconds, four hundred times fewer bytes than a clone.",
  },
  {
    id: "06-fleet",
    narration:
      "Here are twenty agents on twenty branches of one repository, forking, editing, and pushing at the same time.",
  },
  {
    id: "07-numbers",
    narration:
      "Twenty concurrent pushes land in under a second. A fork is a pointer, not a copy. And a compare-and-swap on every reference means no lost updates.",
  },
  {
    id: "08-open",
    narration:
      "It's open source and ships as one binary. SQLite on your laptop, the same store in production.",
  },
  {
    id: "09-cta",
    narration: "Point your agents at Perry. The code is on GitHub, at perry-dev slash perry.",
  },
];

// Spoken English runs close to 2.6 words a second. Scenes are sized from
// this only until the voiceover has been generated.
export function estimateSeconds(narration: string): number {
  const words = narration.trim().split(/\s+/).length;
  return Math.max(3, words / 2.6 + 0.8);
}
