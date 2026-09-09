import { describe, expect, it } from "vitest";
import { accessSessionOptions, validAccessSessionDuration } from "./access-session";

describe("Access session durations", () => {
  it("offers bounded Cloudflare durations and a 24-hour default", () => {
    expect(accessSessionOptions("zh-CN").find((option) => option.value === "24h")?.label).toContain("默认");
    expect(accessSessionOptions("en").find((option) => option.value === "24h")?.label).toContain("default");
    for (const option of accessSessionOptions("zh-CN")) expect(validAccessSessionDuration(option.value)).toBe(true);
  });

  it.each(["", "-1h", "0s", "24", "forever", "731h", "<script>"])("rejects unsupported input %s", (value) => {
    expect(validAccessSessionDuration(value)).toBe(false);
  });
});
