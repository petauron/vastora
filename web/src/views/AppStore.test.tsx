// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { AgentView, AppData, Application, AppView } from "../types";
import { AppStore, AppStoreCard } from "./AppStore";
import { AppHostAccessNote, AppIdentityBadge, isOfficialProduct } from "./AppIdentity";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function app(id = "pulse-agent", sourceId = "vastora-official"): AppView {
  return {
    key: `${sourceId}/${id}`, sourceId, fetchedAt: "2026-09-11T00:00:00Z",
    app: {
      id, version: "0.1.0-alpha.2", name: id === "pulse"
        ? { en: "Pulse", "zh-CN": "Pulse 监控主机" }
        : { en: "Pulse Agent", "zh-CN": "Pulse 探针" },
      description: { en: "Collect host metrics without Docker.", "zh-CN": "采集主机监控指标，无需 Docker。" },
      hostAccess: id !== "pulse", config: [],
    },
  };
}

function markup(node: Parameters<typeof renderToStaticMarkup>[0]) {
  const container = document.createElement("div");
  container.innerHTML = renderToStaticMarkup(node);
  return container;
}

describe("app identity", () => {
  it.each(["pulse", "pulse-agent"])("marks %s as official without a danger badge", (id) => {
    const value = app(id);
    const container = markup(<><AppIdentityBadge app={value} language="zh-CN" /><AppHostAccessNote app={value} language="zh-CN" /></>);
    expect(container.querySelector('[aria-label="Petauron 官方应用"]')?.textContent).toBe("官方");
    expect(container.textContent).not.toContain("高权限");
    expect(container.querySelector(".text-destructive")).toBeNull();
    if (id === "pulse-agent") expect(container.textContent).toContain("读取主机监控指标");
    expect(value.app.hostAccess).toBe(id === "pulse-agent");
  });

  it.each([
    app("komari-agent"),
    app("pulse-agent", "community"),
    { ...app(), key: "community/pulse-agent" },
    { ...app(), sourceId: "community" },
    { ...app(), app: { ...app().app, id: "another-app" } },
  ])("does not confuse catalog inclusion or copied names with official authorship: $key", (value) => {
    expect(isOfficialProduct(value)).toBe(false);
    const container = markup(<><AppIdentityBadge app={value} language="zh-CN" /><AppHostAccessNote app={value} language="zh-CN" /></>);
    expect(container.textContent).toContain("高权限");
    expect(container.textContent).toContain("请确认来源与用途");
    expect(container.querySelector('[aria-label="Petauron 官方应用"]')).toBeNull();
  });
});

describe("app store cards", () => {
  it("keeps full descriptions, a compact version line and a named install action", () => {
    const value = app("pulse");
    value.app.services = [{ name: "dashboard", protocol: "http", containerPort: 8080 }];
    const container = markup(<AppStoreCard app={value} language="zh-CN" installedCount={1} canInstall blocker="" onInstall={vi.fn()} />);
    expect(container.querySelector("h3")?.textContent).toBe("Pulse 监控主机");
    expect(container.textContent).toContain(value.app.description["zh-CN"]);
    expect(container.textContent).toContain("v0.1.0-alpha.2");
    expect(container.textContent).toContain("容器应用");
    expect(container.textContent).toContain("已安装到 1 个节点");
    expect(container.textContent).not.toContain("dashboard");
    expect(container.querySelector<HTMLButtonElement>("button")?.disabled).toBe(false);
    expect(container.querySelector("button")?.getAttribute("aria-label")).toBe("安装 Pulse 监控主机");
    expect(container.querySelector('[data-slot="card-header"] [data-slot="card-action"]')).toBeNull();
  });

  it("keeps blockers visible next to the disabled action and IDs unique across sources", () => {
    const container = markup(<>{[app(), app("pulse-agent", "community")].map((value) =>
      <AppStoreCard key={value.key} app={value} language="zh-CN" installedCount={0} canInstall={false} blocker="先配置访问入口" onInstall={vi.fn()} />
    )}</>);
    const buttons = [...container.querySelectorAll<HTMLButtonElement>("button")];
    const ids = buttons.map((button) => button.getAttribute("aria-describedby"));
    expect(new Set(ids).size).toBe(2);
    for (const button of buttons) {
      expect(button.disabled).toBe(true);
      const note = [...container.querySelectorAll("[id]")].find((element) => element.id === button.getAttribute("aria-describedby"));
      expect(note?.textContent).toBe("先配置访问入口");
    }
  });

  it("does not bypass the private access prerequisite for the official collector", () => {
    const data = {
      apps: [app()],
      agents: [{ id: "collector", connected: true, capabilities: { docker: false }, networkProfile: { serviceAddress: "10.0.0.2" } } as AgentView],
      applications: [{ id: "monitor", appKey: "vastora-official/pulse", status: "running", installedVersion: "0.1.0-alpha.2" } as Application],
      services: [], publications: [],
    } as unknown as AppData;
    const container = markup(<AppStore data={data} language="zh-CN" onInstall={vi.fn()} />);
    expect(container.querySelector<HTMLButtonElement>("button")?.disabled).toBe(true);
    expect(container.textContent).toContain("私网 HTTPS 入口");
    expect(container.textContent).toContain("官方");
  });

  it("uses English identity and neutral host-access copy", () => {
    const container = markup(<AppStoreCard app={app()} language="en" installedCount={0} canInstall blocker="" onInstall={vi.fn()} />);
    expect(container.textContent).toContain("Official");
    expect(container.textContent).toContain("Host app");
    expect(container.textContent).not.toContain("Privileged");
    expect(container.textContent).toContain("Not installed");
  });

  it("passes the exact app to installation and prevents disabled clicks", () => {
    const container = document.createElement("div");
    const root = createRoot(container);
    const onInstall = vi.fn();
    const value = app();
    try {
      act(() => root.render(<AppStoreCard app={value} language="zh-CN" installedCount={0} canInstall blocker="" onInstall={onInstall} />));
      act(() => container.querySelector("button")?.click());
      expect(onInstall).toHaveBeenCalledTimes(1);
      expect(onInstall).toHaveBeenCalledWith(value);
      act(() => root.render(<AppStoreCard app={value} language="zh-CN" installedCount={0} canInstall={false} blocker="先配置访问入口" onInstall={onInstall} />));
      act(() => container.querySelector("button")?.click());
      expect(onInstall).toHaveBeenCalledTimes(1);
    } finally {
      act(() => root.unmount());
    }
  });
});
