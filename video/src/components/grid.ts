import { CARD_H, CARD_W } from "./CloneCard.tsx";

export const COLS = 5;
export const ROWS = 4;
export const COUNT = COLS * ROWS;
const GAP_X = 28;
const GAP_Y = 26;

const TOTAL_W = COLS * CARD_W + (COLS - 1) * GAP_X;
const TOTAL_H = ROWS * CARD_H + (ROWS - 1) * GAP_Y;

export const ORIGIN_X = (1920 - TOTAL_W) / 2;
export const ORIGIN_Y = (1080 - TOTAL_H) / 2;

export const CENTER_X = 1920 / 2;
export const CENTER_Y = 1080 / 2;

/** Top-left corner of card `i`, laid out row by row. */
export function cardPos(i: number): { x: number; y: number } {
  return {
    x: ORIGIN_X + (i % COLS) * (CARD_W + GAP_X),
    y: ORIGIN_Y + Math.floor(i / COLS) * (CARD_H + GAP_Y),
  };
}
