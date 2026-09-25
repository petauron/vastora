// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import type { IPQualityAssessment } from "../ip-quality-types";
import { api } from "../api";
import { AssessmentSummary, assessmentLabel } from "./IPAssessment";
import { IPQualityComparison } from "./IPQualityComparison";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

function assessment(): IPQualityAssessment {
  return {
    version: "meridian-v1", status: "partial", min: 75, max: 100, grade: "unknown", ipType: "residential",
    typeCandidates: ["residential"], typeEvidence: [{ source: "IPinfo", value: "ISP" }],
    contributions: [{ id: "ippure", min: 0, max: 25, weight: 25, missing: ["IPPure"] }], missing: ["IPPure"],
    advice: "recheck", reasons: ["incomplete"], requiredFailed: [], requiredUnknown: [],
    preferences: { requiredServices: ["ChatGPT", "Netflix", "DisneyPlus"], targetRegion: "" },
  };
}

it("keeps missing evidence as an interval and reveals policy only on expansion", async () => {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root?.render(<AssessmentSummary language="zh-CN" assessment={assessment()} />));
  expect(container.textContent).toContain("暂评 75～100");
  expect(container.textContent).not.toContain("Meridian 评分 v1");
  const trigger = container.querySelector<HTMLButtonElement>("button[aria-expanded]")!;
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  await act(async () => trigger.click());
  expect(trigger.getAttribute("aria-expanded")).toBe("true");
  expect(container.textContent).toContain("Meridian 评分 v1");
  expect(container.textContent).toContain("IPPure");
  expect(container.textContent).toContain("0～25 / 25");
});

it("does not display a formal number for expired or changed-IP results", () => {
  expect(assessmentLabel("zh-CN", { ...assessment(), status: "expired" })).toBe("结果已过期");
  expect(assessmentLabel("zh-CN", { ...assessment(), status: "ip_changed" })).toBe("IP 已变化");
});

it("loads comparisons on demand, separates partial rows, and only selects a form value", async () => {
  const quality = vi.spyOn(api, "ipQuality").mockResolvedValue({ checks: [], comparisons: [{
    nodeId: "landing", name: "落地 A", compatible: false, connectionVerified: false, recommended: false, reason: "connection_unverified", assessment: assessment(), services: [],
  }] });
  const mutate = vi.spyOn(api, "selectLanding");
  const select = vi.fn();
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root?.render(<IPQualityComparison language="zh-CN" nodeId="entry" onSelect={select} />));
  expect(quality).not.toHaveBeenCalled();
  await act(async () => container.querySelector<HTMLButtonElement>("button[aria-expanded]")!.click());
  expect(quality).toHaveBeenCalledWith(expect.any(AbortSignal), expect.objectContaining({ targetRegion: "" }), "entry");
  expect(container.textContent).toContain("暂评 / 待复测");
  expect(container.textContent).toContain("连接待验证");
  const choose = Array.from(container.querySelectorAll("button")).find((button) => button.textContent === "选择")!;
  await act(async () => choose.click());
  expect(select).toHaveBeenCalledWith("landing");
  expect(mutate).not.toHaveBeenCalled();
});
