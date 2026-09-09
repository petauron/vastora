import type { LandingLatencyEvent, LandingLatencySnapshot, LandingLatencyView } from "../landing-types";

const key = (sample: Pick<LandingLatencyView, "nodeId" | "landingNodeId">) => JSON.stringify([sample.nodeId, sample.landingNodeId]);

export function applyLandingLatencyEvent(current: LandingLatencySnapshot | null, event: LandingLatencyEvent): LandingLatencySnapshot | null {
  if (!Number.isSafeInteger(event.revision) || event.revision < 0 || typeof event.reset !== "boolean" || !Array.isArray(event.upserts) || !Array.isArray(event.removed)) return current;
  if (current && event.revision < current.revision) return current;
  if (!event.reset && current?.revision !== event.revision) return current;
  const samples = new Map((event.reset ? [] : current?.samples ?? []).map((sample) => [key(sample), sample]));
  for (const removed of event.removed) samples.delete(key(removed));
  for (const sample of event.upserts) {
    if (typeof sample.nodeId !== "string" || typeof sample.landingNodeId !== "string" || !Number.isFinite(Date.parse(sample.checkedAt)) || !["direct", "unavailable"].includes(sample.state)) continue;
    const previous = samples.get(key(sample));
    if (!previous || Date.parse(sample.checkedAt) >= Date.parse(previous.checkedAt)) samples.set(key(sample), sample);
  }
  return { revision: event.revision, samples: [...samples.values()] };
}

// Also expire evidence locally when the stream is disconnected. Keep every
// still-fresh pair, rather than blanking the table on a transient network error.
export function freshLandingLatencies(samples: LandingLatencyView[], now = Date.now()): LandingLatencyView[] {
  const fresh = samples.filter((sample) => {
    const checkedAt = Date.parse(sample.checkedAt);
    return Number.isFinite(checkedAt) && now - checkedAt <= 45_000 && checkedAt - now <= 5_000;
  });
  return fresh.length === samples.length ? samples : fresh;
}
