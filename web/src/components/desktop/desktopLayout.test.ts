import { describe, expect, it } from "vitest";
import { cellID, moveDesktopPositions, resolveDesktopPositions, type DesktopGrid } from "./desktopLayout";

const grid: DesktopGrid = { columns: 8, rows: 5, blocked: new Set(["7:0", "6:0", "7:1", "6:1"]) };
const initial = { a: { column: 1, row: 0 }, b: { column: 0, row: 0 }, c: { column: 1, row: 1 } };

describe("responsive desktop positions", () => {
  it("places an icon in an empty middle cell without moving any other icon or closing the gap", () => {
    expect(moveDesktopPositions(initial, ["a"], "a", { column: 4, row: 3 }, grid)).toEqual({ ...initial, a: { column: 4, row: 3 } });
  });
  it("makes room below the rightmost cell instead of wrapping to the screen's left edge", () => {
    const next = moveDesktopPositions(initial, ["c"], "c", initial.b, grid)!;
    expect(next.c).toEqual(initial.b);
    expect(next.b).toEqual({ column: 0, row: 1 });
    expect(next.a).toEqual(initial.a);
    expect(new Set(Object.values(next).map(cellID)).size).toBe(3);
  });
  it("shifts a short chain down the right edge, with each displaced icon moving only one cell", () => {
    const layout = { ...initial, c: { column: 1, row: 4 }, d: { column: 0, row: 1 } };
    const next = moveDesktopPositions(layout, ["c"], "c", layout.b, grid)!;
    expect(next.b).toEqual({ column: 0, row: 1 });
    expect(next.d).toEqual({ column: 0, row: 2 });
    expect(next.a).toEqual(layout.a);
  });
  it("chooses an adjacent left cell at the bottom-right corner without wrapping to the top", () => {
    const layout = { a: { column: 0, row: 4 }, b: { column: 4, row: 0 } };
    const next = moveDesktopPositions(layout, ["b"], "b", layout.a, grid)!;
    expect(next.a).toEqual({ column: 1, row: 4 });
  });
  it("never uses blocked widget cells or a group's reserved destination for displacement", () => {
    const layout = { a: { column: 1, row: 0 }, b: { column: 0, row: 0 }, c: { column: 1, row: 3 }, d: { column: 0, row: 3 } };
    const next = moveDesktopPositions(layout, ["c", "d"], "c", layout.a, grid)!;
    expect(next.c).toEqual(layout.a);
    expect(next.d).toEqual(layout.b);
    expect(next.a).toEqual({ column: 1, row: 1 });
    expect(next.b).toEqual({ column: 0, row: 1 });
    expect(new Set(Object.values(next).map(cellID)).size).toBe(4);
    const blockedGrid = { ...grid, blocked: new Set(["0:1", "1:0"]) };
    expect(moveDesktopPositions({ a: layout.b, c: layout.c }, ["c"], "c", layout.b, blockedGrid)).toBeNull();
  });
  it("keeps a selected group's shape while moving into empty space", () => {
    const next = moveDesktopPositions(initial, ["a", "c"], "a", { column: 4, row: 2 }, grid)!;
    expect(next.a).toEqual({ column: 4, row: 2 });
    expect(next.c).toEqual({ column: 4, row: 3 });
    expect(next.b).toEqual(initial.b);
  });
  it("rejects overlap with the system widget and groups crossing the desktop boundary", () => {
    expect(moveDesktopPositions(initial, ["a"], "a", { column: 7, row: 0 }, grid)).toBeNull();
    expect(moveDesktopPositions(initial, ["a", "c"], "a", { column: 4, row: 4 }, grid)).toBeNull();
    expect(moveDesktopPositions(initial, ["a"], "a", { column: -1, row: 0 }, grid)).toBeNull();
  });
  it("projects a smaller window without losing the saved large-screen layout or overlapping icons", () => {
    const saved = { ...initial, a: { column: 4, row: 3 } };
    const snapshot = structuredClone(saved);
    const small = resolveDesktopPositions(Object.keys(saved), saved, { columns: 2, rows: 2, blocked: new Set() });
    expect(Object.values(small).every((cell) => cell.column < 2 && cell.row < 2)).toBe(true);
    expect(new Set(Object.values(small).map(cellID)).size).toBe(3);
    expect(saved).toEqual(snapshot);
    expect(resolveDesktopPositions(Object.keys(saved), saved, grid)).toEqual(saved);
  });
  it("allocates every icon when apps outnumber the visible cells and drops removed keys", () => {
    const keys = Array.from({ length: 9 }, (_, index) => String(index));
    const result = resolveDesktopPositions(keys, { removed: { column: 0, row: 0 } }, { columns: 2, rows: 2, blocked: new Set(["1:0"]) });
    expect(Object.keys(result)).toHaveLength(9);
    expect(new Set(Object.values(result).map(cellID)).size).toBe(9);
    expect(Object.values(result).every((cell) => cellID(cell) !== "1:0")).toBe(true);
    expect(Math.max(...Object.values(result).map((cell) => cell.row))).toBeGreaterThan(1);
  });
});
