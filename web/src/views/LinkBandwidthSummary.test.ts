import { describe, expect, it } from "vitest";
import type { NodeDiagnosticCheck } from "../node-diagnostics-types";
import { linkBandwidthStatus, linkBandwidthStatusLabel } from "./LinkBandwidthSummary";

describe("link bandwidth evidence", () => {
  const now = Date.parse("2026-10-01T12:00:00Z");
  const check: NodeDiagnosticCheck = {
    id: "probe", agentId: "entry", landingNodeId: "landing", kind: "meridian.link-bandwidth", state: "succeeded", targetRevision: 2,
    checkedAt: "2026-10-01T11:00:00Z", updatedAt: "2026-10-01T11:00:00Z",
    link: { transportState: "direct", sourceNodeId: "entry", landingNodeId: "landing", uploadMbps: 25, downloadMbps: 50, uploadBytes: 31_250_000, downloadBytes: 62_500_000, uploadSeconds: 10, downloadSeconds: 10 },
  };
  it("requires current verified transport evidence", () => {
    expect(linkBandwidthStatus(check, now)).toBe("succeeded");
    expect(linkBandwidthStatus({ ...check, link: { ...check.link!, transportState: undefined } }, now)).toBe("unverified");
    for (const checkedAt of ["2026-09-29T00:00:00Z", "2026-10-02T00:00:00Z", "invalid"]) {
      expect(linkBandwidthStatus({ ...check, checkedAt }, now)).toBe("expired");
    }
  });
  it("never shows old throughput while active or failed", () => {
    expect(linkBandwidthStatus({ ...check, state: "running" }, now)).toBe("active");
    expect(linkBandwidthStatus({ ...check, error: "transport_not_direct" }, now)).toBe("non_direct");
    expect(linkBandwidthStatus({ ...check, error: "peer_identity_changed" }, now)).toBe("identity_changed");
    expect(linkBandwidthStatus({ ...check, state: "failed", error: "transport_unverified" }, now)).toBe("unverified");
    expect(linkBandwidthStatus({ ...check, state: "failed" }, now)).toBe("failed");
  });
  it("explains the actionable states in both languages", () => {
    expect(linkBandwidthStatusLabel("non_direct", "zh-CN")).toBe("私网未直连");
    expect(linkBandwidthStatusLabel("expired", "en")).toBe("Result expired");
    expect(linkBandwidthStatus(undefined, now)).toBe("missing");
  });
});
