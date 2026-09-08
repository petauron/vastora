import { describe, expect, it } from "vitest";
import { landingLatencyColor } from "./landingLatency";

describe("landing latency colors", () => {
  it.each([0.1, 1, 19.99])("shows %s ms in green", (value) => {
    expect(landingLatencyColor(value)).toBe("text-latency-fast");
  });
  it.each([20, 50, 100])("shows %s ms in yellow", (value) => {
    expect(landingLatencyColor(value)).toBe("text-latency-medium");
  });
  it.each([100.01, 200])("shows %s ms in red", (value) => {
    expect(landingLatencyColor(value)).toBe("text-destructive");
  });
  it.each([null, undefined, NaN, Infinity, -1, 0])("keeps missing or invalid results neutral: %s", (value) => {
    expect(landingLatencyColor(value)).toBe("text-muted-foreground");
  });
});
