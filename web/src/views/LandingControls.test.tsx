// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api";
import type { LandingView } from "../landing-types";
import { LandingExitSelect, LandingLatency, LandingManager, LandingProvider } from "./LandingControls";
import { landingLatencyPreview } from "./landingLatency";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

function overview(): LandingView {
  return {
    nodeIds: ["a", "b"], revision: 4,
    servers: [{ nodeId: "a", name: "落地 A", status: "ready", inUse: true }, { nodeId: "b", name: "落地 B", status: "ready", inUse: false }],
    candidates: [{ nodeId: "a", name: "落地 A" }, { nodeId: "b", name: "落地 B" }, { nodeId: "c", name: "落地 C" }],
    proxies: [{ applicationId: "app-one", landingNodeId: "a", enabled: false, revision: 2, status: "stopped", connection: "disabled" }],
    latencies: [{ nodeId: "source-one", landingNodeId: "a", state: "direct", latencyMs: 12 }, { nodeId: "source-one", landingNodeId: "b", state: "direct", latencyMs: 88 }, { nodeId: "source-two", landingNodeId: "b", state: "direct", latencyMs: 1 }],
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
