import { describe, expect, it } from "vitest";
import { landingLatencyColor } from "./landingLatency";

describe("landing latency colors", () => {
  it.each([0, 0.1, 1, 19.99])("marks %s ms green", (value) => {
    expect(landingLatencyColor(value)).toBe("text-latency-fast");
  });
  it.each([20, 88, 100])("marks %s ms yellow", (value) => {
    expect(landingLatencyColor(value)).toBe("text-latency-medium");
  });
  it.each([100.01, 129, 157, 200])("marks %s ms red", (value) => {
    expect(landingLatencyColor(value)).toBe("text-destructive");
  });
  it.each([null, undefined, NaN, Infinity, -1])("keeps missing or invalid results neutral: %s", (value) => {
    expect(landingLatencyColor(value)).toBe("text-muted-foreground");
  });
});
