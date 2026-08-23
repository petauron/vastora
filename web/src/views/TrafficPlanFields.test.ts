import { describe, expect, it } from "vitest";
import { dateInputValueInTimeZone, endOfDayEpochInTimeZone } from "./TrafficPlanFields";

describe("traffic plan site timezone dates", () => {
  it("keeps the selected calendar day independent of the browser timezone", () => {
    const epoch = endOfDayEpochInTimeZone("2026-08-23", "Asia/Singapore");

    expect(new Date(epoch).toISOString()).toBe("2026-08-23T15:59:59.000Z");
    expect(dateInputValueInTimeZone(epoch, "Asia/Singapore")).toBe("2026-08-23");
  });
});
