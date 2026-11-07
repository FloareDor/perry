import React from "react";
import { C, FONT_MONO } from "../theme.ts";
import { Bar } from "./Primitives.tsx";

export const CARD_W = 300;
export const CARD_H = 124;

/**
 * One agent's clone, as a small card with a bar that never finishes. Used
 * for a single card in the cold open and for all twenty in the pile, so the
 * second scene reads as more of the same thing rather than a new idea.
 */
export const CloneCard: React.FC<{
  index: number;
  progress: number;
  width?: number;
  height?: number;
  style?: React.CSSProperties;
}> = ({ index, progress, width = CARD_W, height = CARD_H, style }) => {
  const mb = (7.97 * progress).toFixed(2);
  return (
    <div
      style={{
        width,
        height,
        background: C.panel,
        border: `1px solid ${C.line}`,
        borderRadius: 10,
        padding: "18px 20px",
        display: "flex",
        flexDirection: "column",
        justifyContent: "space-between",
        fontFamily: FONT_MONO,
        ...style,
      }}
    >
      <div style={{ display: "flex", justifyContent: "space-between", fontSize: 20 }}>
        <span style={{ color: C.dim }}>agent-{String(index + 1).padStart(2, "0")}</span>
        <span style={{ color: C.faint }}>git clone</span>
      </div>
      <Bar value={progress} width={width - 40} height={8} color={C.faint} track="#11161b" />
      <div style={{ fontSize: 19, color: C.faint }}>
        {mb} / 7.97 MB
      </div>
    </div>
  );
};

/** Deterministic jitter so the twenty bars do not crawl in lockstep. */
export function cardRate(index: number): number {
  return 0.72 + ((index * 37) % 11) / 18;
}
