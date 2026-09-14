// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api";
import type { AgentView } from "../types";
import { useLandingRegions } from "./useLandingRegions";
import { RegionFlag } from "./RegionFlag";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

function Harness({ address }: { address: string }) {
  const agents = [{ id: "landing", publicEgress: { address, bindAddress: "10.0.0.2", mode: "nat", observedAt: "2026-09-14T00:00:00Z" }, networkProfile: { directPublic: false } }] as AgentView[];
  const regions = useLandingRegions(["landing"], agents);
  return <RegionFlag code={regions.landing} language="zh-CN" />;
}

it("shows a NAT node flag, reuses it across heartbeat renders, and rejects a result for an old IP", async () => {
  const lookup = vi.spyOn(api, "agentRegionSuggestion").mockResolvedValue({ agentId: "landing", publicAddress: "192.0.2.1", regionCode: "HK", prefix: "", source: "configured_helper" });
  const container = document.createElement("div"); document.body.append(container); root = createRoot(container);
  await act(async () => { root?.render(<Harness address="192.0.2.1" />); });
  expect(container.textContent).toBe("🇭🇰");
  expect(container.querySelector('[role="img"]')?.getAttribute("aria-label")).toContain("香港");
  await act(async () => { root?.render(<Harness address="192.0.2.1" />); });
  expect(lookup).toHaveBeenCalledTimes(1);
  await act(async () => { root?.render(<Harness address="192.0.2.2" />); });
  expect(lookup).toHaveBeenCalledTimes(2);
  expect(container.textContent).toBe("");
});

it("cancels unfinished lookups when leaving the page", async () => {
  let signal: AbortSignal | undefined;
  vi.spyOn(api, "agentRegionSuggestion").mockImplementation((_id, next) => {
    signal = next;
    return new Promise((_resolve, reject) => next?.addEventListener("abort", () => reject(new Error("cancelled")), { once: true }));
  });
  const container = document.createElement("div"); document.body.append(container); root = createRoot(container);
  await act(async () => { root?.render(<Harness address="192.0.2.1" />); });
  expect(signal?.aborted).toBe(false);
  await act(async () => { root?.unmount(); root = undefined; });
  expect(signal?.aborted).toBe(true);
});
