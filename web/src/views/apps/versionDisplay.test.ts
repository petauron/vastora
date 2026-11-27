import { describe, expect, it } from "vitest";
import {
  formatUpgradeTarget,
  formatUpgradeTransition,
  isSameVersionRevisionUpgrade,
} from "./versionDisplay";

describe("same-version package revision display", () => {
  it("formats same-version revision upgrades with explicit revision diff", () => {
    expect(isSameVersionRevisionUpgrade({
      fromVersion: "0.2.1",
      toVersion: "0.2.1",
      fromRevision: 9,
      toRevision: 10,
    })).toBe(true);
    expect(formatUpgradeTransition({
      fromVersion: "0.2.1",
      toVersion: "0.2.1",
      fromRevision: 9,
      toRevision: 10,
    })).toBe("0.2.1 (r9 → r10)");
    expect(formatUpgradeTransition({
      fromVersion: "0.2.1",
      toVersion: "0.2.1",
      fromRevision: 9,
      toRevision: 10,
      prefix: "v",
    })).toBe("v0.2.1 (r9 → r10)");
    expect(formatUpgradeTransition({
      fromVersion: "0.2.1",
      toVersion: "0.2.1",
      fromRevision: 0,
      toRevision: 1,
    })).toBe("0.2.1 (r0 → r1)");
    expect(formatUpgradeTarget({
      fromVersion: "0.2.1",
      toVersion: "0.2.1",
      fromRevision: 9,
      toRevision: 10,
    })).toBe("v0.2.1 (r9 → r10)");
  });

  it("keeps cross-version upgrades concise without revision noise", () => {
    expect(isSameVersionRevisionUpgrade({
      fromVersion: "0.2.0",
      toVersion: "0.2.1",
      fromRevision: 8,
      toRevision: 10,
    })).toBe(false);
    expect(formatUpgradeTransition({
      fromVersion: "0.2.0",
      toVersion: "0.2.1",
      fromRevision: 8,
      toRevision: 10,
      prefix: "v",
    })).toBe("v0.2.0 → v0.2.1");
    expect(formatUpgradeTransition({
      fromVersion: "0.2.0",
      toVersion: "0.2.1",
      fromRevision: 8,
      toRevision: 10,
    })).toBe("0.2.0 → 0.2.1");
    expect(formatUpgradeTarget({
      fromVersion: "0.2.0",
      toVersion: "0.2.1",
      fromRevision: 8,
      toRevision: 10,
    })).toBe("v0.2.1");
  });

  it("falls back safely when revision metadata is equal, missing, or invalid", () => {
    expect(formatUpgradeTransition({
      fromVersion: "0.2.1",
      toVersion: "0.2.1",
      fromRevision: 10,
      toRevision: 10,
    })).toBe("0.2.1 → 0.2.1");
    expect(formatUpgradeTransition({
      fromVersion: "0.2.1",
      toVersion: "0.2.1",
      fromRevision: undefined,
      toRevision: 10,
      prefix: "v",
    })).toBe("v0.2.1 → v0.2.1");
    expect(formatUpgradeTransition({
      fromVersion: undefined,
      toVersion: "0.2.1",
      fromRevision: 9,
      toRevision: 10,
    })).toBe("— → 0.2.1");
    expect(formatUpgradeTarget({
      fromVersion: "0.2.1",
      toVersion: "0.2.1",
      fromRevision: 9,
      toRevision: 10,
      prefix: "",
    })).toBe("0.2.1 (r9 → r10)");
    expect(formatUpgradeTarget({
      fromVersion: "0.2.1",
      toVersion: "0.2.1",
      fromRevision: -1,
      toRevision: 10,
    })).toBe("v0.2.1");
    expect(formatUpgradeTarget({
      fromVersion: "0.2.1",
      toVersion: "0.2.1",
      fromRevision: Number.NaN,
      toRevision: 10,
    })).toBe("v0.2.1");
    expect(formatUpgradeTransition({
      fromVersion: "0.2.1",
      toVersion: "0.2.1",
      fromRevision: 1.5,
      toRevision: 10,
    })).toBe("0.2.1 → 0.2.1");
    expect(formatUpgradeTarget({
      fromVersion: undefined,
      toVersion: undefined,
    })).toBe("—");
  });
});
