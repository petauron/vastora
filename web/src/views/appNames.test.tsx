// @vitest-environment jsdom
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { localized } from "./appAccess";
import { SenlunWorkspace } from "./SenlunWorkspace";
import { dashboard } from "./views.test-support";
import type { AppWorkspaceProps } from "@/app-workspaces/types";
import type { AppView } from "@/types";

function manifest(id: string, en: string, zh: string): AppView {
  const base = dashboard().apps[0];
  return { ...base, key: `vastora-official/${id}`, app: { ...base.app, id, name: { en, "zh-CN": zh } } };
}

describe("catalog-owned application names", () => {
  it.each([
    ["meridian", "Glivela", "Glivela"],
    ["pulse", "Senlun", "Senlun 监控主机"],
    ["pulse-agent", "Senlun Agent", "Senlun 探针"],
    ["meridian", "Future catalog name", "目录中的新名称"],
  ])("uses localized catalog names for %s without a brand override", (id, en, zh) => {
    const app = manifest(id, en, zh);
    expect(localized(app, "en", "name")).toBe(en);
    expect(localized(app, "zh-CN", "name")).toBe(zh);
  });

  it("uses the catalog name rather than a stale installation name in the monitoring workspace", () => {
    const data = dashboard();
    const app = manifest("pulse", "Catalog Monitor", "目录监控名称");
    const application = { ...data.applications[0], appKey: app.key, name: "Old installed name", status: "running" as const, updateAvailable: false };
    const props: AppWorkspaceProps = {
      data, language: "zh-CN", showSite: false, mutate: vi.fn(), onManage: vi.fn(), onUpgrade: vi.fn(), onClients: vi.fn(), onReality: vi.fn(),
      group: { id: app.key, appKey: app.key, app, legacyControllers: [], instances: [{ application, app, siteName: "Test", services: [], publications: [], realityServices: [], realityPublications: [], locked: false }] },
    };
    const html = renderToStaticMarkup(<SenlunWorkspace {...props} />);
    expect(html).toContain('aria-label="目录监控名称"');
    expect(html).not.toContain("Old installed name");
  });
});
