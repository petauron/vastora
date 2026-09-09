// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api } from "../api";
import type { LandingLatencyEvent, LandingView } from "../landing-types";
import { LandingExitSelect, LandingLatency, LandingManager, LandingProvider } from "./LandingControls";
import { landingLatencyPreview } from "./landingLatency";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
class LatencyEventSource {
  static instances: LatencyEventSource[] = [];
  onmessage: ((event: MessageEvent<string>) => void) | null = null;
  close = vi.fn();
  constructor(readonly url: string) { LatencyEventSource.instances.push(this); }
  emit(event: LandingLatencyEvent) { this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(event) })); }
}
beforeEach(() => {
  LatencyEventSource.instances = [];
  vi.stubGlobal("EventSource", LatencyEventSource);
});
afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

function overview(): LandingView {
  return {
    nodeIds: ["a", "b"], revision: 4,
    servers: [{ nodeId: "a", name: "落地 A", status: "ready", inUse: true }, { nodeId: "b", name: "落地 B", status: "ready", inUse: false }],
    candidates: [{ nodeId: "a", name: "落地 A" }, { nodeId: "b", name: "落地 B" }, { nodeId: "c", name: "落地 C" }],
    proxies: [{ applicationId: "app-one", landingNodeId: "a", enabled: false, revision: 2, status: "stopped", connection: "disabled" }],
    latencies: [{ nodeId: "source-one", landingNodeId: "a", state: "direct", latencyMs: 12, checkedAt: new Date().toISOString() }, { nodeId: "source-one", landingNodeId: "b", state: "direct", latencyMs: 88, checkedAt: new Date().toISOString() }, { nodeId: "source-two", landingNodeId: "b", state: "direct", latencyMs: 1, checkedAt: new Date().toISOString() }],
  };
}

it("previews the best exit without selecting it and never borrows another pair's latency", () => {
  const view = overview();
  expect(landingLatencyPreview(view, "source-one", "app-one")).toMatchObject({ selected: false, server: { nodeId: "a" }, latency: { latencyMs: 12 } });
  expect(view.proxies[0].enabled).toBe(false);
  view.proxies[0] = { ...view.proxies[0], enabled: true, landingNodeId: "b", status: "ready" };
  expect(landingLatencyPreview(view, "source-one", "app-one")).toMatchObject({ selected: true, server: { nodeId: "b" }, latency: { latencyMs: 88 } });
  view.latencies = view.latencies.filter((sample) => sample.nodeId !== "source-one");
  expect(landingLatencyPreview(view, "source-one", "app-one").latency).toBeUndefined();
  view.servers[1].status = "offline";
  expect(landingLatencyPreview(view, "source-one", "app-one").server?.nodeId).toBe("b");
});

it("shares one overview and sends a specific exit with the observed revision", async () => {
  const view = overview();
  const read = vi.spyOn(api, "landing").mockResolvedValue(view);
  const update = vi.spyOn(api, "configureLandingProxy").mockResolvedValue({ ...view, proxies: [{ ...view.proxies[0], enabled: true, landingNodeId: "b", revision: 3, status: "pending" }] });
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(<LandingProvider enabled><LandingExitSelect applicationId="app-one" nodeId="source-one" name="节点一" locked={false} language="zh-CN" /><LandingLatency applicationId="app-one" nodeId="source-one" language="zh-CN" /><LandingLatency applicationId="app-two" nodeId="source-two" language="zh-CN" /></LandingProvider>);
  });
  expect(read).toHaveBeenCalledTimes(1);
  expect(container.textContent).toContain("12 ms");
  await act(async () => { container.querySelector<HTMLButtonElement>('[role="combobox"]')?.click(); });
  expect(document.body.textContent).toContain("88 ms");
  const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find((item) => item.textContent?.includes("落地 B"));
  expect(option).toBeDefined();
  await act(async () => { option?.click(); });
  expect(update).toHaveBeenCalledWith("app-one", "b", 2, expect.any(AbortSignal));
  expect(container.textContent).toContain("正在切换");
  expect(container.querySelector<HTMLButtonElement>('[role="combobox"]')?.disabled).toBe(true);
});

it("protects in-use servers while letting an unused server be removed", async () => {
  const view = overview();
  vi.spyOn(api, "landing").mockResolvedValue(view);
  const update = vi.spyOn(api, "selectLanding").mockResolvedValue({ ...view, revision: 5, nodeIds: ["a"], servers: [view.servers[0]] });
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => { root?.render(<LandingProvider enabled><LandingManager language="zh-CN" /></LandingProvider>); });
  await act(async () => { [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("管理落地机"))?.click(); });
  expect(document.querySelector<HTMLButtonElement>('[aria-label="移除 落地 A"]')?.disabled).toBe(true);
  await act(async () => { document.querySelector<HTMLButtonElement>('[aria-label="移除 落地 B"]')?.click(); });
  expect(update).toHaveBeenCalledWith(["a"], 4, expect.any(AbortSignal));
});

it("updates one line immediately and does not let the overview poll replace streamed results", async () => {
  vi.useFakeTimers();
  const view = overview();
  vi.spyOn(api, "landing").mockResolvedValue(view);
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(<LandingProvider enabled><div id="one"><LandingLatency applicationId="app-one" nodeId="source-one" language="zh-CN" /></div><div id="two"><LandingLatency applicationId="app-two" nodeId="source-two" language="zh-CN" /></div></LandingProvider>);
  });
  expect(LatencyEventSource.instances).toHaveLength(1);
  const stream = LatencyEventSource.instances[0];
  act(() => stream.emit({ revision: 4, reset: true, upserts: view.latencies, removed: [] }));
  const untouched = container.querySelector("#two span");
  const updated = { ...view.latencies[0], latencyMs: 37 };
  act(() => stream.emit({ revision: 4, reset: false, upserts: [updated], removed: [] }));
  expect(container.querySelector("#one")?.textContent).toContain("37 ms");
  expect(container.querySelector("#two span")).toBe(untouched);
  expect(untouched?.textContent).toBe("1 ms");
  await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
  expect(container.querySelector("#one")?.textContent).toContain("37 ms");
  expect(LatencyEventSource.instances).toHaveLength(1);
  act(() => root?.unmount());
  root = undefined;
  expect(stream.close).toHaveBeenCalledOnce();
});

it("expires only stale pairs during a disconnected stream and accepts a reconnect snapshot", async () => {
  vi.useFakeTimers();
  const view = overview();
  vi.spyOn(api, "landing").mockResolvedValue(view);
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(<LandingProvider enabled><div id="one"><LandingLatency applicationId="app-one" nodeId="source-one" language="zh-CN" /></div><div id="two"><LandingLatency applicationId="app-two" nodeId="source-two" language="zh-CN" /></div></LandingProvider>);
  });
  const stream = LatencyEventSource.instances[0];
  act(() => stream.emit({ revision: 4, reset: true, upserts: view.latencies, removed: [] }));
  await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
  const fresh = { ...view.latencies[2], latencyMs: 2, checkedAt: new Date().toISOString() };
  act(() => stream.emit({ revision: 4, reset: false, upserts: [fresh], removed: [] }));
  await act(async () => { await vi.advanceTimersByTimeAsync(16_000); });
  expect(container.querySelector("#one")?.textContent).not.toContain("12 ms");
  expect(container.querySelector("#two")?.textContent).toContain("2 ms");
  const recovered = { ...view.latencies[0], latencyMs: 7, checkedAt: new Date().toISOString() };
  act(() => stream.emit({ revision: 4, reset: true, upserts: [recovered, fresh], removed: [] }));
  expect(container.querySelector("#one")?.textContent).toContain("7 ms");
  expect(container.querySelector("#two")?.textContent).toContain("2 ms");
});
