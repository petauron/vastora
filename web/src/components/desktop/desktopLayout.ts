export type DesktopCell = { column: number; row: number }; // Columns count from the right edge.
export type DesktopPositions = Record<string, DesktopCell>;
export type DesktopGrid = { columns: number; rows: number; blocked: Set<string> };
export const desktopCellWidth = 98;
export const desktopCellHeight = 118;
export const desktopColumnStep = 112;
export const desktopRowStep = 138;
export const cellID = (cell: DesktopCell) => `${cell.column}:${cell.row}`;

const allowed = (cell: DesktopCell, grid: DesktopGrid) => cell.column >= 0 && cell.column < grid.columns && cell.row >= 0 && !grid.blocked.has(cellID(cell));

// Project saved positions into the available viewport without overwriting them.
// Explicit positions get priority; new apps fill the rightmost columns.
export function resolveDesktopPositions(keys: string[], saved: DesktopPositions, grid: DesktopGrid): DesktopPositions {
  const result: DesktopPositions = {};
  const occupied = new Set<string>();
  const candidates = [...keys.filter((key) => saved[key]), ...keys.filter((key) => !saved[key])];
  for (const key of candidates) {
    const index = keys.indexOf(key);
    const preferred = saved[key] ?? { column: Math.min(1, grid.columns - 1) - index % Math.min(2, grid.columns), row: Math.floor(index / Math.min(2, grid.columns)) };
    const desired = { column: Math.min(preferred.column, grid.columns - 1), row: Math.min(preferred.row, grid.rows - 1) };
    let chosen: DesktopCell | undefined;
    if (allowed(desired, grid) && !occupied.has(cellID(desired))) chosen = desired;
    // If a smaller viewport collapses positions, use the nearest free cell.
    for (let rows = grid.rows; !chosen; rows++) {
      let distance = Infinity;
      for (let row = 0; row < rows; row++) for (let column = 0; column < grid.columns; column++) {
        const cell = { column, row };
        if (!allowed(cell, grid) || occupied.has(cellID(cell))) continue;
        const next = Math.hypot(column - desired.column, row - desired.row);
        if (next < distance) { chosen = cell; distance = next; }
      }
    }
    result[key] = chosen;
    occupied.add(cellID(chosen));
  }
  return result;
}

// Find the shortest adjacent path to free space. Equal-distance choices prefer
// right, then down, then left/up. No row wrapping or screen-wide teleporting.
function displacementPath(origin: DesktopCell, occupied: Map<string, string>, reserved: Set<string>, grid: DesktopGrid): DesktopCell[] | null {
  const queue = [origin];
  const parent = new Map<string, DesktopCell | null>([[cellID(origin), null]]);
  for (let index = 0; index < queue.length; index++) {
    const current = queue[index];
    const neighbors = [
      { column: current.column - 1, row: current.row },
      { column: current.column, row: current.row + 1 },
      { column: current.column + 1, row: current.row },
      { column: current.column, row: current.row - 1 },
    ];
    for (const cell of neighbors) {
      const id = cellID(cell);
      if (!allowed(cell, grid) || cell.row >= grid.rows || reserved.has(id) || parent.has(id)) continue;
      parent.set(id, current);
      if (!occupied.has(id)) {
        const path = [cell];
        let previous: DesktopCell | null = current;
        while (previous) { path.unshift(previous); previous = parent.get(cellID(previous)) ?? null; }
        return path;
      }
      queue.push(cell);
    }
  }
  return null;
}

// Keep empty space and group geometry. Only icons along the nearest available
// adjacent path make room; every displaced icon moves exactly one cell.
export function moveDesktopPositions(positions: DesktopPositions, moving: string[], anchor: string, destination: DesktopCell, grid: DesktopGrid): DesktopPositions | null {
  const source = positions[anchor];
  if (!source) return null;
  const next = { ...positions };
  const targets: DesktopPositions = {};
  const rows = Math.max(grid.rows, ...Object.values(positions).map((cell) => cell.row + 1));
  for (const key of moving) {
    const cell = positions[key];
    if (!cell) continue;
    const target = { column: cell.column + destination.column - source.column, row: cell.row + destination.row - source.row };
    if (!allowed(target, grid) || target.row >= rows) return null;
    targets[key] = target;
    delete next[key];
  }
  const reserved = new Set(Object.values(targets).map(cellID));
  const displaced: [string, DesktopCell][] = [];
  for (const [key, cell] of Object.entries(next)) {
    if (reserved.has(cellID(cell))) { displaced.push([key, cell]); delete next[key]; }
  }
  Object.assign(next, targets);
  for (const [key, origin] of displaced) {
    const occupied = new Map(Object.entries(next).map(([item, cell]) => [cellID(cell), item]));
    const path = displacementPath(origin, occupied, reserved, { ...grid, rows });
    if (!path) return null;
    for (let index = path.length - 1; index > 0; index--) {
      const item = index === 1 ? key : occupied.get(cellID(path[index - 1]))!;
      next[item] = path[index];
    }
  }
  return next;
}
