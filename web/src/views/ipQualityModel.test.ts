import { describe, expect, it } from "vitest";
import { cleanIPQualityValue, ipClassification, ipQualitySummary, unlockLabel, unlockTypeLabel } from "./ipQualityModel";
import type { IPQualityCheck } from "../ip-quality-types";

describe("IP quality presentation", () => {
  it("removes terminal color codes from saved classification labels", () => {
    expect(cleanIPQualityValue("x1b[43mx1b[37mx1b[1m Business x1b[0m")).toBe("Business");
  });
  it("uses NodeQuality's Chinese category labels and retains unfamiliar values", () => {
    expect(ipClassification("zh-CN", "x1b[43m Business x1b[0m")).toEqual({ label: "商业", tone: "warning" });
    expect(ipClassification("zh-CN", "ISP")).toEqual({ label: "家宽", tone: "good" });
    expect(ipClassification("zh-CN", "Hosting")).toEqual({ label: "机房", tone: "bad" });
    expect(ipClassification("zh-CN", "Unlisted type")).toEqual({ label: "Unlisted type", tone: "neutral" });
  });
  it("does not mistake missing results for unlocked", () => {
    expect(unlockLabel("zh-CN", "null")).toBe("未知");
    expect(unlockLabel("zh-CN", " Yes ")).toBe("解锁");
    expect(unlockLabel("zh-CN", "No")).toBe("未解锁");
  });
  it("removes terminal formatting in detail and list labels", () => {
    const status = "x1b[42mx1b[37m Yes x1b[0m";
    const type = "\u001b[42m\u001b[37m Native \u001b[0m";
    expect(unlockLabel("zh-CN", status)).toBe("解锁");
    expect(unlockTypeLabel("zh-CN", type)).toBe("原生");
    const check: IPQualityCheck = { agentId: "node", id: "check", state: "succeeded", stale: false, updatedAt: "", report: { address: "203.0.113.8", version: "test", scores: [], services: [{ name: "Netflix", status }, { name: "ChatGPT", status }] } };
    expect(ipQualitySummary("zh-CN", check)).toBe("Netflix 解锁 · ChatGPT 解锁");
  });
  it("labels pending, failed and stale results without claiming success", () => {
    const check: IPQualityCheck = { agentId: "node", id: "check", state: "succeeded", stale: true, updatedAt: "", report: { address: "203.0.113.8", version: "test", scores: [], services: [{ name: "Netflix", status: "Yes" }] } };
    expect(ipQualitySummary("zh-CN", check)).toContain("需重新检测");
    expect(ipQualitySummary("zh-CN", { ...check, state: "running" })).toContain("检测中");
    expect(ipQualitySummary("zh-CN", { ...check, stale: false, error: "timeout" })).toContain("检测未完成");
  });
});
