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
    key: `${sourceId}/${id}`, sourceId, fetchedAt: "2026-09-11T00:00:00Z", manifestSha256: "a".repeat(64),
    app: {
      id, version: "0.1.0-alpha.2", packageRevision: 1, runtime: { kind: id === "pulse" ? "docker" : "systemd", version: 1 }, name: id === "pulse"
        ? { en: "Pulse", "zh-CN": "Pulse 监控主机" }
        : { en: "Pulse Agent", "zh-CN": "Pulse 探针" },
      description: { en: "Collect host metrics without Docker.", "zh-CN": "采集主机监控指标，无需 Docker。" },
      hostAccess: id !== "pulse", config: [],
    },
  };
}

const installSelector = 'button[aria-label^="安装 "], button[aria-label^="Install "]';

async function inspectDetails(value: AppView, language: "zh-CN" | "en", installedCount: number, inspect: (details: HTMLElement) => void) {
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  try {
    await act(async () => root.render(<AppStoreCard app={value} language={language} installedCount={installedCount} canInstall blocker="" onInstall={vi.fn()} />));
    expect(container.querySelector('[data-slot="sheet-content"]')).toBeNull();
    await act(async () => container.querySelector<HTMLButtonElement>(".store-app-title")!.click());
    inspect(document.body.querySelector<HTMLElement>('[role="dialog"]')!);
  } finally {
    await act(async () => root.unmount());
    container.remove();
  }
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
  it("keeps same-name apps attributable to their catalog namespaces", () => {
    const values = [app("pulse"), app("pulse", "community")];
    const container = markup(<>{values.map(value => <AppStoreCard key={value.key} app={value} language="zh-CN" installedCount={0} canInstall blocker="" onInstall={vi.fn()} />)}</>);
    const origins = [...container.querySelectorAll('[aria-label="目录来源"]')].map(value => value.textContent);
    expect(origins).toEqual(["community · "]);
    expect(container.querySelectorAll(".store-app-title")).toHaveLength(2);
  });

  it("explains why an empty official store cannot install before first verification", () => {
    const data = { apps: [], applications: [], sources: [{ id: "vastora-official", status: "pending" }] } as unknown as AppData;
    const container = markup(<AppStore data={data} language="zh-CN" onInstall={vi.fn()} />);
    expect(container.querySelector('[role="status"]')?.textContent).toContain("官方目录等待首次验证");
    expect(container.textContent).toContain("控制面板刷新应用目录");
    expect(container.querySelector("button")).toBeNull();
  });

  it.each(["2020-01-01T00:00:00Z", "invalid-date"])("blocks a stale view with an expired or invalid catalog expiry: %s", (catalogExpiresAt) => {
    const container = markup(<AppStoreCard app={{ ...app(), catalogExpiresAt }} language="zh-CN" installedCount={0} canInstall blocker="" onInstall={vi.fn()} />);
    expect(container.querySelector<HTMLButtonElement>(installSelector)?.disabled).toBe(true);
    expect(container.textContent).toContain("刷新应用目录");
  });

  it("blocks installation from expired catalog even with eligible nodes", () => {
    const value = { ...app(), installBlocked: true };
    const container = markup(<AppStoreCard app={value} language="zh-CN" installedCount={1} canInstall blocker="" onInstall={vi.fn()} />);
    const button = container.querySelector<HTMLButtonElement>(installSelector);
    expect(button?.disabled).toBe(true);
    expect(container.textContent).toContain("刷新应用目录");
    expect(container.textContent).toContain("已安装应用不受影响");
    expect(button?.getAttribute("aria-describedby")).toBeTruthy();
  });
  it("keeps the grid compact and full metadata available in app details", async () => {
    const value = app("pulse");
    value.app.services = [{ name: "dashboard", protocol: "http", containerPort: 8080 }];
    const container = markup(<AppStoreCard app={value} language="zh-CN" installedCount={1} canInstall blocker="" onInstall={vi.fn()} />);
    expect(container.querySelector("h3")?.textContent).toBe("Pulse 监控主机");
    expect(container.textContent).toContain("已安装到 1 个节点");
    expect(container.textContent).not.toContain("v0.1.0-alpha.2");
    expect(container.querySelector<HTMLButtonElement>(installSelector)?.disabled).toBe(false);
    expect(container.querySelector(installSelector)?.getAttribute("aria-label")).toBe("安装 Pulse 监控主机");
    await inspectDetails(value, "zh-CN", 1, (details) => {
      expect(details.textContent).toContain(value.app.description["zh-CN"]);
      expect(details.textContent).toContain("v0.1.0-alpha.2 · r1");
      expect(details.textContent).toContain("容器应用");
      expect(details.textContent).toContain("已安装到 1 个节点");
      expect(details.textContent).toContain("官方目录");
      expect(details.querySelector('[aria-label="Petauron 官方应用"]')).not.toBeNull();
      expect(details.textContent).not.toContain("dashboard");
    });
  });

  it("keeps blockers visible next to the disabled action and IDs unique across sources", () => {
    const container = markup(<>{[app(), app("pulse-agent", "community")].map((value) =>
      <AppStoreCard key={value.key} app={value} language="zh-CN" installedCount={0} canInstall={false} blocker="先配置访问入口" onInstall={vi.fn()} />
    )}</>);
    const buttons = [...container.querySelectorAll<HTMLButtonElement>(installSelector)];
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
      sources: [],
      agents: [{ id: "collector", connected: true, credentialRevoked: false, capabilities: { docker: false }, networkProfile: { serviceAddress: "10.0.0.2" } } as AgentView],
      applications: [{ id: "monitor", appKey: "vastora-official/pulse", status: "running", installedVersion: "0.1.0-alpha.2" } as Application],
      services: [], publications: [],
    } as unknown as AppData;
    const container = markup(<AppStore data={data} language="zh-CN" onInstall={vi.fn()} />);
    expect(container.querySelector<HTMLButtonElement>(installSelector)?.disabled).toBe(true);
    expect(container.textContent).toContain("私网 HTTPS 入口");
  });

  it("uses English identity and neutral host-access copy in details", async () => {
    await inspectDetails(app(), "en", 0, (details) => {
      expect(details.textContent).toContain("Official");
      expect(details.textContent).toContain("Host app");
      expect(details.textContent).not.toContain("Privileged");
      expect(details.textContent).toContain("Not installed");
    });
  });

  it("retains third-party source and host permissions in details", async () => {
    await inspectDetails(app("pulse-agent", "community"), "zh-CN", 0, (details) => {
      expect(details.textContent).toContain("第三方目录 · community");
      expect(details.textContent).toContain("高权限");
      expect(details.textContent).toContain("请确认来源与用途");
      expect(details.querySelector('[aria-label="Petauron 官方应用"]')).toBeNull();
    });
  });

  it("keeps unsupported package reasons visible while other catalog entries remain installable", () => {
    const supported = app("new-native", "community");
    const unsupported = app("new-future", "community");
    unsupported.app.runtime!.requiredCapabilities = ["future-device"];
    const data = { apps: [supported, unsupported], sources: [], applications: [], services: [], publications: [], agents: [{ id: "node", name: "Node", connected: true, capabilities: { executorVersions: { systemd: 1 }, runtimeCapabilities: [] }, networkProfile: { serviceAddress: "10.0.0.2" } }] } as unknown as AppData;
    const container = markup(<AppStore data={data} language="en" onInstall={vi.fn()} />);
    const buttons = [...container.querySelectorAll<HTMLButtonElement>(installSelector)];
    expect(buttons).toHaveLength(2);
    expect(buttons.map(button => button.disabled)).toEqual([false, true]);
    expect(container.textContent).toContain("Missing node capabilities: future-device");
  });

  it("passes the exact app to installation and prevents disabled clicks", () => {
    const container = document.createElement("div");
    const root = createRoot(container);
    const onInstall = vi.fn();
    const value = app();
    try {
      act(() => root.render(<AppStoreCard app={value} language="zh-CN" installedCount={0} canInstall blocker="" onInstall={onInstall} />));
      act(() => container.querySelector<HTMLButtonElement>(installSelector)?.click());
      expect(onInstall).toHaveBeenCalledTimes(1);
      expect(onInstall).toHaveBeenCalledWith(value);
      act(() => root.render(<AppStoreCard app={value} language="zh-CN" installedCount={0} canInstall={false} blocker="先配置访问入口" onInstall={onInstall} />));
      act(() => container.querySelector<HTMLButtonElement>(installSelector)?.click());
      expect(onInstall).toHaveBeenCalledTimes(1);
    } finally {
      act(() => root.unmount());
    }
  });
});
