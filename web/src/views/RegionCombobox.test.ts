import { describe, expect, it } from "vitest";
import { regionBaseName, regionDisplayName } from "./RegionCombobox";

describe("subscription region names", () => {
  it("uses the short Hong Kong prefix without changing the node name", () => {
    expect(regionDisplayName(" hk ", " | VMISS DC2 ")).toBe("🇭🇰 香港| VMISS DC2");
  });

  it.each(["🇭🇰 中国香港特别行政区| VMISS DC2", "🇭🇰 香港| VMISS DC2"])(
    "keeps the node name when editing %s",
    (displayName) => {
      const name = regionBaseName(displayName, "HK");
      expect(name).toBe("| VMISS DC2");
      expect(regionDisplayName("HK", name)).toBe("🇭🇰 香港| VMISS DC2");
    },
  );

  it("does not change another region or an unprefixed node name", () => {
    expect(regionDisplayName("US", "Oracle 9929")).toBe("🇺🇸 美国Oracle 9929");
    expect(regionBaseName("VMISS DC2", "HK")).toBe("VMISS DC2");
  });

  it.each([
    ["AE", "🇦🇪", "阿拉伯联合酋长国", "阿联酋"],
    ["BA", "🇧🇦", "波斯尼亚和黑塞哥维那", "波黑"],
    ["MO", "🇲🇴", "中国澳门特别行政区", "澳门"],
    ["PG", "🇵🇬", "巴布亚新几内亚", "巴新"],
    ["PS", "🇵🇸", "巴勒斯坦领土", "巴勒斯坦"],
    ["DO", "🇩🇴", "多米尼加共和国", "多米尼加"],
    ["VG", "🇻🇬", "英属维尔京群岛", "英属维尔京"],
    ["VI", "🇻🇮", "美属维尔京群岛", "美属维尔京"],
    ["GS", "🇬🇸", "南乔治亚和南桑威奇群岛", "GS"],
  ])("shortens %s without retaining pieces of the original prefix", (code, flag, fullName, shortName) => {
    const displayName = `${flag} ${shortName}| Edge`;
    expect(regionDisplayName(code, "| Edge")).toBe(displayName);
    expect(regionBaseName(`${flag} ${fullName}| Edge`, code)).toBe("| Edge");
    expect(regionBaseName(displayName, code)).toBe("| Edge");
    expect(regionBaseName(`${flag} ${code} · Edge`, code)).toBe("Edge");
  });

  it("retains meaningful distinctions instead of truncating similar names", () => {
    expect(regionDisplayName("DM", "| Edge")).toBe("🇩🇲 多米尼克| Edge");
    expect(regionDisplayName("CD", "| Edge")).toBe("🇨🇩 刚果（金）| Edge");
    expect(regionDisplayName("CG", "| Edge")).toBe("🇨🇬 刚果（布）| Edge");
  });
});
