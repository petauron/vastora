// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { AppData, Deployment } from "../types";
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

function fixture(id = "pulse-agent"): AppData {
  const appKey = `vastora-official/${id}`;
  return {
    status: {}, centerUpdate: {}, systemDomain: {}, centerRemoteAccess: null,
    registryCredentials: [], sources: [], organizations: [], sites: [], routes: [],
    actions: [], integrations: [], threeXUIControllerMigrations: [],
    agents: [
      { id: "first", name: "AKKO CN2", connected: true, credentialRevoked: false, siteId: "site", capabilities: { docker: true }, networkProfile: { serviceAddress: "10.0.0.1" } },
      { id: "failed-node", name: "DataWave CN2", connected: true, credentialRevoked: false, siteId: "site", capabilities: { docker: true }, networkProfile: { serviceAddress: "10.0.0.2" } },
    ],
    apps: [{ key: appKey, sourceId: "vastora-official", fetchedAt: "2026-09-11T00:00:00Z", app: {
      id, version: "0.1.0-alpha.2", name: { en: "Collector", "zh-CN": "探针" },
      description: { en: "Host metrics", "zh-CN": "主机监控" }, hostAccess: true, config: [],
    } }],
    // A running monitor with private HTTPS makes Pulse installation eligible.
    applications: [{ id: "monitor", appKey: "vastora-official/pulse", status: "running" }],
    services: [{ id: "dashboard", applicationId: "monitor", name: "dashboard", status: "ready" }],
    publications: [{ id: "private", serviceId: "dashboard", status: "ready", tlsEnabled: true, kind: "headscale_gateway" }],
    deployments: [{ id: "failed-install", agentId: "failed-node", appKey, appVersion: "0.1.0-alpha.2", state: "failed", operation: "install", deleteData: false, error: "agent: Pulse installation failed", createdAt: "2026-09-11T00:00:00Z", updatedAt: "2026-09-11T00:00:00Z" }],
  } as unknown as AppData;
}

const mutate = async (action: () => Promise<unknown>) => { await action(); };

function render(data: AppData) {
  if (!root) {
    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
  }
  act(() => root?.render(<ThemeProvider><AppsView data={data} language="zh-CN" mutate={mutate} /></ThemeProvider>));
}

function button(label: string) {
  const element = [...document.querySelectorAll<HTMLButtonElement>("button")].find((value) => value.textContent?.trim() === label);
  if (!element) throw new Error(`Missing button: ${label}`);
  return element;
}

function retry() {
  act(() => button("重试").click());
}

function selectedNode() {
  return document.querySelector<HTMLButtonElement>('[role="dialog"] [role="combobox"]');
}

describe("application installation retry", () => {
  it.each(["pulse-agent", "komari-agent"])("retries %s on the failed node, not the first candidate", async (id) => {
    const data = fixture(id);
    const create = vi.spyOn(api, "createDeployment").mockResolvedValue({ ...data.deployments[0], state: "pending" } as Deployment);
    render(data);
    retry();
    expect(selectedNode()?.textContent).toContain("DataWave CN2");
    expect(selectedNode()?.disabled).toBe(true);
    expect(button("开始安装").disabled).toBe(false);
    await act(async () => button("开始安装").click());
    expect(create).toHaveBeenCalledExactlyOnceWith("failed-node", `vastora-official/${id}`, {}, "install", false, undefined, "", undefined);
  });

  it("keeps the failed node visible but blocks retry when it is offline", () => {
    const data = fixture();
    data.agents[1].connected = false;
    const create = vi.spyOn(api, "createDeployment");
    render(data);
    retry();
    expect(selectedNode()?.textContent).toContain("DataWave CN2");
    expect(selectedNode()?.getAttribute("aria-invalid")).toBe("true");
    expect(button("开始安装").disabled).toBe(true);
    expect(document.getElementById("deployment-agent-error")?.textContent).toContain("原节点暂时无法安装");
    act(() => button("开始安装").click());
    expect(create).not.toHaveBeenCalled();
  });

  it("rechecks eligibility while open without switching to another node", async () => {
    const data = fixture();
    const create = vi.spyOn(api, "createDeployment");
    render(data);
    retry();
    const unavailable = { ...data, agents: data.agents.filter((agent) => agent.id !== "failed-node") };
    render(unavailable);
    expect(selectedNode()?.textContent).toContain("DataWave CN2");
    expect(button("开始安装").disabled).toBe(true);
    // Even a direct form submission must not bypass the eligibility check.
    await act(async () => document.querySelector('[role="dialog"] form')?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(create).not.toHaveBeenCalled();
    render(data);
    expect(selectedNode()?.textContent).toContain("DataWave CN2");
    expect(button("开始安装").disabled).toBe(false);
  });

  it("does not silently replace the failed node when it is already installed", () => {
    const data = fixture();
    data.applications.push({ id: "installed", nodeId: "failed-node", appKey: data.apps[0].key, status: "running", installedVersion: "0.1.0-alpha.2" } as AppData["applications"][number]);
    render(data);
    retry();
    expect(selectedNode()?.textContent).toContain("DataWave CN2");
    expect(button("开始安装").disabled).toBe(true);
  });

  it("still lets a new installation choose among eligible nodes", () => {
    render(fixture());
    const install = document.querySelector<HTMLButtonElement>('[aria-label="安装 探针"]');
    expect(install).not.toBeNull();
    act(() => install?.click());
    expect(selectedNode()?.textContent).toContain("AKKO CN2");
    expect(selectedNode()?.disabled).toBe(false);
  });
});
