// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { emptyAppData } from "../app-data";
import * as launch from "./applicationLaunch";
import { DesktopView } from "./DesktopView";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let container: HTMLDivElement;
const navigate = vi.fn();
const openApp = vi.fn();
const data = emptyAppData({ version: "test", agentInstallerAvailable: true, agentConnectionMode: "lan", agentConnectUrl: "https://center.example.com" });
const apps = [{ key: "example/alpha", name: "Alpha", count: 1 }, { key: "example/beta", name: "Beta", count: 1 }, { key: "example/gamma", name: "Gamma", count: 1 }];
const names = () => [...container.querySelectorAll(".desktop-shortcut > .desktop-shortcut-label")].map((item) => item.textContent);
const icon = (name: string) => [...container.querySelectorAll<HTMLElement>("[data-desktop-item]")].find((item) => item.querySelector(":scope > .desktop-shortcut-label")?.textContent === name)!;
const cell = (name: string) => icon(name).dataset.desktopCell;
function renderDesktop() { act(() => root.render(<DesktopView data={{ ...data }} language="en" onNavigate={navigate} onOpenApp={openApp} />)); }
function pointer(element: HTMLElement, type: string, column: number, row: number, pointerType = "mouse") {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, clientX: 611 - column * 112, clientY: 159 + row * 138, button: 0 });
  Object.defineProperties(event, { pointerId: { value: 1 }, isPrimary: { value: true }, pointerType: { value: pointerType } });
  act(() => element.dispatchEvent(event));
}
function start(name: string) {
  const item = icon(name);
  const [column, row] = item.dataset.desktopCell!.split(":").map(Number);
  pointer(item, "pointerdown", column, row);
  return item;
}

beforeEach(() => {
  window.localStorage.clear();
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  vi.stubGlobal("innerWidth", 1200);
  vi.stubGlobal("innerHeight", 1000);
  for (const method of ["setPointerCapture", "releasePointerCapture"]) Object.defineProperty(HTMLElement.prototype, method, { configurable: true, value: vi.fn() });
  Object.defineProperty(HTMLElement.prototype, "hasPointerCapture", { configurable: true, value: () => false });
  vi.spyOn(launch, "desktopApplications").mockReturnValue(apps);
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    const width = this.classList.contains("desktop-shortcuts") ? 560 : 0;
    return { left: 100, right: 100 + width, top: 100, bottom: 788, width, height: 688, x: 100, y: 100, toJSON() {} };
  });
  container = document.createElement("div"); document.body.append(container); root = createRoot(container);
});
afterEach(() => { act(() => root.unmount()); container.remove(); window.localStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.clearAllMocks(); });

describe("desktop placement interaction", () => {
  it("previews a free position beyond the original icon columns and persists it only after drop", () => {
    renderDesktop();
    const source = start("Gamma");
    pointer(source, "pointermove", 3, 3);
    expect(cell("Gamma")).toBe("3:3");
    expect(icon("Gamma").dataset.dropTarget).toBe("true");
    expect(cell("Alpha")).toBe("1:1");
    expect(window.localStorage.getItem("vastora.desktop-positions.v1")).toBeNull();
    pointer(source, "pointerup", 3, 3);
    expect(cell("Gamma")).toBe("3:3");
    expect(container.querySelector("[data-drop-target]")).toBeNull();
    act(() => source.click());
    expect(openApp).not.toHaveBeenCalled();
    act(() => root.unmount()); root = createRoot(container); renderDesktop();
    expect(cell("Gamma")).toBe("3:3");
  });
  it("previews a local downward chain at the right edge and commits the exact same positions", () => {
    renderDesktop();
    const source = start("Gamma");
    pointer(source, "pointermove", 0, 0);
    expect(cell("Gamma")).toBe("0:0");
    expect(cell("Hosts")).toBe("0:1");
    expect(cell("Beta")).toBe("0:2");
    expect(cell("App Store")).toBe("1:0");
    pointer(source, "pointermove", 0, 0);
    pointer(source, "pointerup", 0, 0);
    expect(cell("Gamma")).toBe("0:0");
    expect(cell("Hosts")).toBe("0:1");
    expect(cell("Beta")).toBe("0:2");
    expect(cell("App Store")).toBe("1:0");
  });
  it.each(["Escape", "blur", "resize", "pointercancel", "outside"])("cancels a free placement on %s", (reason) => {
    renderDesktop();
    const source = start("Gamma");
    pointer(source, "pointermove", 3, 3);
    if (reason === "Escape") act(() => source.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    else if (reason === "blur" || reason === "resize") act(() => window.dispatchEvent(new Event(reason)));
    else pointer(source, reason === "outside" ? "pointerup" : "pointercancel", 12, 12);
    expect(cell("Gamma")).toBe("1:2");
    expect(container.querySelector(".desktop-drag-preview")).toBeNull();
    expect(window.localStorage.getItem("vastora.desktop-positions.v1")).toBeNull();
  });
  it("allows keyboard placement into an empty cell without opening an app or losing focus", () => {
    renderDesktop();
    act(() => { icon("Alpha").focus(); icon("Alpha").dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", altKey: true, bubbles: true, cancelable: true })); });
    expect(cell("Alpha")).toBe("2:1");
    expect(document.activeElement).toBe(icon("Alpha"));
    expect(openApp).not.toHaveBeenCalled();
  });
  it("retains saved desktop positions when switching to and back from a compact viewport", () => {
    window.localStorage.setItem("vastora.desktop-positions.v1", JSON.stringify({ "app:example/alpha": { column: 3, row: 3 } }));
    renderDesktop();
    expect(icon("Alpha").style.left).not.toBe("");
    vi.stubGlobal("innerWidth", 390);
    act(() => window.dispatchEvent(new Event("resize")));
    expect(icon("Alpha").style.left).toBe("");
    vi.stubGlobal("innerWidth", 1200);
    act(() => window.dispatchEvent(new Event("resize")));
    expect(cell("Alpha")).toBe("3:3");
    expect(icon("Alpha").style.left).not.toBe("");
  });
  it("retains order preferences and ignores removed or corrupt positions", () => {
    window.localStorage.setItem("vastora.desktop-order.v1", JSON.stringify(["app:example/gamma", "app:removed", "app:example/gamma", "system:nodes", 42]));
    window.localStorage.setItem("vastora.desktop-positions.v1", JSON.stringify({ "app:example/alpha": { column: -1, row: 3 }, "app:removed": { column: 2, row: 2 } }));
    renderDesktop();
    expect(names()).toEqual(["Gamma", "Hosts", "App Store", "Alpha", "Beta"]);
    expect(cell("Alpha")).toBe("0:1");
  });
  it("preserves single-click selection, double-click opening and touch activation", () => {
    renderDesktop();
    act(() => icon("Alpha").dispatchEvent(new MouseEvent("click", { detail: 1, bubbles: true })));
    expect(openApp).not.toHaveBeenCalled();
    act(() => icon("Alpha").dispatchEvent(new MouseEvent("click", { detail: 2, bubbles: true })));
    expect(openApp).toHaveBeenCalledExactlyOnceWith("example/alpha");
    pointer(icon("Beta"), "pointerdown", 0, 1, "touch");
    pointer(icon("Beta"), "pointerup", 0, 1, "touch");
    act(() => icon("Beta").dispatchEvent(new MouseEvent("click", { detail: 1, bubbles: true })));
    expect(openApp).toHaveBeenLastCalledWith("example/beta");
    expect(container.querySelector(".desktop-drag-preview")).toBeNull();
  });
  it("still places icons when the browser cannot save the layout", () => {
    renderDesktop();
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("Storage unavailable"); });
    const source = start("Gamma"); pointer(source, "pointermove", 3, 3); pointer(source, "pointerup", 3, 3);
    expect(cell("Gamma")).toBe("3:3");
    expect(container.textContent).toContain("this browser could not save it");
  });
});
