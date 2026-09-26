import { describe, expect, it } from "vitest";
import type { AgentView, AppData, Application, AppView, Service } from "../types";
import { canInstall, eligibleAppNodes, gatewaysForKind, installBlocker, packageNodeBlocker, pulsePrivateAccess } from "./appAccess";

const agent: AgentView = { id: "collector", name: "Collector", version: "test", operatingSystem: "linux", architecture: "amd64", status: "active", appliedInstallations: 0, enrolledAt: "2026-09-26T00:00:00Z", lastSeenAt: "2026-09-26T00:00:00Z", siteId: "site", roles: ["worker"], connected: true, credentialRevoked: false, capabilities: { docker: false, gateway: false, tunnel: false, metrics: true, logs: false, executorVersions: { systemd: 1 }, runtimeCapabilities: [] }, networkCandidates: [], networkProfile: { serviceAddress: "10.0.0.2", enabledKinds: ["lan"], directPublic: false }, gatewayHealthy: false, remoteUpdateSupported: true };
const app = (id: string, kind: string): AppView => ({ key: `vastora-official/${id}`, sourceId: "vastora-official", fetchedAt: "2026-09-26T00:00:00Z", manifestSha256: "a".repeat(64), app: { id, version: "1.0.0", packageRevision: 1, runtime: { kind, version: 1, requiredCapabilities: [] }, name: { en: id, "zh-CN": id }, description: { en: id, "zh-CN": id }, config: [] } });
const native = app("pulse-agent", "systemd");
const container = app("pulse", "docker");
const controller = { id: "monitor", appKey: "vastora-official/pulse", status: "running", installedVersion: "0.1.0-alpha.2" } as Application;
const dashboard = { id: "dashboard", applicationId: "monitor", name: "dashboard", status: "ready", protocol: "http" } as Service;
const data = { apps: [native, container], agents: [agent], applications: [controller], services: [dashboard], publications: [] } as unknown as AppData;

describe("Pulse app access", () => {
  it("allows native collectors without Docker", () => {
    expect(canInstall(agent, native)).toBe(true);
    expect(canInstall(agent, container)).toBe(false);
  });
  it("uses a single monitoring service across sites", () => {
    expect(eligibleAppNodes(data, "vastora-official/pulse")).toEqual([]);
    expect(installBlocker(data, "vastora-official/pulse", "zh-CN")).toContain("全局监控主机");
  });
  it("waits for private HTTPS, not a browser-authenticated Tunnel", () => {
    expect(eligibleAppNodes(data, "vastora-official/pulse-agent")).toEqual([]);
    const publication = { id: "entry", serviceId: "dashboard", kind: "cloudflare_tunnel", status: "ready", tlsEnabled: true } as AppData["publications"][number];
    expect(pulsePrivateAccess({ ...data, publications: [publication] })).toBeUndefined();
    const ready = { ...data, publications: [{ ...publication, kind: "headscale_gateway" as const }] };
    expect(eligibleAppNodes(ready, "vastora-official/pulse-agent")).toEqual([agent]);
    expect(pulsePrivateAccess({ ...ready, publications: [{ ...ready.publications[0], tlsEnabled: false }] })).toBeUndefined();
  });
  it("does not offer unauthenticated direct public access", () => {
    expect(gatewaysForKind(data, dashboard, "public_direct")).toEqual([]);
  });
});

describe("declarative runtime eligibility", () => {
  it("accepts previously unknown application IDs using advertised executor support", () => {
    const unknown = app("never-compiled-into-vastora", "systemd");
    expect(canInstall(agent, unknown)).toBe(true);
    expect(eligibleAppNodes({ ...data, apps: [unknown] }, unknown.key)).toEqual([agent]);
  });

  it("reports unknown capabilities without affecting another supported package", () => {
    const unknown = app("future-package", "systemd");
    unknown.app.runtime!.requiredCapabilities = ["future-device"];
    expect(canInstall(agent, unknown)).toBe(false);
    expect(packageNodeBlocker(agent, unknown, "en")).toBe("Missing node capabilities: future-device");
    expect(canInstall(agent, native)).toBe(true);
  });

  it("rejects absent recipes and unsupported runtime versions instead of guessing from app ID", () => {
    expect(canInstall(agent)).toBe(false);
    const changed = { ...native, app: { ...native.app, runtime: { kind: "systemd", version: 2 } } };
    expect(canInstall(agent, changed)).toBe(false);
    expect(packageNodeBlocker(agent, changed, "zh-CN")).toContain("systemd v2");
    expect(canInstall({ ...agent, connected: false }, native)).toBe(false);
  });
});
