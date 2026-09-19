// @vitest-environment jsdom

import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, vi } from "vitest";
import type { AppData } from "../App";
import type { ApplicationCommand } from "../types";
import { ThemeProvider } from "../components/theme";


let root: Root | undefined;
beforeEach(() => {
  vi.stubGlobal("matchMedia", vi.fn((media: string) => ({ matches: false, media, addEventListener: vi.fn(), removeEventListener: vi.fn() })));
  vi.stubGlobal("EventSource", class {
    onmessage: ((event: MessageEvent<string>) => void) | null = null;
    close() {}
  });
});
afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  window.sessionStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

export const dashboard = (): AppData => ({
  status: { version: "test", agentInstallerAvailable: true, agentConnectionMode: "lan", agentConnectUrl: "https://center.example.com" },
  centerUpdate: { currentVersion: "test", latestVersion: "test", updateAvailable: false, releaseCheckAvailable: true, automatic: true, state: "idle", checkedAt: "2026-08-18T00:00:00Z" },
  sources: [], organizations: [], routes: [], actions: [], integrations: [], threeXUIControllerMigrations: [],
  systemDomain: { namespace: "vastora.example.com", centerUrl: "https://center.vastora.example.com", headscaleUrl: "https://headscale.vastora.example.com", cloudflareZone: "example.com", aliases: [], activePublications: 0, pendingCleanup: 0, builtinHeadscale: true, cloudflareOAuthAvailable: true },
  sites: [{ id: "site", organizationId: "org", name: "Home", code: "home", description: "", timezone: "Asia/Singapore", domainSuffix: "home.example", status: "active", gatewayNodes: ["agent"], gatewayStatus: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }],
  agents: [{ id: "agent", name: "home-server", version: "test", operatingSystem: "linux", architecture: "amd64", status: "active", appliedInstallations: 1, enrolledAt: "2026-08-18T00:00:00Z", lastSeenAt: "2026-08-18T00:00:00Z", connected: true, credentialRevoked: false, siteId: "site", roles: ["worker", "gateway"], capabilities: { docker: true, gateway: true, tunnel: true, metrics: false, logs: false }, networkCandidates: [{ address: "192.168.1.2", interface: "eth0", kind: "lan", observedAt: "2026-08-18T00:00:00Z" }], networkProfile: { serviceAddress: "192.168.1.2", lanAddress: "192.168.1.2", enabledKinds: ["lan"], directPublic: false }, gatewayHealthy: true, remoteUpdateSupported: true }],
  apps: [{ key: "vastora-official/komari-agent", sourceId: "vastora-official", fetchedAt: "2026-08-18T00:00:00Z", app: { id: "komari-agent", version: "1.2.60", name: { en: "Komari Agent", "zh-CN": "Komari 探针" }, description: { en: "Monitoring", "zh-CN": "监控探针" }, hostAccess: true, config: [] } }],
  registryCredentials: [],
  centerRemoteAccess: { available: true, enabled: false, status: "disabled" },
  applications: [
    { id: "running", name: "Komari Agent", nodeId: "agent", siteId: "site", appKey: "vastora-official/komari-agent", image: "", status: "running", runtime: "host", installedVersion: "1.2.60", availableVersion: "1.2.60", updateAvailable: false, createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" },
    { id: "failed", name: "Failed", nodeId: "agent", siteId: "site", appKey: "vastora-official/failed", image: "image", status: "failed", runtime: "docker", updateAvailable: false, createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }
  ],
  deployments: [], services: [], publications: []
});

export const realityDashboard = () => {
  const data = dashboard();
  data.agents[0].networkProfile = { serviceAddress: "10.0.0.10", publicAddress: "203.0.113.10", publicBindAddress: "203.0.113.10", publicMode: "direct", enabledKinds: ["lan", "public"], directPublic: true };
  data.sites[0].domainSuffix = "vastora.example.com";
  data.apps = [{ key: "vastora-official/3x-ui", sourceId: "vastora-official", fetchedAt: "2026-08-18T00:00:00Z", app: { id: "3x-ui", version: "3.7.0", name: { en: "3x-ui", "zh-CN": "3x-ui" }, description: { en: "Proxy management", "zh-CN": "代理管理" }, hostAccess: true, config: [] } }];
  data.applications = [{ ...data.applications[0], id: "three-x-ui", name: "3x-ui", appKey: "vastora-official/3x-ui", role: "master", controllerApplicationId: "three-x-ui", installedVersion: "3.7.0", availableVersion: "3.7.0" }];
  return data;
};

export function render(element: ReactNode) {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root?.render(<ThemeProvider>{element}</ThemeProvider>));
  return container;
}

export function rerender(element: ReactNode) {
  act(() => root?.render(<ThemeProvider>{element}</ThemeProvider>));
}

export function resetRender() {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
}

export function openAppDetails(container: HTMLElement, applicationID?: string) {
  const row = applicationID
    ? [...container.querySelectorAll<HTMLElement>("[data-application-id]")].find((element) => element.dataset.applicationId === applicationID)
    : container.querySelector<HTMLElement>('[data-slot="subscription-controller"]') ?? container.querySelector<HTMLElement>("[data-application-id]");
  const manage = [...(row?.querySelectorAll<HTMLButtonElement>("button") ?? [])].find((button) => button.getAttribute("aria-label")?.startsWith("管理 ") && (button.getAttribute("aria-label")?.endsWith(" 应用") || button.getAttribute("aria-label")?.endsWith(" 订阅主机")));
  if (!manage) throw new Error("Application management action was not rendered");
  act(() => manage.click());
  return document.body;
}

export function renderAppDetails(element: ReactNode) {
  return openAppDetails(render(element));
}

export async function openRealityCreation(container: HTMLElement, applicationID?: string) {
  const details = openAppDetails(container, applicationID);
  const create = [...details.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("创建 VLESS"));
  if (!create) throw new Error("Subscription controller did not offer local node creation");
  await act(async () => {
    create.click();
    await Promise.resolve();
  });
}

export function mockCommandEvent(command: ApplicationCommand) {
  class CommandEventSource {
    onmessage: ((event: MessageEvent<string>) => void) | null = null;

    constructor(readonly url: string, readonly init?: EventSourceInit) {
      queueMicrotask(() => this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(command) })));
    }

    close() {}
  }
  vi.stubGlobal("EventSource", CommandEventSource);
}
