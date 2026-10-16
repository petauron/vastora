import { describe, expect, it } from "vitest";
import { userError } from "./shared";

it("shows the recovery action needed instead of the generic conflict message", () => {
  const error = Object.assign(new Error("center: Glivela replacement identity or configuration changed; inspect it again before confirming"), { code: "conflict" });
  expect(userError("zh-CN", error)).toBe("节点身份或配置已变化，请重新检查并确认。");
  expect(userError("en", error)).toBe(error.message);
});

it("shows the new product label for the server's native runtime recovery error", () => {
  const error = new Error("center: restore the native Meridian runtime and wait for a fresh authenticated private identity report before recovering landing access");
  expect(userError("zh-CN", error)).toBe("请先恢复本机 Glivela，并等待节点上报最新私网身份，再恢复落地线路。");
});

describe("catalog task rejection guidance", () => {
  const message = "Refresh the app catalog and retry this operation.";

  it("shows actionable Chinese guidance without requiring technical details", () => {
    expect(userError("zh-CN", message)).toBe("请先在设置中刷新应用目录，再重试。");
  });

  it("shows the same guidance for a failed request", () => {
    expect(userError("en", new Error(message))).toBe("Refresh the app catalog in Settings, then retry.");
  });
});
