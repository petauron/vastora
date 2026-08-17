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
});
