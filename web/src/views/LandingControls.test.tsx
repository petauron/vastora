// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api } from "../api";
import type { LandingLatencyEvent, LandingView } from "../landing-types";
import { LandingExitSelect, LandingLatency, LandingManager, LandingProvider } from "./LandingControls";
import { selectedLandingLatencies } from "./landingLatency";
import { useIsMobile } from "@/hooks/use-mobile";

vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: vi.fn(() => false) }));

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
  vi.mocked(useIsMobile).mockReturnValue(false);
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
    proxies: [{ applicationId: "app-one", landingNodeId: "a", enabled: false, revision: 2, status: "stopped", connection: "disabled", applied: { revision: 2, landingNodeId: "" } }],
    latencies: [{ nodeId: "source-one", landingNodeId: "a", state: "direct", latencyMs: 12, checkedAt: new Date().toISOString() }, { nodeId: "source-one", landingNodeId: "b", state: "direct", latencyMs: 88, checkedAt: new Date().toISOString() }, { nodeId: "source-two", landingNodeId: "b", state: "direct", latencyMs: 1, checkedAt: new Date().toISOString() }],
  };
}

it("summarizes only configured exits and never borrows another pair's latency", () => {
  const view = overview();
  expect(selectedLandingLatencies(view, "source-one", "app-one")).toEqual([]);
  expect(view.proxies[0].enabled).toBe(false);
  view.proxies[0] = { ...view.proxies[0], enabled: true, landingNodeId: "b", status: "ready" };
  expect(selectedLandingLatencies(view, "source-one", "app-one")).toMatchObject([{ server: { nodeId: "b" }, latency: { latencyMs: 88 } }]);
  view.latencies = view.latencies.filter((sample) => sample.nodeId !== "source-one");
  expect(selectedLandingLatencies(view, "source-one", "app-one")[0].latency).toBeUndefined();
  view.servers[1].status = "offline";
  expect(selectedLandingLatencies(view, "source-one", "app-one")[0].server?.status).toBe("offline");
  view.nodeExits = [{ applicationId: "app-one", ownExit: true, landingNodeIds: [], revision: 3 }];
  expect(selectedLandingLatencies(view, "source-one", "app-one")).toEqual([]);
});


it.each([false, true])("saves multiple exits from the node row (mobile: %s)", async (mobile) => {
  vi.mocked(useIsMobile).mockReturnValue(mobile);
  const view = overview();
  view.nodeExits = [{ applicationId: "app-one", ownExit: true, landingNodeIds: [], revision: 2 }];
  const read = vi.spyOn(api, "landing").mockResolvedValue(view);
  const update = vi.spyOn(api, "configureNodeExits").mockResolvedValue({ ...view, nodeExits: [{ ...view.nodeExits[0], landingNodeIds: ["a", "b"], revision: 3, status: "applying" }] });
  const container = document.createElement("div"); document.body.append(container); root = createRoot(container);
  await act(async () => { root?.render(<LandingProvider enabled><LandingExitSelect applicationId="app-one" nodeId="source-one" name="节点一" locked={false} language="zh-CN" /></LandingProvider>); });
  expect(read).toHaveBeenCalledTimes(1);
  await act(async () => { container.querySelector<HTMLButtonElement>('button')?.click(); });
  const checks = [...document.querySelectorAll<HTMLElement>('[role="checkbox"]')];
  expect(checks).toHaveLength(3);
  await act(async () => { checks[1].click(); });
  await act(async () => { checks[2].click(); });
  expect(update).not.toHaveBeenCalled();
  await act(async () => { [...document.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent === "保存出口组合")?.click(); });
  expect(update).toHaveBeenCalledWith("app-one", { ownExit: true, landingNodeIds: ["a", "b"], revision: 2, confirmSessionReset: true }, expect.any(AbortSignal));
  expect(container.textContent).toContain("正在同步组合");
});

it("keeps the row compact and restores the selected exits after closing the editor", async () => {
  const view = overview();
  view.nodeExits = [{ applicationId: "app-one", ownExit: true, landingNodeIds: ["a", "b"], revision: 3, status: "saved" }];
  view.latencies[1].latencyMs = 157;
  vi.spyOn(api, "landing").mockResolvedValue(view);
  const update = vi.spyOn(api, "configureNodeExits");
  const container = document.createElement("div"); document.body.append(container); root = createRoot(container);
  await act(async () => { root?.render(<LandingProvider enabled><LandingExitSelect applicationId="app-one" nodeId="source-one" name="节点一" locked={false} language="zh-CN" /><LandingLatency applicationId="app-one" nodeId="source-one" language="zh-CN" /></LandingProvider>); });
  expect(container.textContent).toContain("本机 + 2 个落地");
  expect(container.textContent).toContain("12–157 ms");
  expect(container.textContent).not.toContain("出口配置已保存");
  expect(container.textContent).not.toContain("落地 A");
  const trigger = container.querySelector<HTMLButtonElement>('button[aria-label="配置 节点一 的出口"]');
  await act(async () => { trigger?.click(); });
  expect(document.querySelector(".apps-exit-popover")?.textContent).toContain("落地 A");
  expect(document.querySelector(".apps-exit-popover")?.textContent).toContain("157 ms");
  expect(document.querySelector(".apps-exit-popover .text-destructive")).toBeNull();
  await act(async () => { document.querySelectorAll<HTMLElement>('[role="checkbox"]')[1].click(); });
  await act(async () => { [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent === "取消")?.click(); });
  await act(async () => { trigger?.click(); });
  expect([...document.querySelectorAll<HTMLElement>('[role="checkbox"]')].map((checkbox) => checkbox.getAttribute("aria-checked"))).toEqual(["true", "true", "true"]);
  expect(update).not.toHaveBeenCalled();
});

it("keeps failed and pending exits visible beside the measured range", async () => {
  const view = overview();
  view.nodeExits = [{ applicationId: "app-one", ownExit: false, landingNodeIds: ["a", "b", "missing"], revision: 3 }];
  view.latencies = view.latencies.filter((sample) => sample.landingNodeId !== "b");
  vi.spyOn(api, "landing").mockResolvedValue(view);
  const container = document.createElement("div"); document.body.append(container); root = createRoot(container);
  await act(async () => { root?.render(<LandingProvider enabled><LandingLatency applicationId="app-one" nodeId="source-one" language="zh-CN" /></LandingProvider>); });
  expect(container.textContent).toContain("12 ms");
  expect(container.textContent).toContain("1 个待检测");
  expect(container.querySelector(".text-destructive")?.textContent).toBe("1 个不可用");
});

it.each(["paused", "failed", "controller-blocked"] as const)("does not allow an unsafe write when %s", async (condition) => {
  const view = overview(); view.tasksPaused = condition === "paused";
  view.controllerBlocked = condition === "controller-blocked";
  vi.spyOn(api, "landing").mockImplementation(() => condition === "failed" ? Promise.reject(new Error("offline")) : Promise.resolve(view));
  const update = vi.spyOn(api, "configureNodeExits");
  const container = document.createElement("div"); document.body.append(container); root = createRoot(container);
  await act(async () => { root?.render(<LandingProvider enabled><LandingExitSelect applicationId="app-one" nodeId="source-one" name="节点一" locked={false} language="zh-CN" /></LandingProvider>); });
  const trigger = container.querySelector<HTMLButtonElement>("button");
  if (condition === "failed") {
    expect(trigger?.disabled).toBe(true);
  } else {
    expect(trigger?.disabled).toBe(false);
    await act(async () => { trigger?.click(); });
    const save = [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent === "保存出口组合");
    expect(save?.disabled).toBe(true);
    if (condition === "controller-blocked") expect(document.body.textContent).toContain("订阅主机任务待处理");
  }
  expect(update).not.toHaveBeenCalled();
});

it("distinguishes a ready exit with unresolved tasks and restores saved checks", async () => {
  const view = overview();
  view.blockedNodeIds = ["b"];
  view.nodeExits = [{ applicationId: "app-one", ownExit: true, landingNodeIds: ["a"], revision: 3 }];
  vi.spyOn(api, "landing").mockResolvedValue(view);
  const container = document.createElement("div"); document.body.append(container); root = createRoot(container);
  await act(async () => { root?.render(<LandingProvider enabled><LandingExitSelect applicationId="app-one" nodeId="source-one" name="节点一" locked={false} language="zh-CN" /></LandingProvider>); });
  await act(async () => { container.querySelector<HTMLButtonElement>("button")?.click(); });
  const checks = [...document.querySelectorAll<HTMLElement>('[role="checkbox"]')];
  expect(checks[1].getAttribute("aria-checked")).toBe("true");
  expect(document.body.textContent).toContain("任务待处理");
  expect(document.body.textContent).not.toContain("未就绪");
});

it("keeps the draft and a specific error when a save is rejected", async () => {
  vi.spyOn(api, "landing").mockResolvedValue(overview());
  vi.spyOn(api, "configureNodeExits").mockRejectedValue(Object.assign(new Error("blocked"), { code: "exit_controller_blocked" }));
  const container = document.createElement("div"); document.body.append(container); root = createRoot(container);
  await act(async () => { root?.render(<LandingProvider enabled><LandingExitSelect applicationId="app-one" nodeId="source-one" name="节点一" locked={false} language="zh-CN" /></LandingProvider>); });
  await act(async () => { container.querySelector<HTMLButtonElement>("button")?.click(); });
  await act(async () => { document.querySelectorAll<HTMLElement>('[role="checkbox"]')[1].click(); });
  await act(async () => { [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent === "保存出口组合")?.click(); });
  expect(document.querySelectorAll<HTMLElement>('[role="checkbox"]')[1].getAttribute("aria-checked")).toBe("true");
  expect(document.body.textContent).toContain("本次出口配置未保存");
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
  view.nodeExits = [{ applicationId: "app-one", ownExit: true, landingNodeIds: ["a"], revision: 3 }, { applicationId: "app-two", ownExit: true, landingNodeIds: ["b"], revision: 3 }];
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
  view.nodeExits = [{ applicationId: "app-one", ownExit: true, landingNodeIds: ["a"], revision: 3 }, { applicationId: "app-two", ownExit: true, landingNodeIds: ["b"], revision: 3 }];
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
