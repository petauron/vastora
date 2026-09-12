import { describe, expect, it } from "vitest";
import { userError } from "./shared";

describe("catalog task rejection guidance", () => {
  const message = "Refresh the app catalog and retry this operation.";

  it("shows actionable Chinese guidance without requiring technical details", () => {
    expect(userError("zh-CN", message)).toBe("请先在设置中刷新应用目录，再重试。");
  });

  it("shows the same guidance for a failed request", () => {
    expect(userError("en", new Error(message))).toBe("Refresh the app catalog in Settings, then retry.");
  });
});
