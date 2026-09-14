// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api } from "../api";
import type { LandingView } from "../landing-types";
import { LandingLatency, LandingManager, LandingProvider } from "./LandingControls";
import { selectedLandingLatencies } from "./landingLatency";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
class LatencyEventSource {
  onmessage: ((event: MessageEvent<string>) => void) | null = null;
  close = vi.fn();
}
beforeEach(() => vi.stubGlobal("EventSource", LatencyEventSource));
afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function overview(): LandingView {
  return {
    nodeIds: ["a", "b"],
    landingRegionCodes: { a: "US", b: "TW" },
    revision: 4,
    status: "applying",
    eligibleEntries: 2,
    readyCombinations: 3,
    failedCombinations: 1,
    withheldCombinations: 2,
    servers: [
      { nodeId: "a", name: "落地 A", status: "ready", inUse: true, eligibleEntries: 2, readyCombinations: 2, failedCombinations: 0, withheldCombinations: 0 },
      { nodeId: "b", name: "落地 B", status: "ready", inUse: true, eligibleEntries: 2, readyCombinations: 1, failedCombinations: 1, withheldCombinations: 2 },
    ],
    candidates: [{ nodeId: "c", name: "落地 C" }],
    proxies: [],
    latencies: [
      { nodeId: "source-one", landingNodeId: "a", state: "direct", latencyMs: 12, checkedAt: new Date().toISOString() },
      { nodeId: "source-one", landingNodeId: "b", state: "direct", latencyMs: 128, checkedAt: new Date().toISOString() },
    ],
  };
}

it("shows every global landing latency and never includes self-to-self", () => {
  const view = overview();
  expect(selectedLandingLatencies(view, "source-one", "entry-one")).toHaveLength(2);
  expect(selectedLandingLatencies(view, "a", "entry-on-a").map((pair) => pair.server.nodeId)).toEqual(["b"]);
});

it("reports global and per-server rollout counts", async () => {
  vi.spyOn(api, "landing").mockResolvedValue(overview());
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => { root?.render(<LandingProvider enabled><LandingManager language="zh-CN" /></LandingProvider>); });
  await act(async () => { container.querySelector<HTMLButtonElement>("button")?.click(); });
  expect(document.body.textContent).toContain("2 个 VLESS 入口 · 3 个组合就绪 · 1 个失败 · 2 个暂缓发布");
  expect(document.body.textContent).toContain("2 个入口 · 1 就绪 · 1 失败 · 2 暂缓");
});

it("removes an in-use server through the global draining operation", async () => {
  const view = overview();
  vi.spyOn(api, "landing").mockResolvedValue(view);
  const update = vi.spyOn(api, "selectLanding").mockResolvedValue({ ...view, nodeIds: ["a"], retiringNodeIds: ["b"], revision: 5 });
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => { root?.render(<LandingProvider enabled><LandingManager language="zh-CN" /></LandingProvider>); });
  await act(async () => { container.querySelector<HTMLButtonElement>("button")?.click(); });
  await act(async () => { document.querySelector<HTMLButtonElement>('[aria-label="移除 落地 B"]')?.click(); });
  expect(update).toHaveBeenCalledWith(["a"], 4, { a: "US" }, expect.any(AbortSignal));
});

it("renders global latency as read-only state", async () => {
  vi.spyOn(api, "landing").mockResolvedValue(overview());
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => { root?.render(<LandingProvider enabled><LandingLatency applicationId="entry-one" nodeId="source-one" language="zh-CN" /></LandingProvider>); });
  expect(container.textContent).toContain("12 ms");
  expect(container.textContent).toContain("128 ms");
  expect(container.querySelector(".text-destructive")?.textContent).toContain("128 ms");
});
