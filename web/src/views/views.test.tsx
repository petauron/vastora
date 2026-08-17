// @vitest-environment jsdom

import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it } from "vitest";
import type { DashboardData } from "../App";
import { AppsView } from "./AppsView";
import { NetworkView } from "./NetworkView";

let root: Root | undefined;
afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
});

const dashboard = (): DashboardData => ({
  status: { version: "test", catalogSources: 1, catalogApps: 1, agents: 1, deployments: 1 },
  sources: [], organizations: [], routes: [], actions: [], integrations: [],
  sites: [{ id: "site", organizationId: "org", name: "Home", code: "home", description: "", domainSuffix: "home.example", status: "active", gatewayNodes: ["agent"], gatewayStatus: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }],
  agents: [{ id: "agent", name: "home-server", version: "test", appliedInstallations: 1, enrolledAt: "2026-08-18T00:00:00Z", lastSeenAt: "2026-08-18T00:00:00Z", connected: true, siteId: "site", roles: ["worker", "gateway"], capabilities: { docker: true, gateway: true, tunnel: true, metrics: false, logs: false }, networkCandidates: [{ address: "192.168.1.2", interface: "eth0", family: "ipv4", kind: "lan", observedAt: "2026-08-18T00:00:00Z" }], networkProfile: { serviceAddress: "192.168.1.2", lanAddress: "192.168.1.2", enabledKinds: ["lan"], directPublic: false }, gatewayHealthy: true }],
  apps: [{ key: "vastora-official/komari-agent", sourceId: "vastora-official", fetchedAt: "2026-08-18T00:00:00Z", app: { id: "komari-agent", version: "1.2.60", name: { en: "Komari Agent", "zh-CN": "Komari 探针" }, description: { en: "Monitoring", "zh-CN": "监控探针" }, hostAccess: true, config: [] } }],
  applications: [
    { id: "running", name: "Komari Agent", nodeId: "agent", siteId: "site", appKey: "vastora-official/komari-agent", image: "image", status: "running", runtime: "docker", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" },
    { id: "failed", name: "Failed", nodeId: "agent", siteId: "site", appKey: "vastora-official/failed", image: "image", status: "failed", runtime: "docker", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }
  ],
  deployments: [], services: [], publications: []
});

function render(element: ReactNode) {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root?.render(element));
  return container;
}

describe("network and app views", () => {
  it("shows LAN, Headscale, and public networking as simultaneous capabilities", () => {
    const container = render(<NetworkView data={dashboard()} language="zh-CN" mutate={async () => undefined} />);
    expect(container.textContent).toContain("局域网");
    expect(container.textContent).toContain("Headscale");
    expect(container.textContent).toContain("公网与 Cloudflare");
    expect(container.textContent).toContain("同时使用局域网、Headscale 和公网");
  });

  it("shows only successful applications and marks host-privileged packages", () => {
    const container = render(<AppsView data={dashboard()} language="zh-CN" mutate={async () => undefined} />);
    expect(container.textContent).toContain("Komari 探针");
    expect(container.textContent).toContain("高权限");
    expect(container.textContent).not.toContain("Failed");
    expect(container.textContent).toContain("先把应用安装为私有服务");
  });
});
