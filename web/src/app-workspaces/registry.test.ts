import { describe, expect, it } from "vitest";
import { appWorkspace } from "./registry";

describe("application UI ownership", () => {
  it("selects the reviewed module by exact catalog source and app identity", () => {
    expect(appWorkspace("vastora-official/meridian")?.manifest.appKey).toBe("vastora-official/meridian");
    expect(appWorkspace("untrusted/meridian")).toBeUndefined();
    expect(appWorkspace("meridian")).toBeUndefined();
    expect(appWorkspace("https://example.com/meridian.js")).toBeUndefined();
  });
  it("defines navigation and capabilities in the application manifest", () => {
    const pages = appWorkspace("vastora-official/meridian")!.manifest.pages;
    expect(pages.filter((page) => page.surface === "tab").map((page) => page.id)).toEqual(["nodes", "network"]);
    expect(pages.find((page) => page.surface === "manager")?.capabilities).toContain("accounts");
    expect(new Set(pages.map((page) => page.id)).size).toBe(pages.length);
  });
});
