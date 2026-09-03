import { describe, expect, it } from "vitest";
import type { Application, Publication, Service } from "../types";
import { canCreateRealityNode, installedAppGroups, publicationNeedsAttention, threeXUIAppKey } from "./installed-apps-model";

const application = (id: string, overrides: Partial<Application> = {}): Application => ({
  id, name: "3x-ui", nodeId: id, siteId: "site-a", appKey: threeXUIAppKey,
  image: "example/3x-ui", runtime: "docker", status: "running", installedVersion: "3.7.0",
  updateAvailable: false, createdAt: "2026-09-01T00:00:00Z", updatedAt: "2026-09-01T00:00:00Z",
  ...overrides,
});

const service = (id: string, applicationId: string, overrides: Partial<Service> = {}): Service => ({
  id, applicationId, siteId: "site-a", name: id, protocol: "tcp", containerPort: 443,
  hostPort: 30443, endpoint: "10.0.0.10:30443", source: "observed", appProtocol: "vless/tcp/reality",
  management: false, status: "ready", createdAt: "2026-09-01T00:00:00Z", updatedAt: "2026-09-01T00:00:00Z",
  ...overrides,
});

const publication = (id: string, serviceId: string, overrides: Partial<Publication> = {}): Publication => ({
  id, serviceId, kind: "public_shared_443", ingress: { owner: "application_node", entryNodeId: "worker" },
  hostname: "node.example.com", dnsProvider: "cloudflare", tlsEnabled: false,
  desiredRevision: 1, appliedRevision: 1, status: "ready", createdAt: "2026-09-01T00:00:00Z",
  updatedAt: "2026-09-01T00:00:00Z", ...overrides,
});

const data = (): Parameters<typeof installedAppGroups>[0] => ({ applications: [], apps: [], agents: [], sites: [], services: [], publications: [], deployments: [], threeXUIControllerMigrations: [] });

describe("installed application grouping", () => {
  it("puts the actual controller first and counts its local node only once", () => {
    const input = data();
    input.applications = [
      application("worker", { role: "worker", controllerApplicationId: "controller", nodeSyncStatus: "ready" }),
      application("controller", { role: "master", controllerApplicationId: "controller" }),
    ];
    input.services = [service("worker-inbound", "worker"), service("controller-inbound", "controller")];
    const original = [...input.applications];
    const groups = installedAppGroups(input);

    expect(groups).toHaveLength(1);
    expect(groups[0].controller?.application.id).toBe("controller");
    expect(groups[0].instances.map((instance) => instance.application.id)).toEqual(["controller", "worker"]);
    expect(groups[0].instances.filter((instance) => instance.realityServices.length > 0)).toHaveLength(2);
    expect(input.applications).toEqual(original);
    expect(groups[0].instances.every((instance) => !canCreateRealityNode(instance))).toBe(true);
  });

  it("groups one global controller, cross-Site workers, and legacy controllers together", () => {
    const input = data();
    input.applications = [
      application("controller-a", { role: "master", controllerApplicationId: "controller-a" }),
      application("controller-b", { role: "master", controllerApplicationId: "controller-a", siteId: "site-b" }),
      application("worker-a", { role: "worker", controllerApplicationId: "controller-a", nodeSyncStatus: "ready" }),
      application("cross-site", { role: "worker", controllerApplicationId: "controller-a", nodeSyncStatus: "ready", siteId: "site-b" }),
    ];
    input.threeXUIControllerMigrations = [{ id: "converge", kind: "consolidate", siteId: "site-b", sourceApplicationId: "controller-b", targetApplicationId: "controller-a", backupRevision: 1, state: "backing_up", step: "backup", createdAt: "2026-09-01T00:00:00Z", updatedAt: "2026-09-01T00:00:00Z" }];
    const groups = installedAppGroups(input);
    expect(groups).toHaveLength(1);
    expect(groups[0].controller?.application.id).toBe("controller-a");
    expect(groups[0].legacyControllers.map((instance) => instance.application.id)).toEqual(["controller-b"]);
    expect(groups[0].convergence?.id).toBe("converge");
    expect(groups[0].instances.map((instance) => instance.application.id)).toEqual(["controller-a", "controller-b", "cross-site", "worker-a"]);
  });

  it("keeps generic applications grouped and preserves installed failures outside the catalog", () => {
    const input = data();
    input.applications = [
      application("first", { appKey: "official/monitor", role: undefined }),
      application("second", { appKey: "official/monitor", role: undefined, status: "failed", siteId: "site-b" }),
      application("never-installed", { installedVersion: undefined, status: "failed" }),
      application("another-app", { appKey: "official/other", role: undefined }),
    ];
    const groups = installedAppGroups(input);
    expect(groups).toHaveLength(2);
    expect(groups.flatMap((group) => group.instances)).toHaveLength(3);
    const monitoring = groups.find((group) => group.appKey === "official/monitor")!;
    expect(monitoring.controller).toBeUndefined();
    expect(monitoring.instances).toHaveLength(2);
    expect(monitoring.instances.find((instance) => instance.application.id === "second")?.locked).toBe(true);
  });

  it("keeps Web publication health separate from the same controller's REALITY ingress", () => {
    const input = data();
    input.applications = [application("controller", { role: "master", controllerApplicationId: "controller" })];
    input.services = [
      service("reality", "controller"),
      service("panel", "controller", { protocol: "http", appProtocol: undefined, management: true }),
      service("old-reality", "controller", { status: "stopped" }),
    ];
    input.publications = [
      publication("node-ready", "reality"),
      publication("panel-failed", "panel", { kind: "cloudflare_tunnel", ingress: { owner: "tunnel_connector", entryNodeId: "connector" }, status: "failed", lastError: "Origin unavailable" }),
      publication("old", "reality", { status: "stopped" }),
      publication("cleanup", "panel", { status: "stopped", actionRequired: true }),
    ];
    const instance = installedAppGroups(input)[0].instances[0];
    expect(instance.services).toHaveLength(2);
    expect(instance.realityPublications.map((entry) => entry.id)).toEqual(["node-ready"]);
    expect(instance.publications.map((entry) => entry.id)).toEqual(["node-ready", "panel-failed", "cleanup"]);
    expect(instance.publications.filter(publicationNeedsAttention)).toHaveLength(2);
    expect(instance.application.status).toBe("running");
  });

  it("keeps unfinished recovery locked and requires workers to finish joining", () => {
    const input = data();
    input.applications = [
      application("controller", { role: "master", controllerApplicationId: "controller" }),
      application("joining", { role: "worker", controllerApplicationId: "controller", nodeSyncStatus: "pending" }),
      application("ready", { role: "worker", controllerApplicationId: "controller", nodeSyncStatus: "ready" }),
    ];
    input.deployments = [{ id: "recovery", applicationId: "controller", agentId: "controller", appKey: threeXUIAppKey, appVersion: "3.7.0", state: "failed", reconciliationRequired: true, operation: "upgrade", deleteData: false, createdAt: "2026-09-01T00:00:00Z", updatedAt: "2026-09-01T00:00:00Z" }];
    const instances = installedAppGroups(input)[0].instances;
    expect(instances.find((instance) => instance.application.id === "controller")?.locked).toBe(true);
    expect(canCreateRealityNode(instances.find((instance) => instance.application.id === "controller")!)).toBe(false);
    expect(canCreateRealityNode(instances.find((instance) => instance.application.id === "joining")!)).toBe(false);
    input.deployments = [];
    expect(installedAppGroups(input)[0].instances.filter(canCreateRealityNode).map((instance) => instance.application.id)).toEqual(["controller", "ready"]);
  });

  it("surfaces failed entries ahead of healthy workers without moving the controller", () => {
    const input = data();
    input.applications = [
      application("a-healthy", { role: "worker", controllerApplicationId: "controller", nodeSyncStatus: "ready" }),
      application("z-attention", { role: "worker", controllerApplicationId: "controller", nodeSyncStatus: "ready" }),
      application("controller", { role: "master", controllerApplicationId: "controller" }),
    ];
    input.services = [service("failed-inbound", "z-attention")];
    input.publications = [publication("pending-with-error", "failed-inbound", { status: "pending", lastError: "Listener unavailable" })];
    expect(installedAppGroups(input)[0].instances.map((instance) => instance.application.id)).toEqual(["controller", "z-attention", "a-healthy"]);
  });
});
