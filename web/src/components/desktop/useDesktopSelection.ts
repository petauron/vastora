import { useCallback, useEffect, useRef, useState, type KeyboardEvent, type MouseEvent, type PointerEvent } from "react";

type Point = { x: number; y: number };
type Rectangle = Point & { width: number; height: number };
type Gesture = { pointerId: number; start: Point; client: Point; initial: Set<string>; additive: boolean; moved: boolean };
const itemSelector = "[data-desktop-item]";

// This bounded marquee needs native pointer capture and rectangle intersection,
// not a drag/reorder engine. Keep transient pointer events outside React state.
export function useDesktopSelection() {
  const surfaceRef = useRef<HTMLElement>(null);
  const gesture = useRef<Gesture | null>(null);
  const frame = useRef<number | null>(null);
  const suppressClick = useRef(false);
  const pointerType = useRef("mouse");
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const [rectangle, setRectangle] = useState<Rectangle | null>(null);

  const cancelFrame = useCallback(() => {
    if (frame.current !== null) cancelAnimationFrame(frame.current);
    frame.current = null;
  }, []);

  const stopDrag = useCallback((restore: boolean) => {
    cancelFrame();
    const current = gesture.current;
    gesture.current = null;
    setRectangle(null);
    if (!current) return;
    suppressClick.current = current.moved;
    if (restore) setSelected(current.initial);
    const surface = surfaceRef.current;
    if (surface?.hasPointerCapture(current.pointerId)) surface.releasePointerCapture(current.pointerId);
  }, [cancelFrame]);

  useEffect(() => {
    const onBlur = () => stopDrag(true);
    window.addEventListener("blur", onBlur);
    return () => { window.removeEventListener("blur", onBlur); cancelFrame(); };
  }, [cancelFrame, stopDrag]);

  function updateRectangle() {
    frame.current = null;
    const current = gesture.current;
    const surface = surfaceRef.current;
    if (!current || !surface) return;
    const bounds = surface.getBoundingClientRect();
    const end = {
      x: Math.max(0, Math.min(bounds.width, current.client.x - bounds.left)),
      y: Math.max(0, Math.min(bounds.height, current.client.y - bounds.top)),
    };
    if (!current.moved && Math.hypot(end.x - current.start.x, end.y - current.start.y) < 4) return;
    current.moved = true;
    const next = { x: Math.min(current.start.x, end.x), y: Math.min(current.start.y, end.y), width: Math.abs(end.x - current.start.x), height: Math.abs(end.y - current.start.y) };
    const nextSelected = new Set(current.additive ? current.initial : []);
    // Read all geometry before updating either visual state.
    for (const item of surface.querySelectorAll<HTMLElement>(itemSelector)) {
      const itemBounds = item.getBoundingClientRect();
      if (next.width > 0 && next.height > 0 && itemBounds.right > bounds.left + next.x && itemBounds.left < bounds.left + next.x + next.width && itemBounds.bottom > bounds.top + next.y && itemBounds.top < bounds.top + next.y + next.height) {
        nextSelected.add(item.dataset.desktopItem!);
      }
    }
    setRectangle(next);
    setSelected(nextSelected);
  }

  function onPointerDown(event: PointerEvent<HTMLElement>) {
    suppressClick.current = false;
    pointerType.current = event.pointerType;
    if (event.button !== 0 || !event.isPrimary || event.pointerType === "touch" || gesture.current) return;
    if (!(event.target instanceof Element) || event.target.closest("button, a, input, select, textarea, [data-desktop-no-select]")) return;
    const surface = event.currentTarget;
    const bounds = surface.getBoundingClientRect();
    const additive = event.shiftKey || event.metaKey || event.ctrlKey;
    gesture.current = { pointerId: event.pointerId, start: { x: event.clientX - bounds.left, y: event.clientY - bounds.top }, client: { x: event.clientX, y: event.clientY }, initial: new Set(selected), additive, moved: false };
    if (!additive) setSelected(new Set());
    event.preventDefault();
    surface.focus({ preventScroll: true });
    surface.setPointerCapture(event.pointerId);
  }

  function onPointerMove(event: PointerEvent<HTMLElement>) {
    if (gesture.current?.pointerId !== event.pointerId) return;
    gesture.current.client = { x: event.clientX, y: event.clientY };
    if (frame.current === null) frame.current = requestAnimationFrame(updateRectangle);
  }

  function onPointerUp(event: PointerEvent<HTMLElement>) {
    if (gesture.current?.pointerId !== event.pointerId) return;
    cancelFrame();
    gesture.current.client = { x: event.clientX, y: event.clientY };
    updateRectangle();
    stopDrag(false);
  }

  function onPointerCancel(event: PointerEvent<HTMLElement>) {
    if (gesture.current?.pointerId === event.pointerId) stopDrag(true);
  }

  function toggleItem(key: string) {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key); else next.add(key);
      return next;
    });
  }

  function onClickCapture(event: MouseEvent<HTMLElement>) {
    if (suppressClick.current) {
      suppressClick.current = false;
      event.preventDefault();
      event.stopPropagation();
      return;
    }
    const item = event.target instanceof Element ? event.target.closest<HTMLElement>(itemSelector) : null;
    if (!item) {
      if (event.target instanceof Element && !event.target.closest("button, a, [data-desktop-no-select]")) setSelected(new Set());
      return;
    }
    const key = item.dataset.desktopItem!;
    if (event.shiftKey || event.metaKey || event.ctrlKey) {
      event.preventDefault();
      event.stopPropagation();
      toggleItem(key);
      return;
    }
    setSelected(new Set([key]));
    // Keep native Enter / assistive activation (detail=0) and touch taps.
    // A mouse's second click opens once; the first click only selects.
    if (event.detail > 0 && pointerType.current === "mouse" && event.detail !== 2) {
      event.preventDefault();
      event.stopPropagation();
    }
  }

  function onKeyDown(event: KeyboardEvent<HTMLElement>) {
    suppressClick.current = false;
    if (event.key === "Escape") {
      stopDrag(false);
      setSelected(new Set());
      event.preventDefault();
      return;
    }
    if (event.target instanceof Element && event.target.closest("[data-desktop-no-select], input, select, textarea")) return;
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "a") {
      event.preventDefault();
      setSelected(new Set([...event.currentTarget.querySelectorAll<HTMLElement>(itemSelector)].map((item) => item.dataset.desktopItem!)));
      return;
    }
    const item = event.target instanceof Element ? event.target.closest<HTMLElement>(itemSelector) : null;
    if (event.key === "Enter" && !item && selected.size === 1) {
      event.preventDefault();
      if (!event.repeat) [...event.currentTarget.querySelectorAll<HTMLElement>(itemSelector)].find((candidate) => selected.has(candidate.dataset.desktopItem!))?.click();
      return;
    }
    if (event.key === " " && item) {
      event.preventDefault();
      if (!event.repeat) toggleItem(item.dataset.desktopItem!);
    }
  }

  function beginItemDrag(key: string) {
    suppressClick.current = true;
    const moving = selected.has(key) ? selected : new Set([key]);
    setSelected(moving);
    return [...moving];
  }

  return { surfaceRef, selected, rectangle, beginItemDrag, surfaceProps: { onPointerDown, onPointerMove, onPointerUp, onPointerCancel, onLostPointerCapture: onPointerCancel, onClickCapture, onKeyDown } };
}
