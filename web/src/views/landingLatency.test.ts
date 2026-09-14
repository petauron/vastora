import { describe, expect, it } from "vitest";
import { landingLatencyColor } from "./landingLatency";

describe("landing latency colors", () => {
  it.each([0, 0.1, 1, 20, 100, 129, 157, 200])("does not treat %s ms as a failure", (value) => {
    expect(landingLatencyColor(value)).toBe("text-foreground");
  });
  it.each([null, undefined, NaN, Infinity, -1])("keeps missing or invalid results neutral: %s", (value) => {
    expect(landingLatencyColor(value)).toBe("text-muted-foreground");
  });
});
