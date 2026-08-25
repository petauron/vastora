// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { api } from "./api";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;

beforeEach(() => {
  Object.defineProperty(window, "matchMedia", { configurable: true, value: vi.fn().mockImplementation((query: string) => ({ matches: false, media: query, onchange: null, addEventListener: vi.fn(), removeEventListener: vi.fn(), addListener: vi.fn(), removeListener: vi.fn(), dispatchEvent: vi.fn() })) });
  window.localStorage.setItem("vastora.language", "en");
});

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  window.history.replaceState({}, "", "/");
  window.localStorage.clear();
  vi.restoreAllMocks();
});

function mockReadyCenter() {
  vi.spyOn(api, "setupStatus").mockResolvedValue({ administratorConfigured: true, onboardingComplete: true, suggestedAgentConnectUrl: "https://center.example.com", builtinHeadscaleAvailable: true, cloudflareOAuthAvailable: true, cloudflareConfigured: false, publicAddressCandidates: [], gatewayAddressCandidates: [] });
  const status = vi.spyOn(api, "status").mockResolvedValue({ version: "test", agentInstallerAvailable: true, agentConnectionMode: "lan", agentConnectUrl: "https://center.example.com" });
  vi.spyOn(api, "centerUpdate").mockResolvedValue({ currentVersion: "test", latestVersion: "test", updateAvailable: false, automatic: true, state: "idle" });
  vi.spyOn(api, "sites").mockResolvedValue({ sites: [] });
  vi.spyOn(api, "agents").mockResolvedValue({ agents: [] });
  vi.spyOn(api, "applications").mockResolvedValue({ applications: [] });
  vi.spyOn(api, "publications").mockResolvedValue({ publications: [] });
  vi.spyOn(api, "actions").mockResolvedValue({ actions: [] });
  vi.spyOn(api, "integrations").mockResolvedValue({ integrations: [] });
  return status;
}

async function renderReadyApp() {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root?.render(<App />));
  await vi.waitFor(() => expect(container.textContent).toContain("Center connected"));
  return container;
}

describe("application shell", () => {
  it("requires ten characters when creating the administrator", async () => {
    vi.spyOn(api, "setupStatus").mockResolvedValue({ administratorConfigured: false, onboardingComplete: false, suggestedAgentConnectUrl: "", builtinHeadscaleAvailable: true, cloudflareOAuthAvailable: false, cloudflareConfigured: false, publicAddressCandidates: [], gatewayAddressCandidates: [] });
    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    act(() => root?.render(<App />));
    await vi.waitFor(() => expect(container.textContent).toContain("Create administrator"));
    expect(container.querySelector<HTMLInputElement>("#password")?.minLength).toBe(10);
    expect(container.textContent).toContain("At least 10 characters.");
  });

  it("moves keyboard focus to the main content after navigation", async () => {
    mockReadyCenter();
    const container = await renderReadyApp();
    const nodes = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("Nodes"));
    act(() => nodes?.click());
    await vi.waitFor(() => expect(container.textContent).toContain("Add your first node"));
    await vi.waitFor(() => expect(document.activeElement).toBe(container.querySelector("#main-content")));
    expect(document.activeElement).toBe(container.querySelector("#main-content"));
  });

  it("marks cached data stale and offers retry after losing Center", async () => {
    const status = mockReadyCenter();
    const container = await renderReadyApp();
    status.mockRejectedValueOnce(new TypeError("Failed to fetch"));
    const nodes = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("Nodes"));
    act(() => nodes?.click());
    await vi.waitFor(() => expect(container.textContent).toContain("Connection to Center was interrupted"));
    expect(container.textContent).toContain("This page is showing the last successful data");
    expect(container.textContent).toContain("Retry now");
    expect(container.textContent).toContain("Reconnecting");
  });
});
