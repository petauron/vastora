// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AppData } from "../types";
import { ThemeProvider } from "../components/theme";
import { AppsView } from "./AppsView";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  window.sessionStorage.clear();
  vi.restoreAllMocks();
});

function fixture(): AppData {
  const appKey = "vastora-official/pulse-agent";
  return {
    status: {}, centerUpdate: {}, systemDomain: {}, centerRemoteAccess: null,
    registryCredentials: [], sources: [], organizations: [], sites: [], routes: [],
    actions: [], integrations: [], threeXUIControllerMigrations: [],
    agents: [
      { id: "first", name: "Edge Node C", connected: true, credentialRevoked: false, siteId: "site", capabilities: { docker: true }, networkProfile: { serviceAddress: "10.0.0.1" } },
      { id: "failed-node", name: "Edge Node A", connected: true, credentialRevoked: false, siteId: "site", capabilities: { docker: true }, networkProfile: { serviceAddress: "10.0.0.2" } },
    ],
    apps: [{ key: appKey, sourceId: "vastora-official", fetchedAt: "2026-09-11T00:00:00Z", app: {
      id: "pulse-agent", version: "0.1.0-alpha.2", name: { en: "Collector", "zh-CN": "探针" },
      description: { en: "Host metrics", "zh-CN": "主机监控" }, hostAccess: true, config: [],
    } }],
    applications: [{ id: "monitor", appKey: "vastora-official/pulse", status: "running" }],
    services: [{ id: "dashboard", applicationId: "monitor", name: "dashboard", status: "ready" }],
    publications: [{ id: "private", serviceId: "dashboard", status: "ready", tlsEnabled: true, kind: "headscale_gateway" }],
    deployments: [{ id: "failed-install", agentId: "failed-node", appKey, appVersion: "0.1.0-alpha.2", state: "failed", operation: "install", deleteData: false, error: "agent: Pulse installation failed", createdAt: "2026-09-11T00:00:00Z", updatedAt: "2026-09-11T00:00:00Z" }],
  } as unknown as AppData;
}

function render(data: AppData) {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root?.render(<ThemeProvider><AppsView data={data} language="zh-CN" mutate={async (action) => { await action(); }} /></ThemeProvider>));
  return container;
}

describe("application operation history", () => {
  it("keeps a terminal deployment failure out of the live operations area", () => {
    const container = render(fixture());
    expect(container.textContent).not.toContain("最近操作");
    expect(container.textContent).not.toContain("Pulse installation failed");
    expect([...container.querySelectorAll("button")].some((value) => value.textContent?.trim() === "重试")).toBe(false);
  });

  it("still allows a fresh installation from the app store", () => {
    const container = render(fixture());
    const install = container.querySelector<HTMLButtonElement>('[aria-label="安装 探针"]');
    expect(install).not.toBeNull();
    act(() => install?.click());
    const selectedNode = document.querySelector<HTMLButtonElement>('[role="dialog"] [role="combobox"]');
    expect(selectedNode?.textContent).toContain("Edge Node C");
    expect(selectedNode?.disabled).toBe(false);
  });
});
