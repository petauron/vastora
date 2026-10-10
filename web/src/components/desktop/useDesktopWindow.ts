import { useCallback, useEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type PointerEvent } from "react";

export type WindowRect = { x: number; y: number; width: number; height: number };
type WindowState = { rect: WindowRect; maximized: boolean };
type Viewport = { width: number; height: number };
export type ResizeEdge = "n" | "s" | "e" | "w" | "ne" | "nw" | "se" | "sw";
export const resizeEdges: ResizeEdge[] = ["n", "s", "e", "w", "ne", "nw", "se", "sw"];
const viewportSize = (): Viewport => ({ width: window.innerWidth, height: window.innerHeight });
const clamp = (value: number, min: number, max: number) => Math.max(min, Math.min(value, max));
const area = (viewport: Viewport): WindowRect => ({ x: 8, y: 42, width: Math.max(1, viewport.width - 16), height: Math.max(1, viewport.height - 140) });

export function fitWindow(rect: WindowRect, viewport: Viewport): WindowRect {
  const bounds = area(viewport);
  const width = clamp(rect.width, Math.min(640, bounds.width), bounds.width);
  const height = clamp(rect.height, Math.min(360, bounds.height), bounds.height);
  return { width, height, x: clamp(rect.x, bounds.x, bounds.x + bounds.width - width), y: clamp(rect.y, bounds.y, bounds.y + bounds.height - height) };
}
function initialRect(key: string, viewport: Viewport, launchIndex: number): WindowRect {
  const width = Math.min(key === "settings" ? 1080 : 1340, viewport.width - 80);
  const height = Math.min(key === "settings" ? 720 : 880, viewport.height - 180);
  return fitWindow({ x: (viewport.width - width) / 2 + (launchIndex % 5) * 28, y: 74 + (launchIndex % 5) * 28, width, height }, viewport);
}
export function resizeWindow(rect: WindowRect, edge: ResizeEdge, dx: number, dy: number, viewport: Viewport): WindowRect {
  const bounds = area(viewport);
  const minWidth = Math.min(640, bounds.width), minHeight = Math.min(360, bounds.height);
  let left = rect.x, top = rect.y, right = left + rect.width, bottom = top + rect.height;
  if (edge.includes("w")) left = clamp(left + dx, bounds.x, right - minWidth);
  if (edge.includes("e")) right = clamp(right + dx, left + minWidth, bounds.x + bounds.width);
  if (edge.includes("n")) top = clamp(top + dy, bounds.y, bottom - minHeight);
  if (edge.includes("s")) bottom = clamp(bottom + dy, top + minHeight, bounds.y + bounds.height);
  return { x: left, y: top, width: right - left, height: bottom - top };
}

// Pointer Capture provides both drag and eight-edge resize without an extra DnD
// dependency. Base UI handles dialogs, but does not supply movable app windows.
export function useDesktopWindow(key: string, launchIndex = 0) {
  const [viewport, setViewport] = useState(viewportSize);
  const [saved, setSaved] = useState<WindowState | undefined>(undefined);
  const [interacting, setInteracting] = useState(false);
  const gesture = useRef<{
    pointerId: number; element: HTMLElement; edge?: ResizeEdge;
    startX: number; startY: number; origin: WindowRect; restore: WindowRect;
    previous: WindowState | undefined; maximized: boolean; moved: boolean; viewport: Viewport;
  } | null>(null);
  const compact = viewport.width <= 767;
  const initialIndex = useRef(launchIndex);
  const normalRect = fitWindow(saved?.rect ?? initialRect(key, viewport, initialIndex.current), viewport);
  const maximized = !compact && (saved?.maximized ?? false);
  const rect = maximized ? area(viewport) : normalRect;
  const finish = useCallback((commit: boolean) => {
    const active = gesture.current;
    if (!active) return;
    gesture.current = null;
    if (!commit && active.moved) setSaved(active.previous);
    if (active.element.hasPointerCapture(active.pointerId)) active.element.releasePointerCapture(active.pointerId);
    setInteracting(false);
  }, []);
  useEffect(() => {
    const resized = () => { finish(false); setViewport(viewportSize()); };
    const cancel = () => finish(false);
    const escaped = (event: globalThis.KeyboardEvent) => { if (event.key === "Escape") cancel(); };
    window.addEventListener("resize", resized);
    window.addEventListener("blur", cancel);
    window.addEventListener("keydown", escaped);
    return () => { window.removeEventListener("resize", resized); window.removeEventListener("blur", cancel); window.removeEventListener("keydown", escaped); };
  }, [finish]);
  useEffect(() => () => finish(false), [finish]);

  const toggleMaximize = () => {
    if (compact) return;
    finish(false);
    setSaved((current) => ({ rect: current?.rect ?? normalRect, maximized: !current?.maximized }));
  };
  const start = (event: PointerEvent<HTMLElement>, edge?: ResizeEdge) => {
    if (compact || event.button !== 0 || event.isPrimary === false || gesture.current || (edge && maximized)) return;
    if (!edge && (event.target as HTMLElement).closest("button, a, input, select, textarea, [role=button]")) return;
    event.preventDefault();
    event.currentTarget.focus({ preventScroll: true });
    event.currentTarget.setPointerCapture(event.pointerId);
    gesture.current = { pointerId: event.pointerId, element: event.currentTarget, edge, startX: event.clientX, startY: event.clientY, origin: rect, restore: normalRect, previous: saved, maximized, moved: false, viewport };
  };
  const move = (event: PointerEvent<HTMLElement>) => {
    const active = gesture.current;
    if (!active || active.pointerId !== event.pointerId) return;
    const dx = event.clientX - active.startX, dy = event.clientY - active.startY;
    if (!active.moved && Math.hypot(dx, dy) < 4) return;
    active.moved = true;
    let next: WindowRect;
    if (active.edge) next = resizeWindow(active.origin, active.edge, dx, dy, active.viewport);
    else if (active.maximized) {
      const anchor = clamp((active.startX - active.origin.x) / active.origin.width, 0, 1);
      next = fitWindow({ ...active.restore, x: event.clientX - anchor * active.restore.width, y: event.clientY - (active.startY - active.origin.y) }, active.viewport);
    } else next = fitWindow({ ...active.origin, x: active.origin.x + dx, y: active.origin.y + dy }, active.viewport);
    setInteracting(true);
    setSaved({ rect: next, maximized: false });
  };
  const pointerHandlers = {
    onPointerMove: move,
    onPointerUp: (event: PointerEvent<HTMLElement>) => { if (gesture.current?.pointerId === event.pointerId) { move(event); finish(true); } },
    onPointerCancel: () => finish(false),
    onLostPointerCapture: () => finish(false),
  };
  const onKeyDown = (event: KeyboardEvent<HTMLElement>) => {
    if (compact || !event.altKey || !["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(event.key) || event.target !== event.currentTarget) return;
    event.preventDefault();
    if (maximized) return;
    const dx = event.key === "ArrowLeft" ? -16 : event.key === "ArrowRight" ? 16 : 0;
    const dy = event.key === "ArrowUp" ? -16 : event.key === "ArrowDown" ? 16 : 0;
    const next = event.shiftKey ? resizeWindow(normalRect, "se", dx, dy, viewport) : fitWindow({ ...normalRect, x: normalRect.x + dx, y: normalRect.y + dy }, viewport);
    setSaved({ rect: next, maximized: false });
  };
  return {
    compact, maximized, interacting, toggleMaximize,
    style: compact ? undefined : { left: rect.x, top: rect.y, width: rect.width, height: rect.height } as CSSProperties,
    titleBarProps: {
      ...pointerHandlers, onPointerDown: (event: PointerEvent<HTMLElement>) => start(event), onKeyDown,
      onDoubleClick: (event: React.MouseEvent<HTMLElement>) => { if (!(event.target as HTMLElement).closest("button, a")) toggleMaximize(); },
    },
    resizeProps: (edge: ResizeEdge) => ({ ...pointerHandlers, onPointerDown: (event: PointerEvent<HTMLElement>) => start(event, edge) }),
  };
}
