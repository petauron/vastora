// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { APIError, api } from "./api";

afterEach(() => {
  vi.unstubAllGlobals();
  document.cookie = "vastora_csrf=; Max-Age=0; Path=/";
});

describe("Center API client", () => {
  it("keeps application installation separate from publication", async () => {
    document.cookie = "vastora_csrf=csrf-value; Path=/";
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ id: "deployment-1" }), {
        status: 201,
        headers: { "Content-Type": "application/json" }
      })
    );
    vi.stubGlobal("fetch", fetchMock);

    await api.createDeployment(
      "agent-1",
      "vastora-official/cpa",
      { timezone: "UTC", debug: false },
      "upgrade",
      false
    );

    expect(fetchMock).toHaveBeenCalledOnce();
    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(path).toBe("/api/v1/deployments");
    expect(init.credentials).toBe("same-origin");
    expect(init.method).toBe("POST");
    expect(new Headers(init.headers).get("X-CSRF-Token")).toBe("csrf-value");
    expect(JSON.parse(String(init.body))).toEqual({
      agentId: "agent-1",
      appKey: "vastora-official/cpa",
      config: { timezone: "UTC", debug: false },
      operation: "upgrade",
      deleteData: false
    });
  });

  it("creates an independent service publication", async () => {
    document.cookie = "vastora_csrf=csrf-value; Path=/";
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ id: "publication-1" }), { status: 201, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    await api.createPublication({ serviceId: "service-1", kind: "headscale_gateway", gatewayNodeId: "agent-1", hostname: "cpa.internal.example", dnsProvider: "headscale" });

    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(path).toBe("/api/v1/publications");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ serviceId: "service-1", kind: "headscale_gateway", gatewayNodeId: "agent-1", hostname: "cpa.internal.example", dnsProvider: "headscale" });
  });

  it("reauthenticates for application credentials and uses an idempotency key for rotation", async () => {
    document.cookie = "vastora_csrf=csrf-value; Path=/";
    const reauthentication = ["test", "reauth"].join("-");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ kind: "cpa", managementKey: "management", clientApiKey: "client" }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: "rotation-1", state: "pending" }), { status: 202, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: "rotation-1", state: "succeeded" }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    await api.revealApplicationCredentials("cpa-1", reauthentication);
    await api.rotateApplicationCredentials("cpa-1", "management", reauthentication, "rotation-operation-key-0001");
    await api.applicationCredentialRotation("cpa-1", "rotation-1");

    const [revealPath, revealInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(revealPath).toBe("/api/v1/applications/cpa-1/credentials/reveal");
    expect(JSON.parse(String(revealInit.body))).toEqual({ currentPassword: reauthentication });
    const [rotationPath, rotationInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(rotationPath).toBe("/api/v1/applications/cpa-1/credentials/rotate");
    expect(new Headers(rotationInit.headers).get("Idempotency-Key")).toBe("rotation-operation-key-0001");
    expect(JSON.parse(String(rotationInit.body))).toEqual({ target: "management", currentPassword: reauthentication, confirm: true });
    const [statusPath, statusInit] = fetchMock.mock.calls[2] as [string, RequestInit];
    expect(statusPath).toBe("/api/v1/applications/cpa-1/credentials/rotations/rotation-1");
    expect(statusInit.method).toBeUndefined();
  });

  it("updates HTTPS on an existing private publication", async () => {
    document.cookie = "vastora_csrf=csrf-value; Path=/";
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ id: "publication-1", tlsEnabled: true }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    await api.updatePublicationTLS("publication-1", true);

    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(path).toBe("/api/v1/publications/publication-1/tls");
    expect(init.method).toBe("PUT");
    expect(JSON.parse(String(init.body))).toEqual({ enabled: true });
  });

  it("requires an explicit controller migration confirmation", async () => {
    document.cookie = "vastora_csrf=csrf-value; Path=/";
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ id: "migration-1" }), { status: 201, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    await api.migrateThreeXUIController("controller", "worker", true);

    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(path).toBe("/api/v1/applications/controller/3xui-controller/migrate");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ targetApplicationId: "worker", confirm: true, allowStaleBackup: true });
  });

  it("preserves Center errors and status codes", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: "deployment already active" }), {
          status: 409,
          headers: { "Content-Type": "application/json" }
        })
      )
    );

    await expect(api.deployments()).rejects.toEqual(new APIError("deployment already active", 409));
  });

  it("preserves stable error codes for localized messages", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ code: "authentication_required", error: "center: authentication required" }), {
          status: 401,
          headers: { "Content-Type": "application/json" }
        })
      )
    );

    await expect(api.status()).rejects.toEqual(new APIError("center: authentication required", 401, "authentication_required"));
  });

  it("requests a bounded activity page", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ actions: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    await api.actions(100);

    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v1/actions?limit=100");
  });
});
