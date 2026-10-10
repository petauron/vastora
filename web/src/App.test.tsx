// @vitest-environment jsdom

import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { APIError, api } from "./api";
import { emptyAppData } from "./app-data";
import { ThemeProvider } from "./components/theme";
import type { CenterUpdateStatus, Site } from "./types";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((next, fail) => { resolve = next; reject = fail; });
  return { promise, resolve, reject };
}

function site(id: string, name: string): Site {
  return { id, organizationId: "organization-1", name, code: id, description: "", timezone: "UTC", domainSuffix: "", status: "active", gatewayNodes: [], gatewayStatus: "inactive", createdAt: "2026-08-30T00:00:00Z", updatedAt: "2026-08-30T00:00:00Z" };
}

beforeEach(() => {
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
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
  delete window.turnstile;
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function mockReadyCenter() {
  vi.spyOn(api, "setupStatus").mockResolvedValue({ administratorConfigured: true, onboardingComplete: true, suggestedAgentConnectUrl: "https://center.example.com", builtinHeadscaleAvailable: true, cloudflareOAuthAvailable: true, publicNetworkHelperAvailable: true, regionLookupAvailable: true, cloudflareConfigured: false, cloudflareAccessConfigured: false, cloudflareTurnstileConfigured: false, loginProtection: { captchaRequired: false }, publicAddressCandidates: [], gatewayAddressCandidates: [] });
  const centerStatus = { version: "test", agentInstallerAvailable: true, agentConnectionMode: "lan" as const, agentConnectUrl: "https://center.example.com" };
  const status = vi.spyOn(api, "status").mockResolvedValue(centerStatus);
  vi.spyOn(api, "centerUpdate").mockResolvedValue({ currentVersion: "test", latestVersion: "test", updateAvailable: false, releaseCheckAvailable: true, automatic: true, state: "idle" });
  vi.spyOn(api, "sites").mockResolvedValue({ sites: [] });
  vi.spyOn(api, "agents").mockResolvedValue({ agents: [] });
  vi.spyOn(api, "applications").mockResolvedValue({ applications: [] });
  vi.spyOn(api, "apps").mockResolvedValue({ apps: [] });
  vi.spyOn(api, "services").mockResolvedValue({ services: [] });
  vi.spyOn(api, "publications").mockResolvedValue({ publications: [] });
  vi.spyOn(api, "actions").mockResolvedValue({ actions: [] });
  vi.spyOn(api, "integrations").mockResolvedValue({ integrations: [] });
  vi.spyOn(api, "meridian").mockResolvedValue(emptyAppData(centerStatus).meridian);
  return status;
}

async function renderReadyApp() {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  // Complete initialization and menu registration before sending user events.
  await act(async () => root?.render(<ThemeProvider><App /></ThemeProvider>));
  await vi.waitFor(() => expect(container.querySelector(".desktop-system-bar")).not.toBeNull());
  return container;
}

describe("application shell", () => {
  it("keeps desktop selection and the same surface mounted behind an opened and minimized window", async () => {
    mockReadyCenter();
    const container = await renderReadyApp();
    const desktop = container.querySelector(".desktop-home");
    const shortcut = container.querySelector<HTMLButtonElement>('[data-desktop-item="system:nodes"]')!;
    act(() => shortcut.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, detail: 1 })));
    expect(shortcut.dataset.selected).toBe("true");
    act(() => {
      window.history.pushState({}, "", "/overview");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await vi.waitFor(() => expect(container.querySelector(".desktop-window")).not.toBeNull());
    expect(container.querySelector(".desktop-home")).toBe(desktop);
    expect(shortcut.dataset.selected).toBe("true");
    expect(container.querySelectorAll('[role="main"]')).toHaveLength(1);
    expect(container.querySelector(".desktop-window .desktop-home")).toBeNull();
    await act(async () => container.querySelector<HTMLButtonElement>('button[aria-label="Minimize window"]')?.click());
    expect(container.querySelector<HTMLElement>(".desktop-window")?.style.display).toBe("none");
    expect(container.querySelector(".desktop-home")).toBe(desktop);
    expect(shortcut.dataset.selected).toBe("true");
    expect(container.querySelectorAll('[role="main"]')).toHaveLength(1);
  });

  it("renders installed shortcuts on a direct non-desktop visit and keeps the mobile background inert", async () => {
    vi.stubGlobal("innerWidth", 390);
    window.history.replaceState({}, "", "/overview");
    mockReadyCenter();
    vi.mocked(api.applications).mockResolvedValue({ applications: [{ id: "sample-app", name: "Sample app", nodeId: "sample-node", siteId: "sample-site", appKey: "example/sample", installedVersion: "1.0.0", image: "example/sample:1", status: "running", runtime: "docker", updateAvailable: false, createdAt: "", updatedAt: "" }] });
    const container = await renderReadyApp();
    const desktop = container.querySelector(".desktop-home");
    expect(desktop?.querySelector('[data-desktop-item="app:example/sample"]')?.textContent).toContain("Sample app");
    expect(container.querySelector(".desktop-surface")?.hasAttribute("inert")).toBe(true);
    await act(async () => container.querySelector<HTMLButtonElement>('button[aria-label="Minimize window"]')?.click());
    expect(container.querySelector(".desktop-home")).toBe(desktop);
    expect(container.querySelector(".desktop-surface")?.hasAttribute("inert")).toBe(false);
  });

  it("keeps independent windows, geometry and local input across focus, minimize and close", async () => {
    window.history.replaceState({}, "", "/overview");
    mockReadyCenter();
    vi.mocked(api.agents).mockResolvedValue({ agents: [{ id: "test-node", name: "Test host", version: "test", operatingSystem: "linux", architecture: "amd64", status: "active", appliedInstallations: 0, enrolledAt: "", lastSeenAt: "", siteId: "test-site", roles: ["worker"], connected: true, credentialRevoked: false, capabilities: { docker: false, gateway: false, tunnel: false, metrics: true, logs: false, executorVersions: { systemd: 1 }, runtimeCapabilities: [] }, networkCandidates: [], networkProfile: { serviceAddress: "10.0.0.2", enabledKinds: ["lan"], directPublic: false }, gatewayHealthy: false, remoteUpdateSupported: true }] });
    const container = await renderReadyApp();
    const overview = container.querySelector<HTMLElement>('[data-window-id="overview"]')!;
    const title = overview.querySelector<HTMLElement>('.desktop-window-bar')!;
    const initialLeft = parseFloat(overview.style.left);
    act(() => title.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", altKey: true, bubbles: true, cancelable: true })));
    const movedLeft = overview.style.left;
    expect(parseFloat(movedLeft)).toBeGreaterThan(initialLeft);
    const nodesButton = container.querySelector<HTMLButtonElement>('.desktop-dock button[aria-label="Hosts"]')!;
    await act(async () => nodesButton.click());
    const nodes = container.querySelector<HTMLElement>('[data-window-id="nodes"]')!;
    expect(container.querySelectorAll(".desktop-window")).toHaveLength(2);
    expect(overview.style.display).not.toBe("none");
    expect(nodes.dataset.active).toBe("true");
    expect(overview.style.left).toBe(movedLeft);
    await vi.waitFor(() => expect(nodes.querySelector('input[aria-label="Search nodes"]')).not.toBeNull());
    const search = nodes.querySelector<HTMLInputElement>('input[aria-label="Search nodes"]')!;
    expect(search).not.toBeNull();
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(search, "keep this query");
      search.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const dockOrder = [...container.querySelectorAll('.desktop-dock button')].map((button) => button.getAttribute("aria-label"));
    await act(async () => overview.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true })));
    expect(overview.dataset.active).toBe("true");
    expect(Number(overview.style.zIndex)).toBeGreaterThan(Number(nodes.style.zIndex));
    expect([...container.querySelectorAll('.desktop-dock button')].map((button) => button.getAttribute("aria-label"))).toEqual(dockOrder);
    await act(async () => nodesButton.click());
    expect(container.querySelectorAll(".desktop-window")).toHaveLength(2);
    expect(nodes.querySelector("input")).toBe(search);
    expect(search.value).toBe("keep this query");
    await act(async () => nodes.querySelector<HTMLButtonElement>('[aria-label="Minimize window"]')!.click());
    expect(nodes.style.display).toBe("none");
    expect(overview.dataset.active).toBe("true");
    expect(window.location.pathname).toBe("/overview");
    await act(async () => nodesButton.click());
    expect(nodes.style.display).not.toBe("none");
    expect(search.value).toBe("keep this query");
    await act(async () => nodes.querySelector<HTMLButtonElement>('[aria-label="Close window"]')!.click());
    expect(container.querySelector('[data-window-id="nodes"]')).toBeNull();
    expect(container.querySelector('[data-window-id="overview"]')).toBe(overview);
    expect(overview.style.left).toBe(movedLeft);
    expect(container.querySelectorAll('[role="main"]')).toHaveLength(1);
    await act(async () => container.querySelector<HTMLButtonElement>('.desktop-dock button[aria-label="Desktop"]')!.click());
    expect(overview.style.display).toBe("none");
    expect(container.querySelector('.desktop-home')).not.toBeNull();
  });

  it("shows only the foreground window on mobile and restores background windows on desktop", async () => {
    window.history.replaceState({}, "", "/overview");
    mockReadyCenter();
    const container = await renderReadyApp();
    await act(async () => container.querySelector<HTMLButtonElement>('.desktop-dock button[aria-label="Hosts"]')!.click());
    const overview = container.querySelector<HTMLElement>('[data-window-id="overview"]')!;
    const nodes = container.querySelector<HTMLElement>('[data-window-id="nodes"]')!;
    await act(async () => { vi.stubGlobal("innerWidth", 390); window.dispatchEvent(new Event("resize")); });
    expect(overview.style.display).toBe("none");
    expect(nodes.style.display).not.toBe("none");
    expect(container.querySelector('.desktop-surface')?.hasAttribute("inert")).toBe(true);
    await act(async () => container.querySelector<HTMLButtonElement>('.desktop-dock button[aria-label="Overview"]')!.click());
    expect(overview.style.display).not.toBe("none");
    expect(nodes.style.display).toBe("none");
    await act(async () => { vi.stubGlobal("innerWidth", 1576); window.dispatchEvent(new Event("resize")); });
    expect(overview.style.display).not.toBe("none");
    expect(nodes.style.display).not.toBe("none");
    expect(container.querySelector('[data-window-id="overview"]')).toBe(overview);
  });

  it("keeps one neutral startup screen during StrictMode initialization and delayed session data", async () => {
    const status = mockReadyCenter();
    const pending = deferred<Awaited<ReturnType<typeof api.status>>>();
    status.mockImplementation((signal) => new Promise((resolve, reject) => {
      pending.promise.then(resolve, reject);
      signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
    }));
    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    await act(async () => root?.render(<StrictMode><ThemeProvider><App /></ThemeProvider></StrictMode>));

    expect(container.textContent).toContain("Loading desktop…");
    expect(container.querySelector(".auth-shell")).toBeNull();
    expect(container.textContent).not.toContain("Center unavailable");
    expect(container.querySelector("#username")).toBeNull();
    expect(container.querySelector(".desktop-dock")).toBeNull();
    await act(async () => pending.resolve({ version: "test", agentInstallerAvailable: true, agentConnectionMode: "lan", agentConnectUrl: "https://center.example.com" }));
    await vi.waitFor(() => expect(container.querySelector(".desktop-dock")).not.toBeNull());
    expect(container.textContent).not.toContain("Center unavailable");
  });

  it.each(["setup", "unauthorized", "network"])("ignores obsolete %s initialization results", async (outcome) => {
    mockReadyCenter();
    const setup = await api.setupStatus();
    const obsolete = deferred<typeof setup>();
    const current = deferred<typeof setup>();
    vi.mocked(api.setupStatus).mockImplementationOnce(() => obsolete.promise).mockImplementationOnce(() => current.promise);
    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    await act(async () => root?.render(<StrictMode><ThemeProvider><App /></ThemeProvider></StrictMode>));
    await act(async () => {
      if (outcome === "setup") obsolete.resolve({ ...setup, administratorConfigured: false });
      else obsolete.reject(outcome === "unauthorized" ? new APIError("Authentication required", 401) : new TypeError("Failed to fetch"));
    });
    expect(container.textContent).toContain("Loading desktop…");
    expect(container.querySelector(".auth-shell")).toBeNull();
    expect(api.status).not.toHaveBeenCalled();
    await act(async () => current.resolve(setup));
    await vi.waitFor(() => expect(container.querySelector(".desktop-dock")).not.toBeNull());
  });

  it("shows a real startup failure and returns to neutral loading while retrying", async () => {
    mockReadyCenter();
    const setup = await api.setupStatus();
    vi.mocked(api.setupStatus).mockRejectedValueOnce(new TypeError("Failed to fetch"));
    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    await act(async () => root?.render(<ThemeProvider><App /></ThemeProvider>));
    expect(container.textContent).toContain("Center unavailable");
    const pending = deferred<typeof setup>();
    vi.mocked(api.setupStatus).mockImplementationOnce(() => pending.promise);
    await act(async () => [...container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent === "Retry")?.click());
    expect(container.textContent).toContain("Loading desktop…");
    expect(container.textContent).not.toContain("Center unavailable");
    await act(async () => pending.resolve(setup));
    await vi.waitFor(() => expect(container.querySelector(".desktop-dock")).not.toBeNull());
    expect(container.textContent).not.toContain("Unable to connect");
  });

  it("renders when the browser cannot observe system theme changes", async () => {
    Object.defineProperty(window, "matchMedia", { configurable: true, value: vi.fn().mockImplementation((query: string) => ({ matches: true, media: query })) });
    mockReadyCenter();

    const container = await renderReadyApp();

    expect(container.querySelector(".desktop-system-bar")).not.toBeNull();
    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });

  it("lets the user switch themes and remembers the choice", async () => {
    mockReadyCenter();
    const container = await renderReadyApp();
    const quickSettings = container.querySelector<HTMLButtonElement>('button[aria-label="Quick settings"]');
    expect(quickSettings).not.toBeNull();
    await act(async () => {
      quickSettings?.focus();
      quickSettings?.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true, cancelable: true }));
    });
    await vi.waitFor(() => expect(document.querySelector('button[aria-label="Switch to dark mode"]')).not.toBeNull());
    const toggle = document.querySelector<HTMLButtonElement>(
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
    vi.spyOn(api, "setupStatus").mockResolvedValue({ administratorConfigured: false, onboardingComplete: false, suggestedAgentConnectUrl: "", builtinHeadscaleAvailable: true, cloudflareOAuthAvailable: false, publicNetworkHelperAvailable: false, regionLookupAvailable: false, cloudflareConfigured: false, cloudflareAccessConfigured: false, cloudflareTurnstileConfigured: false, loginProtection: { captchaRequired: false }, publicAddressCandidates: [], gatewayAddressCandidates: [] });
    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    act(() => root?.render(<ThemeProvider><App /></ThemeProvider>));
    await vi.waitFor(() => expect(container.textContent).toContain("Create administrator"));
    expect(container.querySelector<HTMLInputElement>("#password")?.minLength).toBe(10);
    expect(container.textContent).toContain("At least 10 characters.");
  });

  it("requires a Turnstile token on the direct Cloudflare Tunnel login", async () => {
    vi.spyOn(api, "setupStatus").mockResolvedValue({ administratorConfigured: true, onboardingComplete: true, suggestedAgentConnectUrl: "https://center.example.com", builtinHeadscaleAvailable: true, cloudflareOAuthAvailable: true, publicNetworkHelperAvailable: true, regionLookupAvailable: true, cloudflareConfigured: true, cloudflareAccessConfigured: false, cloudflareTurnstileConfigured: true, loginProtection: { captchaRequired: true, turnstileSiteKey: "site-key" }, publicAddressCandidates: [], gatewayAddressCandidates: [] });
    vi.spyOn(api, "status").mockRejectedValue(new APIError("center: authentication required", 401, "authentication_required"));
    const renderTurnstile = vi.fn((_container: HTMLElement, options: Record<string, unknown>) => {
      (options.callback as (token: string) => void)("verified-token");
      return "widget-id";
    });
    window.turnstile = { render: renderTurnstile, remove: vi.fn() };
    const login = vi.spyOn(api, "login").mockRejectedValue(new APIError("center: sign-in failed", 401, "invalid_credentials", true));
    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    act(() => root?.render(<ThemeProvider><App /></ThemeProvider>));
    await vi.waitFor(() => expect(container.textContent).toContain("Continue with your administrator account."));
    await vi.waitFor(() => expect(renderTurnstile).toHaveBeenCalled());
    const username = container.querySelector<HTMLInputElement>("#username")!;
    const password = container.querySelector<HTMLInputElement>("#password")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(username, "admin");
      username.dispatchEvent(new Event("input", { bubbles: true }));
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(password, "wrong-password");
      password.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(password.type).toBe("password");
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="Show password"]')!.click());
    expect(password.type).toBe("text");
    expect(password.value).toBe("wrong-password");
    expect(login).not.toHaveBeenCalled();
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="Hide password"]')!.click());
    expect(password.type).toBe("password");
    expect(password.autocomplete).toBe("current-password");
    await act(async () => {
      [...container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "Sign in")?.click();
      await Promise.resolve();
    });
    expect(login).toHaveBeenCalledWith("admin", "wrong-password", "verified-token");
    expect(container.textContent).toContain("The username or password is incorrect.");
    expect(container.querySelector("#credential-error")?.getAttribute("role")).toBe("alert");
    expect(container.textContent).not.toMatch(/Turnstile|Cloudflare|backoff|lockout|consecutive failures|seconds|Retry in/);
    expect(renderTurnstile.mock.calls.length).toBeGreaterThan(1);
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="Change language"]')!.click());
    expect(container.querySelector("#credential-error")?.textContent).toBe("账号或密码不正确。");
    expect(username.value).toBe("admin");
    expect(password.value).toBe("wrong-password");
    expect(login).toHaveBeenCalledTimes(1);
  });

  it.each([
    ["en", "login_throttled", 429, "Unable to sign in right now. Try again later."],
    ["zh-CN", "login_throttled", 429, "暂时无法登录，请稍后再试。"],
    ["en", "login_protection_unavailable", 403, "Unable to sign in right now. Try again later."],
    ["zh-CN", "login_protection_unavailable", 403, "暂时无法登录，请稍后再试。"],
  ] as const)("keeps %s %s login feedback generic", async (language, code, statusCode, message) => {
    const status = mockReadyCenter();
    window.localStorage.setItem("vastora.language", language);
    status.mockRejectedValue(new APIError("Sign in to continue.", 401, "authentication_required"));
    const login = vi.spyOn(api, "login").mockRejectedValue(new APIError("internal_marker: throttle scope=account remaining=900", statusCode, code));
    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    act(() => root?.render(<ThemeProvider><App /></ThemeProvider>));
    await vi.waitFor(() => expect(container.querySelector("#username")).not.toBeNull());
    await act(async () => {
      container.querySelector("form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(login).toHaveBeenCalledTimes(1);
    expect(container.querySelector("#credential-error")?.textContent).toBe(message);
    expect(container.textContent).not.toMatch(/internal_marker|remaining|900|Turnstile|Cloudflare|bootstrap|backoff|lockout|consecutive failures|Retry in|秒|锁定/);
    expect(container.querySelector<HTMLButtonElement>('button[type="submit"]')?.disabled).toBe(false);
  });

  it("moves keyboard focus to the main content after navigation", async () => {
    mockReadyCenter();
    const container = await renderReadyApp();
    const nodes = container.querySelector<HTMLButtonElement>('button[aria-label="Hosts"]');
    act(() => nodes?.click());
    await vi.waitFor(() => expect(container.textContent).toContain("Add your first node"));
    await vi.waitFor(() => expect(document.activeElement).toBe(container.querySelector("#main-content")));
    expect(document.activeElement).toBe(container.querySelector("#main-content"));
  });

  it("marks cached data stale and offers retry after losing Center", async () => {
    const status = mockReadyCenter();
    const container = await renderReadyApp();
    status.mockRejectedValueOnce(new TypeError("Failed to fetch"));
    const nodes = container.querySelector<HTMLButtonElement>('button[aria-label="Hosts"]');
    await act(async () => nodes?.click());
    await vi.waitFor(() => expect(container.textContent).toContain("Connection to Center was interrupted"));
    expect(container.textContent).toContain("This page is showing the last successful data");
    expect(container.textContent).toContain("Retry now");
    const retry = [...container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "Retry now");
    expect(retry?.disabled).toBe(false);
    await act(async () => retry?.click());
    await vi.waitFor(() => expect(container.textContent).toContain("Add your first node"));
    expect(container.textContent).not.toContain("Connection to Center was interrupted");
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
    const visitOverview = () => {
      window.history.pushState({}, "", "/overview");
      window.dispatchEvent(new PopStateEvent("popstate"));
    };

    act(visitOverview);
    act(visitOverview);
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
    const nodes = container.querySelector<HTMLButtonElement>('button[aria-label="Hosts"]');
    const home = container.querySelector<HTMLButtonElement>('button[aria-label="Desktop"]');

    act(() => nodes?.click());
    act(() => home?.click());
    expect(staleSignal?.aborted).toBe(true);
    await vi.waitFor(() => expect(container.textContent).toContain("Vastora desktop"));
    await act(async () => { staleNodes.resolve({ agents: [] }); });
    expect(container.textContent).toContain("Vastora desktop");

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
    updates.mockResolvedValueOnce({ currentVersion: "fresh-version", latestVersion: "fresh-version", updateAvailable: false, releaseCheckAvailable: true, automatic: true, state: "idle" });
    const settings = container.querySelector<HTMLButtonElement>('button[aria-label="Control Panel"]');

    act(() => settings?.click());
    const check = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("Check"));
    expect(check).toBeDefined();
    await act(async () => { check?.click(); await Promise.resolve(); });
    await vi.waitFor(() => expect(container.textContent).toContain("fresh-version"));
    expect(staleSignal?.aborted).toBe(true);
    await act(async () => { stale.resolve({ currentVersion: "stale-version", latestVersion: "stale-version", updateAvailable: false, releaseCheckAvailable: true, automatic: true, state: "idle" }); });

    expect(container.textContent).toContain("fresh-version");
    expect(container.textContent).not.toContain("stale-version");
  });
});
