// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api } from "../api";
import type { LandingLatencyEvent, LandingLatencyView, LandingView } from "../landing-types";
import { LandingProvider, useLanding } from "./LandingControls";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let container: HTMLDivElement;
class TestStream {
  static instances: TestStream[] = [];
  onmessage: ((event: MessageEvent) => void) | null = null;
  close = vi.fn();
  constructor() { TestStream.instances.push(this); }
  send(event: LandingLatencyEvent) { this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(event) })); }
}
const sample = (latencyMs: number): LandingLatencyView => ({ nodeId: "entry", landingNodeId: "exit", state: "direct", latencyMs, checkedAt: new Date().toISOString() });
const view = (latencies: LandingLatencyView[], revision = 1): LandingView => ({ revision, latencies, nodeIds: [], status: "ready", eligibleEntries: 0, readyCombinations: 0, failedCombinations: 0, withheldCombinations: 0, servers: [], candidates: [], proxies: [] });
function Probe() {
  const landing = useLanding();
  return <><output>{JSON.stringify(landing?.view?.latencies ?? [])}</output><button onClick={landing?.refresh}>refresh</button></>;
}
beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-10-18T00:00:00Z"));
  vi.stubGlobal("EventSource", TestStream);
  TestStream.instances = [];
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});
afterEach(() => {
  act(() => root.unmount());
  document.body.replaceChildren();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
async function mount() { await act(async () => root.render(<LandingProvider enabled><Probe /></LandingProvider>)); }
const shown = () => JSON.parse(container.querySelector("output")!.textContent!);
async function send(event: LandingLatencyEvent) { await act(async () => TestStream.instances[0].send(event)); }

it("uses fresh polling snapshots after a silent stream stall beyond three minutes", async () => {
  vi.spyOn(api, "landing").mockImplementation(async () => view([sample(24)]));
  await mount();
  await send({ revision: 1, reset: true, upserts: [sample(10)], removed: [] });
  expect(shown()[0].latencyMs).toBe(10);
  await act(async () => vi.advanceTimersByTimeAsync(195_000));
  expect(shown()).toEqual([sample(24)]);
  await send({ revision: 1, reset: true, upserts: [sample(30)], removed: [] });
  expect(shown()[0].latencyMs).toBe(30);
});

it("does not let a delayed poll resurrect a pair removed by a newer stream event", async () => {
  const request = vi.spyOn(api, "landing").mockResolvedValue(view([sample(10)]));
  await mount();
  let finish!: (value: LandingView) => void;
  request.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  await act(async () => container.querySelector("button")!.click());
  await send({ revision: 1, reset: false, upserts: [], removed: [{ nodeId: "entry", landingNodeId: "exit" }] });
  await act(async () => finish(view([sample(10)])));
  expect(shown()).toEqual([]);
});

it("replaces cached pairs on complete polls and rejects old-revision reconnect events", async () => {
  const request = vi.spyOn(api, "landing").mockResolvedValue(view([sample(10)]));
  await mount();
  await send({ revision: 1, reset: true, upserts: [sample(10)], removed: [] });
  request.mockResolvedValue(view([], 2));
  await act(async () => container.querySelector("button")!.click());
  await send({ revision: 1, reset: true, upserts: [sample(10)], removed: [] });
  expect(shown()).toEqual([]);
});

it("expires samples when both polling and streaming stop", async () => {
  const request = vi.spyOn(api, "landing").mockResolvedValue(view([sample(10)]));
  await mount();
  request.mockRejectedValue(new Error("offline"));
  await act(async () => vi.advanceTimersByTimeAsync(181_000));
  expect(shown()).toEqual([]);
});
