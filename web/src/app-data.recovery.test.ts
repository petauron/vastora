// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { APIError, api } from "./api";
import { emptyAppData, loadScreenData } from "./app-data";
import { applicationLaunchURL, desktopApplications } from "./views/applicationLaunch";
import type { CenterStatus } from "./types";

const status: CenterStatus = { version: "test", agentInstallerAvailable: true, agentConnectionMode: "lan", agentConnectUrl: "https://center.example.com" };
const desktopEndpoints = ["apps", "applications", "services", "publications"] as const;

beforeEach(() => {
  vi.spyOn(api, "status").mockResolvedValue(status);
  vi.spyOn(api, "agents").mockResolvedValue({ agents: [] });
  vi.spyOn(api, "actions").mockResolvedValue({ actions: [] });
  vi.spyOn(api, "apps").mockResolvedValue({ apps: [] });
  vi.spyOn(api, "applications").mockResolvedValue({ applications: [] });
  vi.spyOn(api, "services").mockResolvedValue({ services: [] });
  vi.spyOn(api, "publications").mockResolvedValue({ publications: [] });
});

afterEach(() => vi.restoreAllMocks());

describe("activity data failure isolation", () => {
  it.each(desktopEndpoints)("keeps activity available when %s display data fails", async (endpoint) => {
    vi.mocked(api[endpoint]).mockRejectedValue(new APIError("Display data unavailable", 503));
    const controller = new AbortController();

    const result = await loadScreenData("activity", controller.signal);

    expect(result).toMatchObject({ status, agents: [], actions: [], desktopDataUnavailable: true });
    for (const key of desktopEndpoints) expect(result).not.toHaveProperty(key);
    expect(api.actions).toHaveBeenCalledWith(100, controller.signal);
    for (const request of [api.status, api.agents, api.apps, api.applications, api.services, api.publications]) expect(request).toHaveBeenCalledWith(controller.signal);
  });

  it.each(["status", "agents", "actions"] as const)("does not degrade required %s data failures", async (endpoint) => {
    const error = new APIError("Required data unavailable", 503);
    vi.mocked(api[endpoint]).mockRejectedValue(error);
    await expect(loadScreenData("activity")).rejects.toBe(error);
  });

  it.each(desktopEndpoints)("does not degrade %s authentication failures", async (endpoint) => {
    const error = new APIError("Authentication required", 401);
    vi.mocked(api[endpoint]).mockRejectedValue(error);
    await expect(loadScreenData("activity")).rejects.toBe(error);
  });

  it("does not hide an authentication failure behind an earlier display error", async () => {
    vi.mocked(api.apps).mockRejectedValue(new APIError("Catalog unavailable", 503));
    const error = new APIError("Authentication required", 401);
    vi.mocked(api.publications).mockRejectedValue(error);
    await expect(loadScreenData("activity")).rejects.toBe(error);
  });

  it("preserves cancellation instead of accepting a degraded patch", async () => {
    const controller = new AbortController();
    vi.mocked(api.apps).mockImplementation(async () => {
      controller.abort();
      throw new DOMException("Aborted", "AbortError");
    });
    await expect(loadScreenData("activity", controller.signal)).rejects.toMatchObject({ name: "AbortError" });
  });

  it("clears unavailability after refresh and disables retained launch data until then", async () => {
    vi.mocked(api.apps).mockRejectedValueOnce(new APIError("Catalog unavailable", 503));
    const data = { ...emptyAppData(status), ...await loadScreenData("activity") };
    expect(data.desktopDataUnavailable).toBe(true);
    expect(desktopApplications(data, "en")).toEqual([]);
    expect(applicationLaunchURL(data, "vastora-official/senlun")).toBeUndefined();

    const refreshed = { ...data, ...await loadScreenData("activity") };
    expect(refreshed.desktopDataUnavailable).toBe(false);
    for (const key of desktopEndpoints) expect(refreshed[key]).toEqual([]);
  });

  it("keeps catalog-dependent screens fail closed", async () => {
    const error = new APIError("Catalog unavailable", 503);
    vi.mocked(api.apps).mockRejectedValue(error);
    await expect(loadScreenData("home")).rejects.toBe(error);
  });
});
