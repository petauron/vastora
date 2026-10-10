// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fitWindow, resizeEdges, resizeWindow, useDesktopWindow, type ResizeEdge } from "./useDesktopWindow";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root, container: HTMLDivElement;
function Harness({ app = "overview" }: { app?: string }) {
  const frame = useDesktopWindow(app);
  return <section style={frame.style} data-maximized={frame.maximized} data-compact={frame.compact}>
    <header {...frame.titleBarProps} tabIndex={0}><span>Title</span><button onClick={frame.toggleMaximize}>Zoom</button></header>
    {!frame.compact && !frame.maximized ? resizeEdges.map((edge) => <div key={edge} data-edge={edge} {...frame.resizeProps(edge)} />) : null}
  </section>;
}
const frame = () => container.querySelector("section")!;
const title = () => container.querySelector("header")!;
const rect = () => { const s = frame().style; return [s.left, s.top, s.width, s.height].map(parseFloat); };
function render(app = "overview") { act(() => root.render(<Harness app={app} />)); }
function pointer(target: HTMLElement, type: string, x: number, y: number) {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, clientX: x, clientY: y, button: 0 });
  Object.defineProperties(event, { pointerId: { value: 1 }, isPrimary: { value: true } });
  act(() => target.dispatchEvent(event));
}
beforeEach(() => {
  vi.stubGlobal("innerWidth", 1576); vi.stubGlobal("innerHeight", 1218);
  for (const method of ["setPointerCapture", "releasePointerCapture"]) Object.defineProperty(HTMLElement.prototype, method, { configurable: true, value: vi.fn() });
  Object.defineProperty(HTMLElement.prototype, "hasPointerCapture", { configurable: true, value: () => false });
  container = document.createElement("div"); document.body.append(container); root = createRoot(container);
});
afterEach(() => { act(() => root.unmount()); container.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe("application window interaction", () => {
  it("moves the window, restores its exact geometry after maximize and keeps each app independent", () => {
    render(); const original = rect();
    pointer(title(), "pointerdown", 400, 95); pointer(title(), "pointermove", 350, 195); pointer(title(), "pointerup", 350, 195);
    const moved = rect(); expect(moved).toEqual([original[0] - 50, original[1] + 100, original[2], original[3]]);
    act(() => container.querySelector("button")!.click()); expect(frame().dataset.maximized).toBe("true"); expect(rect()).toEqual([8, 42, 1560, 1078]);
    act(() => container.querySelector("button")!.click()); expect(rect()).toEqual(moved);
    render("settings"); expect(rect()[2]).toBe(1080); render(); expect(rect()).toEqual(moved);
  });
  it.each(["Escape", "blur", "pointercancel", "lostpointercapture", "resize"])("cancels an in-progress gesture on %s", (reason) => {
    render(); const original = rect();
    pointer(title(), "pointerdown", 400, 95); pointer(title(), "pointermove", 350, 195); expect(rect()).not.toEqual(original);
    if (reason === "Escape") act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    else if (reason === "blur" || reason === "resize") act(() => window.dispatchEvent(new Event(reason)));
    else pointer(title(), reason, 350, 195);
    expect(rect()).toEqual(original);
  });
  it("resizes from a corner while holding the opposite corner fixed", () => {
    render(); const [x,y,w,h] = rect(); const edge = container.querySelector<HTMLElement>('[data-edge="nw"]')!;
    pointer(edge, "pointerdown", x,y); pointer(edge, "pointermove", x+150,y+100); pointer(edge, "pointerup", x+150,y+100);
    expect(rect()).toEqual([x+150,y+100,w-150,h-100]);
  });
  it("ignores traffic-light pointer gestures and tiny title-bar movement", () => {
    render(); const original = rect(); const button = container.querySelector("button")!;
    pointer(button, "pointerdown", 400,95); pointer(title(), "pointermove", 350,195); pointer(title(), "pointerup", 350,195); expect(rect()).toEqual(original);
    pointer(title(), "pointerdown", 400,95); pointer(title(), "pointerup", 402,96); expect(rect()).toEqual(original);
  });
  it("double-clicks to maximize and drags a maximized window back under the pointer", () => {
    render(); const original = rect(); act(() => title().dispatchEvent(new MouseEvent("dblclick", { bubbles: true })));
    expect(frame().dataset.maximized).toBe("true");
    pointer(title(), "pointerdown", 788,65); pointer(title(), "pointermove", 738,165); pointer(title(), "pointerup", 738,165);
    expect(frame().dataset.maximized).toBe("false"); expect(rect()).toEqual([68,142,original[2],original[3]]);
  });
  it("keeps saved desktop geometry across viewport shrink and mobile mode", () => {
    render(); const original = rect();
    act(() => { vi.stubGlobal("innerWidth", 900); vi.stubGlobal("innerHeight", 700); window.dispatchEvent(new Event("resize")); });
    const [x,y,w,h] = rect(); expect(x+w).toBeLessThanOrEqual(892); expect(y+h).toBeLessThanOrEqual(602);
    act(() => { vi.stubGlobal("innerWidth", 390); window.dispatchEvent(new Event("resize")); });
    expect(frame().style.width).toBe(""); expect(container.querySelector("[data-edge]")).toBeNull();
    act(() => { vi.stubGlobal("innerWidth", 1576); vi.stubGlobal("innerHeight", 1218); window.dispatchEvent(new Event("resize")); });
    expect(rect()).toEqual(original);
  });
  it("moves and resizes by keyboard without activating toolbar buttons", () => {
    render(); const original = rect();
    act(() => title().dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", altKey: true, bubbles: true, cancelable: true })));
    expect(rect()[0]).toBe(original[0]-16);
    act(() => title().dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", altKey: true, shiftKey: true, bubbles: true, cancelable: true })));
    expect(rect()[3]).toBe(original[3]+16);
  });
  it("clamps all eight resize directions to minimum size and work-area bounds", () => {
    const viewport = { width: 1200, height: 900 }, original = { x: 100, y: 100, width: 800, height: 600 };
    for (const edge of resizeEdges as ResizeEdge[]) for (const delta of [-10000,10000]) {
      const result = resizeWindow(original, edge, delta, delta, viewport);
      expect(result.width).toBeGreaterThanOrEqual(640); expect(result.height).toBeGreaterThanOrEqual(360);
      expect(fitWindow(result, viewport)).toEqual(result);
    }
  });
});
