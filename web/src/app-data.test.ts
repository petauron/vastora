// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { loadScreenData, pathForScreen, screenFromPath } from "./app-data";
import type { CenterStatus } from "./types";

const status: CenterStatus = {
  version: "test",
  agentInstallerAvailable: true,
  agentConnectionMode: "lan",
  agentConnectUrl: "https://center.example.com"
};

afterEach(() => {
  vi.restoreAllMocks();
  window.history.replaceState({}, "", "/");
});

describe("screen-scoped data loading", () => {
  it("loads only the home summary resources", async () => {
    vi.spyOn(api, "status").mockResolvedValue(status);
    vi.spyOn(api, "centerUpdate").mockResolvedValue({ currentVersion: "test", latestVersion: "test", updateAvailable: false, automatic: true, state: "idle" });
    vi.spyOn(api, "sites").mockResolvedValue({ sites: [] });
    vi.spyOn(api, "agents").mockResolvedValue({ agents: [] });
    vi.spyOn(api, "applications").mockResolvedValue({ applications: [] });
    vi.spyOn(api, "publications").mockResolvedValue({ publications: [] });
    const actions = vi.spyOn(api, "actions").mockResolvedValue({ actions: [] });
    const apps = vi.spyOn(api, "apps");
    const deployments = vi.spyOn(api, "deployments");

    const result = await loadScreenData("home");

    expect(result.status).toEqual(status);
    expect(actions).toHaveBeenCalledWith(10);
    expect(apps).not.toHaveBeenCalled();
    expect(deployments).not.toHaveBeenCalled();
  });

  it("loads the application workspace in parallel without activity history", async () => {
    vi.spyOn(api, "status").mockResolvedValue(status);
    vi.spyOn(api, "apps").mockResolvedValue({ apps: [] });
    vi.spyOn(api, "agents").mockResolvedValue({ agents: [] });
    vi.spyOn(api, "deployments").mockResolvedValue({ deployments: [] });
    vi.spyOn(api, "applications").mockResolvedValue({ applications: [] });
    vi.spyOn(api, "services").mockResolvedValue({ services: [] });
    vi.spyOn(api, "publications").mockResolvedValue({ publications: [] });
    vi.spyOn(api, "integrations").mockResolvedValue({ integrations: [] });
    vi.spyOn(api, "sites").mockResolvedValue({ sites: [] });
	vi.spyOn(api, "registryCredentials").mockResolvedValue({ credentials: [] });
	vi.spyOn(api, "threeXUIControllerMigrations").mockResolvedValue({ migrations: [] });
    const actions = vi.spyOn(api, "actions");

    const result = await loadScreenData("apps");

    expect(result.apps).toEqual([]);
    expect(result.deployments).toEqual([]);
    expect(actions).not.toHaveBeenCalled();
  });

  it("maps navigation to stable deep links", () => {
    expect(pathForScreen("network")).toBe("/network");
    window.history.replaceState({}, "", "/activity");
    expect(screenFromPath()).toBe("activity");
    window.history.replaceState({}, "", "/unknown");
    expect(screenFromPath()).toBe("home");
  });
});
