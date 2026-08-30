// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { api } from "./api";
import { ThemeProvider } from "./components/theme";
import type { CenterUpdateStatus, Site } from "./types";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((next) => { resolve = next; });
  return { promise, resolve };
}

function site(id: string, name: string): Site {
  return { id, organizationId: "organization-1", name, code: id, description: "", timezone: "UTC", domainSuffix: "", status: "active", gatewayNodes: [], gatewayStatus: "inactive", createdAt: "2026-08-30T00:00:00Z", updatedAt: "2026-08-30T00:00:00Z" };
}

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
  document.documentElement.classList.remove("dark");
  document.documentElement.style.removeProperty("color-scheme");
  vi.restoreAllMocks();
});

function mockReadyCenter() {
  vi.spyOn(api, "setupStatus").mockResolvedValue({ administratorConfigured: true, onboardingComplete: true, suggestedAgentConnectUrl: "https://center.example.com", builtinHeadscaleAvailable: true, cloudflareOAuthAvailable: true, cloudflareConfigured: false, cloudflareAccessConfigured: false, publicAddressCandidates: [], gatewayAddressCandidates: [] });
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
  act(() => root?.render(<ThemeProvider><App /></ThemeProvider>));
  await vi.waitFor(() => expect(container.textContent).toContain("Center connected"));
  return container;
}

describe("application shell", () => {
  it("renders when the browser cannot observe system theme changes", async () => {
    Object.defineProperty(window, "matchMedia", { configurable: true, value: vi.fn().mockImplementation((query: string) => ({ matches: true, media: query })) });
    mockReadyCenter();

    const container = await renderReadyApp();

    expect(container.textContent).toContain("Center connected");
    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });

  it("lets the user switch themes and remembers the choice", async () => {
    mockReadyCenter();
    const container = await renderReadyApp();
    const toggle = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Switch to dark mode"]',
    );

    expect(toggle).not.toBeNull();
    expect(document.documentElement.classList.contains("dark")).toBe(false);
    act(() => toggle?.click());
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(window.localStorage.getItem("vastora.theme")).toBe("dark");
    expect(toggle?.getAttribute("aria-label")).toBe("Switch to light mode");
  });

  it("requires ten characters when creating the administrator", async () => {
    vi.spyOn(api, "setupStatus").mockResolvedValue({ administratorConfigured: false, onboardingComplete: false, suggestedAgentConnectUrl: "", builtinHeadscaleAvailable: true, cloudflareOAuthAvailable: false, cloudflareConfigured: false, cloudflareAccessConfigured: false, publicAddressCandidates: [], gatewayAddressCandidates: [] });
    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    act(() => root?.render(<ThemeProvider><App /></ThemeProvider>));
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

  it("keeps the newest same-screen refresh when deferred responses resolve in reverse order", async () => {
    mockReadyCenter();
    const container = await renderReadyApp();
    const sites = vi.mocked(api.sites);
    const stale = deferred<{ sites: Site[] }>();
    const fresh = deferred<{ sites: Site[] }>();
    let staleSignal: AbortSignal | undefined;
    sites.mockImplementationOnce((signal) => { staleSignal = signal; return stale.promise; });
    sites.mockImplementationOnce(() => fresh.promise);
    const home = [...container.querySelectorAll("button")].find((button) => button.textContent?.trim() === "Home");

    act(() => home?.click());
    act(() => home?.click());
    expect(staleSignal?.aborted).toBe(true);
    await act(async () => { fresh.resolve({ sites: [site("fresh", "Fresh location")] }); });
    await vi.waitFor(() => expect(container.textContent).toContain("Fresh location"));
    await act(async () => { stale.resolve({ sites: [site("stale", "Stale location")] }); });

    expect(container.textContent).toContain("Fresh location");
    expect(container.textContent).not.toContain("Stale location");
  });

  it("fences rapid Home to Nodes to Home navigation and permits a later Nodes load", async () => {
    mockReadyCenter();
    const container = await renderReadyApp();
    const agents = vi.mocked(api.agents);
    const staleNodes = deferred<Awaited<ReturnType<typeof api.agents>>>();
    let staleSignal: AbortSignal | undefined;
    agents.mockImplementationOnce((signal) => { staleSignal = signal; return staleNodes.promise; });
    const nodes = [...container.querySelectorAll("button")].find((button) => button.textContent?.trim() === "Nodes");
    const home = [...container.querySelectorAll("button")].find((button) => button.textContent?.trim() === "Home");

    act(() => nodes?.click());
    act(() => home?.click());
    expect(staleSignal?.aborted).toBe(true);
    await vi.waitFor(() => expect(container.textContent).toContain("Welcome back"));
    await act(async () => { staleNodes.resolve({ agents: [] }); });
    expect(container.textContent).toContain("Welcome back");

    act(() => nodes?.click());
    await vi.waitFor(() => expect(container.textContent).toContain("Add your first node"));
  });

  it("keeps an explicit update check authoritative over an older settings refresh", async () => {
    window.history.replaceState({}, "", "/settings");
    mockReadyCenter();
    vi.spyOn(api, "sources").mockResolvedValue({ sources: [] });
    vi.spyOn(api, "systemDomain").mockResolvedValue({ namespace: "", centerUrl: "https://center.example.com", headscaleUrl: "", cloudflareZone: "", aliases: [], activePublications: 0, pendingCleanup: 0, builtinHeadscale: false, cloudflareOAuthAvailable: false });
    const container = await renderReadyApp();
    await vi.waitFor(() => expect(container.textContent).toContain("Center update"));
    const updates = vi.mocked(api.centerUpdate);
    const stale = deferred<CenterUpdateStatus>();
    let staleSignal: AbortSignal | undefined;
    updates.mockImplementationOnce((_refresh, signal) => { staleSignal = signal; return stale.promise; });
    updates.mockResolvedValueOnce({ currentVersion: "fresh-version", latestVersion: "fresh-version", updateAvailable: false, automatic: true, state: "idle" });
    const settings = [...container.querySelectorAll("button")].find((button) => button.textContent?.trim() === "Settings");

    act(() => settings?.click());
    const check = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("Check"));
    expect(check).toBeDefined();
    await act(async () => { check?.click(); await Promise.resolve(); });
    await vi.waitFor(() => expect(container.textContent).toContain("fresh-version"));
    expect(staleSignal?.aborted).toBe(true);
    await act(async () => { stale.resolve({ currentVersion: "stale-version", latestVersion: "stale-version", updateAvailable: false, automatic: true, state: "idle" }); });

    expect(container.textContent).toContain("fresh-version");
    expect(container.textContent).not.toContain("stale-version");
  });
});
