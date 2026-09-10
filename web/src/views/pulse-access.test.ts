import { describe, expect, it } from "vitest";
import type { AgentView, AppData, Application, Service } from "../types";
import { canInstall, eligibleAppNodes, gatewaysForKind, installBlocker, pulsePrivateAccess } from "./appAccess";

const agent = { id: "collector", name: "Collector", connected: true, capabilities: { docker: false }, networkProfile: { serviceAddress: "10.0.0.2", enabledKinds: ["lan"] } } as AgentView;
const controller = { id: "monitor", appKey: "vastora-official/pulse", status: "running", installedVersion: "0.1.0-alpha.2" } as Application;
const dashboard = { id: "dashboard", applicationId: "monitor", name: "dashboard", status: "ready", protocol: "http" } as Service;
const data = { agents: [agent], applications: [controller], services: [dashboard], publications: [] } as unknown as AppData;

describe("Pulse app access", () => {
  it("allows native collectors without Docker", () => {
    expect(canInstall(agent, "vastora-official/pulse-agent")).toBe(true);
    expect(canInstall(agent, "vastora-official/pulse")).toBe(false);
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
