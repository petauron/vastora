import { expect, it } from "vitest";
import type { LandingLatencyView } from "../landing-types";
import { applyLandingLatencyEvent, freshLandingLatencies } from "./landingLatencyEvents";

it("merges by source and exit, retaining unchanged objects and rejecting old revisions", () => {
  const now = new Date().toISOString();
  const a: LandingLatencyView = { nodeId: "one", landingNodeId: "a", state: "direct", latencyMs: 12, checkedAt: now };
  const b: LandingLatencyView = { ...a, nodeId: "two", latencyMs: 88 };
  const initial = applyLandingLatencyEvent(null, { revision: 4, reset: true, upserts: [a, b], removed: [] })!;
  const changed = applyLandingLatencyEvent(initial, { revision: 4, reset: false, upserts: [{ ...a, latencyMs: 20 }], removed: [] })!;
  expect(changed.samples[0].latencyMs).toBe(20);
  expect(changed.samples[1]).toBe(b);
  expect(applyLandingLatencyEvent(changed, { revision: 3, reset: true, upserts: [a], removed: [] })).toBe(changed);
  const removed = applyLandingLatencyEvent(changed, { revision: 4, reset: false, upserts: [], removed: [a] })!;
  expect(removed.samples).toEqual([b]);
  const reset = applyLandingLatencyEvent(changed, { revision: 5, reset: true, upserts: [], removed: [] })!;
  expect(reset.samples).toEqual([]);
  expect(applyLandingLatencyEvent(reset, { revision: 6, reset: false, upserts: [a], removed: [] })).toBe(reset);
});

it("expires individual samples without allocating when everything remains fresh", () => {
  const now = Date.now();
  const fresh: LandingLatencyView = { nodeId: "one", landingNodeId: "a", state: "direct", latencyMs: 12, checkedAt: new Date(now).toISOString() };
  const samples = [fresh];
  expect(freshLandingLatencies(samples, now)).toBe(samples);
  expect(freshLandingLatencies([fresh, { ...fresh, nodeId: "two", checkedAt: new Date(now-46_000).toISOString() }], now)).toEqual(samples);
});
