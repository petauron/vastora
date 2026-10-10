import { useCallback, useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import { cellID, desktopCellHeight, desktopCellWidth, desktopColumnStep, desktopRowStep, moveDesktopPositions, resolveDesktopPositions, type DesktopCell, type DesktopGrid, type DesktopPositions } from "./desktopLayout";

// Order is also used by the compact layout and assistive navigation; positions
// describe the spacious desktop independently of temporary viewport projection.
const orderStorageKey = "vastora.desktop-order.v1";
const positionsStorageKey = "vastora.desktop-positions.v1";
type Geometry = DesktopGrid & { width: number; compact: boolean };
type Gesture = { pointerId: number; element: HTMLElement; key: string; x: number; y: number; moving: string[]; positions: DesktopPositions; bounds: DOMRect; grid: Geometry };

function readOrder(): string[] {
  try {
    const value: unknown = JSON.parse(window.localStorage.getItem(orderStorageKey) ?? "[]");
    return Array.isArray(value) ? [...new Set(value.filter((key): key is string => typeof key === "string"))] : [];
  } catch { return []; }
}
function readPositions(): DesktopPositions {
  try {
    const value: unknown = JSON.parse(window.localStorage.getItem(positionsStorageKey) ?? "{}");
    if (!value || typeof value !== "object" || Array.isArray(value)) return {};
    return Object.fromEntries(Object.entries(value).filter(([, cell]) => cell && Number.isInteger(cell.column) && cell.column >= 0 && cell.column < 1000 && Number.isInteger(cell.row) && cell.row >= 0 && cell.row < 1000));
  } catch { return {}; }
}

export function useDesktopOrder(keys: string[], beginDrag: (key: string) => string[]) {
  const gridRef = useRef<HTMLElement>(null);
  const [grid, setGrid] = useState<Geometry | null>(null);
  const [savedOrder, setSavedOrder] = useState(readOrder);
  const [savedPositions, setSavedPositions] = useState(readPositions);
  const [dragging, setDragging] = useState<string[]>([]);
  const gesture = useRef<Gesture | null>(null);
  const [position, setPosition] = useState<{ x: number; y: number; key: string } | null>(null);
  const [preview, setPreview] = useState<DesktopPositions | null>(null);
  const [announcement, setAnnouncement] = useState<{ revision: number; saved: boolean } | null>(null);
  const available = new Set(keys);
  const orderedKeys = [...savedOrder.filter((key) => available.has(key)), ...keys.filter((key) => !savedOrder.includes(key))];
  const positions = grid ? resolveDesktopPositions(orderedKeys, savedPositions, grid) : {};

  const endDrag = useCallback(() => {
    const current = gesture.current;
    gesture.current = null;
    if (current?.element.hasPointerCapture(current.pointerId)) current.element.releasePointerCapture(current.pointerId);
    setDragging([]);
    setPosition(null);
    setPreview(null);
  }, []);

  useLayoutEffect(() => {
    const element = gridRef.current;
    if (!element) return;
    const widget = element.closest(".desktop-home")?.querySelector<HTMLElement>(".desktop-widget");
    function measure() {
      const bounds = element!.getBoundingClientRect();
      const compact = window.innerWidth <= 767;
      const columns = Math.max(1, Math.floor((bounds.width + 14) / desktopColumnStep));
      const rows = Math.max(1, Math.floor((window.innerHeight - bounds.top - 106 + 20) / desktopRowStep));
      const blocked = new Set<string>();
      const widgetBounds = widget?.getBoundingClientRect();
      if (!compact && widgetBounds) for (let row = 0; row < Math.max(rows, Math.ceil((widgetBounds.bottom - bounds.top + 12) / desktopRowStep)); row++) for (let column = 0; column < columns; column++) {
        const left = bounds.right - desktopCellWidth - column * desktopColumnStep;
        const top = bounds.top + row * desktopRowStep;
        if (left < widgetBounds.right + 12 && left + desktopCellWidth > widgetBounds.left - 12 && top < widgetBounds.bottom + 12 && top + desktopCellHeight > widgetBounds.top - 12) blocked.add(cellID({ column, row }));
      }
      const next = { columns, rows, width: bounds.width, compact, blocked };
      setGrid((previous) => previous && previous.columns === columns && previous.rows === rows && previous.width === bounds.width && previous.compact === compact && [...previous.blocked].join() === [...blocked].join() ? previous : next);
    }
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    if (widget) observer.observe(widget);
    window.addEventListener("resize", measure);
    return () => { observer.disconnect(); window.removeEventListener("resize", measure); };
  }, []);

  useEffect(endDrag, [endDrag, grid]);

  useEffect(() => {
    window.addEventListener("blur", endDrag);
    window.addEventListener("resize", endDrag);
    window.addEventListener("scroll", endDrag, true);
    return () => { window.removeEventListener("blur", endDrag); window.removeEventListener("resize", endDrag); window.removeEventListener("scroll", endDrag, true); };
  }, [endDrag]);

  function commit(next: DesktopPositions) {
    if (orderedKeys.some((key) => !next[key] || !positions[key])) return;
    // Preserve offscreen preferences for every icon that did not move.
    const updated: DesktopPositions = {};
    for (const key of orderedKeys) {
      const moved = cellID(next[key]) !== cellID(positions[key]);
      updated[key] = moved ? next[key] : savedPositions[key] ?? positions[key];
    }
    const order = [...orderedKeys].sort((a, b) => next[a].row - next[b].row || next[b].column - next[a].column);
    setSavedPositions(updated);
    setSavedOrder(order);
    let saved = true;
    try {
      window.localStorage.setItem(positionsStorageKey, JSON.stringify(updated));
      window.localStorage.setItem(orderStorageKey, JSON.stringify(order));
    } catch { saved = false; }
    setAnnouncement((current) => ({ revision: (current?.revision ?? 0) + 1, saved }));
  }

  function destination(event: PointerEvent<HTMLElement>): DesktopPositions | null {
    const current = gesture.current;
    if (!current?.moving.length) return null;
    const { bounds, grid: geometry } = current;
    if (event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom) return null;
    const target: DesktopCell = {
      column: Math.min(geometry.columns - 1, Math.max(0, Math.round((bounds.right - desktopCellWidth / 2 - event.clientX) / desktopColumnStep))),
      row: Math.max(0, Math.round((event.clientY - bounds.top - desktopCellHeight / 2) / desktopRowStep)),
    };
    return moveDesktopPositions(current.positions, current.moving, current.key, target, geometry);
  }

  function itemProps(key: string) {
    const cell = positions[key];
    const shown = preview?.[key] ?? cell;
    return {
      draggable: false,
      "data-dragging": dragging.includes(key),
      "data-drop-target": dragging.includes(key) && preview !== null || undefined,
      "data-desktop-cell": shown ? cellID(shown) : undefined,
      style: grid && !grid.compact && cell && shown ? { left: grid.width - desktopCellWidth - cell.column * desktopColumnStep, top: cell.row * desktopRowStep, transform: `translate(${(cell.column - shown.column) * desktopColumnStep}px, ${(shown.row - cell.row) * desktopRowStep}px)` } : undefined,
      onPointerDown(event: PointerEvent<HTMLElement>) {
        if (!grid || grid.compact || event.button !== 0 || !event.isPrimary || event.pointerType === "touch" || !gridRef.current) return;
        gesture.current = { pointerId: event.pointerId, element: event.currentTarget, key, x: event.clientX, y: event.clientY, moving: [], positions, bounds: gridRef.current.getBoundingClientRect(), grid };
        event.currentTarget.setPointerCapture(event.pointerId);
      },
      onPointerMove(event: PointerEvent<HTMLElement>) {
        const current = gesture.current;
        if (!current || current.pointerId !== event.pointerId) return;
        if (!current.moving.length) {
          if (Math.hypot(event.clientX - current.x, event.clientY - current.y) < 5) return;
          current.moving = beginDrag(current.key).filter((candidate) => available.has(candidate));
          setDragging(current.moving);
          setAnnouncement(null);
        }
        event.preventDefault();
        setPosition({ x: event.clientX, y: event.clientY, key: current.key });
        setPreview(destination(event));
      },
      onPointerUp(event: PointerEvent<HTMLElement>) {
        if (gesture.current?.pointerId !== event.pointerId) return;
        const next = destination(event);
        endDrag();
        if (next) commit(next);
      },
      onPointerCancel: endDrag,
      onLostPointerCapture: endDrag,
      onKeyDown(event: KeyboardEvent<HTMLElement>) {
        if (event.key === "Escape") { endDrag(); return; }
        if (!grid || grid.compact || !event.altKey || event.ctrlKey || event.metaKey || !["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(event.key)) return;
        event.preventDefault();
        event.stopPropagation();
        if (gesture.current) return;
        const target = { column: cell.column + (event.key === "ArrowLeft" ? 1 : event.key === "ArrowRight" ? -1 : 0), row: cell.row + (event.key === "ArrowUp" ? -1 : event.key === "ArrowDown" ? 1 : 0) };
        const next = moveDesktopPositions(positions, [key], key, target, grid);
        if (next) commit(next);
      },
    };
  }

  const contentRows = Math.max(grid?.rows ?? 1, ...Object.values(positions).map((cell) => cell.row + 1));
  return { gridRef, gridStyle: grid && !grid.compact ? { height: contentRows * desktopRowStep - 20 } : undefined, ready: grid !== null, orderedKeys, itemProps, announcement, dragging, position };
}
