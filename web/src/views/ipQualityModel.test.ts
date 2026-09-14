import { describe, expect, it } from "vitest";
import { ipQualitySummary, unlockLabel } from "./ipQualityModel";
import type { IPQualityCheck } from "../ip-quality-types";

describe("IP quality presentation", () => {
  it("does not mistake missing results for unlocked", () => {
    expect(unlockLabel("zh-CN", "null")).toBe("未知");
    expect(unlockLabel("zh-CN", " Yes ")).toBe("解锁");
    expect(unlockLabel("zh-CN", "No")).toBe("未解锁");
  });
  it("labels pending, failed and stale results without claiming success", () => {
    const check: IPQualityCheck = { agentId: "node", id: "check", state: "succeeded", stale: true, updatedAt: "", report: { address: "203.0.113.8", version: "test", scores: [], services: [{ name: "Netflix", status: "Yes" }] } };
    expect(ipQualitySummary("zh-CN", check)).toContain("需重新检测");
    expect(ipQualitySummary("zh-CN", { ...check, state: "running" })).toContain("检测中");
    expect(ipQualitySummary("zh-CN", { ...check, stale: false, error: "timeout" })).toContain("检测未完成");
  });
});
