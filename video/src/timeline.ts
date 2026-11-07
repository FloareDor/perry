/**
 * The edit, in bars.
 *
 * Every scene starts and ends on a downbeat, and the boundaries here are the
 * same ones the arrangement in scripts/music.py changes section on. Bars 6,
 * 11, 15 and 23 open on a low impact, so the four cuts that need to hit
 * hardest (the name, the API, the fleet, the end card) are placed there.
 */
export interface Beat {
  id: string;
  /** Bar the scene starts on, and how many bars it holds. */
  at: number;
  length: number;
}

export const TIMELINE: Beat[] = [
  { id: "clone", at: 0, length: 2 },     // one agent, one clone, crawling
  { id: "pile", at: 2, length: 3 },      // twenty of them
  { id: "collapse", at: 5, length: 1 },  // the pile implodes
  { id: "name", at: 6, length: 2 },      // impact
  { id: "native", at: 8, length: 3 },    // it is still real git
  { id: "api", at: 11, length: 4 },      // impact: the race
  { id: "fleet", at: 15, length: 4 },    // impact: twenty branches
  { id: "pack", at: 19, length: 2 },     // smaller than git's own packs
  { id: "numbers", at: 21, length: 2 },  // the four figures
  { id: "end", at: 23, length: 2 },      // impact: the card
];
