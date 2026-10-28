// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AppData } from "../types";
import { ThemeProvider } from "../components/theme";
import { APIError, api } from "../api";
import { AppsView } from "./AppsView";
import { emptyAppData } from "../app-data";

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
    meridian: emptyAppData({ version: "test", agentInstallerAvailable: true, agentConnectionMode: "lan", agentConnectUrl: "https://center.example.test" }).meridian,
    status: {}, centerUpdate: {}, systemDomain: {}, centerRemoteAccess: null,
    registryCredentials: [], sources: [], organizations: [], sites: [], routes: [],
    actions: [], integrations: [], threeXUIControllerMigrations: [],
    agents: [
      { id: "first", name: "Edge Node C", connected: true, credentialRevoked: false, siteId: "site", capabilities: { docker: true, executorVersions: { docker: 1, systemd: 1 }, runtimeCapabilities: [] }, networkProfile: { serviceAddress: "10.0.0.1" } },
      { id: "failed-node", name: "Edge Node A", connected: true, credentialRevoked: false, siteId: "site", capabilities: { docker: true, executorVersions: { docker: 1, systemd: 1 }, runtimeCapabilities: [] }, networkProfile: { serviceAddress: "10.0.0.2" } },
    ],
    apps: [{ key: appKey, sourceId: "vastora-official", fetchedAt: "2026-09-11T00:00:00Z", manifestSha256: "a".repeat(64), app: {
      id: "pulse-agent", version: "0.1.0-alpha.2", packageRevision: 1, runtime: { kind: "systemd", version: 1 }, name: { en: "Collector", "zh-CN": "探针" },
      description: { en: "Host metrics", "zh-CN": "主机监控" }, hostAccess: true, config: [],
    } }],
    applications: [{ id: "monitor", appKey: "vastora-official/pulse", status: "running" }],
    services: [{ id: "dashboard", applicationId: "monitor", name: "dashboard", status: "ready" }],
    publications: [{ id: "private", serviceId: "dashboard", status: "ready", tlsEnabled: true, kind: "headscale_gateway" }],
    deployments: [{ id: "failed-install", agentId: "failed-node", appKey, appVersion: "0.1.0-alpha.2", state: "failed", operation: "install", deleteData: false, error: "agent: Senlun installation failed", createdAt: "2026-09-11T00:00:00Z", updatedAt: "2026-09-11T00:00:00Z" }],
  } as unknown as AppData;
}

function render(data: AppData) {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root?.render(<ThemeProvider><AppsView workspaceKey={null} onOpenApp={vi.fn()} onStore={vi.fn()} onSettings={vi.fn()} data={data} language="zh-CN" mutate={async (action) => { await action(); }} /></ThemeProvider>));
  return container;
}

describe("application operation history", () => {
  it("updates an installed app directly without reopening the installation form", async () => {
    const data = fixture();
    data.applications = [{ ...data.applications[0], id: "installed-agent", nodeId: "first", siteId: "site", appKey: data.apps[0].key, name: "Collector", status: "running", installedVersion: "0.1.0-alpha.1", updateAvailable: true }];
    data.deployments = [];
    const create = vi.spyOn(api, "createDeployment").mockResolvedValue({ id: "update", state: "pending" } as never);
    const container = render(data);
    const manage = container.querySelector<HTMLButtonElement>('[aria-label="管理 探针"]');
    expect(manage).not.toBeNull();
    act(() => manage?.click());
    const update = document.querySelector<HTMLButtonElement>('[aria-label="更新 Edge Node C 的应用"]');
    expect(update).not.toBeNull();
    await act(async () => { update?.click(); update?.click(); });
    expect(create).toHaveBeenCalledTimes(1);
    expect(create).toHaveBeenCalledWith("first", data.apps[0].key, {}, "upgrade", false, undefined, undefined, undefined, undefined, { packageRevision: 1, manifestSha256: "a".repeat(64) });
    expect(document.getElementById("deployment-agent")).toBeNull();
  });

  it("keeps an upgrade rejection inline and never retries automatically", async () => {
    const data = fixture();
    data.applications = [{ ...data.applications[0], id: "installed-agent", nodeId: "first", siteId: "site", appKey: data.apps[0].key, name: "Collector", status: "running", installedVersion: "0.1.0-alpha.1", updateAvailable: true }];
    data.deployments = [];
    const create = vi.spyOn(api, "createDeployment").mockRejectedValue(new APIError("catalog changed; refresh before updating", 409));
    const container = render(data);
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="管理 探针"]')?.click());
    await act(async () => document.querySelector<HTMLButtonElement>('[aria-label="更新 Edge Node C 的应用"]')?.click());
    expect(create).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toContain("更新请求需要检查");
    expect(document.getElementById("deployment-agent")).toBeNull();
    expect(document.body.textContent).not.toContain("更新成功");
  });

  it("routes pending adoption to resource management without creating an upgrade", async () => {
    const data = fixture();
    data.applications = [{ ...data.applications[0], id: "installed-agent", nodeId: "first", siteId: "site", appKey: data.apps[0].key, name: "Collector", status: "running", adoptionState: "pending", installedVersion: "0.1.0-alpha.1", updateAvailable: true }];
    data.deployments = [];
    const create = vi.spyOn(api, "createDeployment");
    const container = render(data);
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="管理 探针"]')?.click());
    await act(async () => document.querySelector<HTMLButtonElement>('[aria-label="更新 Edge Node C 的应用"]')?.click());
    expect(create).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("核对并接管");
    expect(document.getElementById("deployment-agent")).toBeNull();
  });

  it("keeps a terminal deployment failure out of the live operations area", () => {
    const container = render(fixture());
    expect(container.textContent).not.toContain("最近操作");
    expect(container.textContent).not.toContain("Senlun installation failed");
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
