// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App, type Mutate } from "./App";
import { api, APIError } from "./api";
import { ThemeProvider } from "./components/theme";

const operation = vi.hoisted(() => ({ error: undefined as unknown, calls: 0 }));
vi.mock("./views/HomeView", async () => {
  const { useState } = await import("react");
  return { HomeView: ({ mutate }: { mutate: Mutate }) => {
    const [draft, setDraft] = useState("Unsaved account draft");
    const [error, setError] = useState("");
    return <form aria-label="Workspace account" onSubmit={async (event) => {
      event.preventDefault();
      try {
        await mutate(async () => { operation.calls++; throw operation.error; }, undefined, { reportError: false });
      } catch { setError("Save failed; draft retained"); }
    }}>
      <input aria-label="Account draft" value={draft} onChange={(event) => setDraft(event.target.value)} />
      <button type="submit">Save account</button>
      {error ? <p role="alert">{error}</p> : null}
    </form>;
  } };
});

// Deliberately distinct from the host constructor, as in a signed app bundle.
class WorkspaceAPIError extends Error {
  constructor(readonly status: number, readonly code = "request_failed", readonly captchaRequired = false) {
    super("Workspace request failed");
  }
}

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;

beforeEach(() => {
  operation.calls = 0;
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  Object.defineProperty(window, "matchMedia", { configurable: true, value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })) });
  window.localStorage.setItem("vastora.language", "en");
  window.history.replaceState({}, "", "/overview");
  vi.spyOn(api, "setupStatus").mockResolvedValue({ administratorConfigured: true, onboardingComplete: true, suggestedAgentConnectUrl: "https://center.example.com", builtinHeadscaleAvailable: true, cloudflareOAuthAvailable: true, publicNetworkHelperAvailable: true, regionLookupAvailable: true, cloudflareConfigured: false, cloudflareAccessConfigured: false, cloudflareTurnstileConfigured: false, loginProtection: { captchaRequired: false }, publicAddressCandidates: [], gatewayAddressCandidates: [] });
  vi.spyOn(api, "status").mockResolvedValue({ version: "test", agentInstallerAvailable: true, agentConnectionMode: "lan", agentConnectUrl: "https://center.example.com" });
  vi.spyOn(api, "apps").mockResolvedValue({ apps: [] });
  vi.spyOn(api, "applications").mockResolvedValue({ applications: [] });
  vi.spyOn(api, "agents").mockResolvedValue({ agents: [] });
  vi.spyOn(api, "services").mockResolvedValue({ services: [] });
  vi.spyOn(api, "publications").mockResolvedValue({ publications: [] });
  vi.spyOn(api, "sites").mockResolvedValue({ sites: [] });
  vi.spyOn(api, "actions").mockResolvedValue({ actions: [] });
  vi.spyOn(api, "centerUpdate").mockResolvedValue({ currentVersion: "test", updateAvailable: false, releaseCheckAvailable: true, automatic: false, state: "idle" });
});

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  window.history.replaceState({}, "", "/");
  window.localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function save(error: unknown) {
  operation.error = error;
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root?.render(<ThemeProvider><App /></ThemeProvider>));
  await vi.waitFor(() => expect(container.querySelector('[aria-label="Workspace account"]')).not.toBeNull());
  const form = container.querySelector<HTMLFormElement>("form")!;
  await act(async () => form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
  expect(operation.calls).toBe(1);
  return { container, form };
}

describe("independently bundled app mutation errors", () => {
  it.each([409, 500])("preserves the form after HTTP %s without reporting Center offline", async (status) => {
    const error = new WorkspaceAPIError(status);
    expect(error).not.toBeInstanceOf(APIError);
    const { container, form } = await save(error);
    expect(container.querySelector("form")).toBe(form);
    expect(container.querySelector<HTMLInputElement>("input")?.value).toBe("Unsaved account draft");
    expect(form.textContent).toContain("Save failed; draft retained");
    expect(container.textContent).not.toContain("Connection to Center was interrupted");
  });

  it("sends an independent HTTP 401 through the existing login flow", async () => {
    const { container } = await save(new WorkspaceAPIError(401));
    expect(container.querySelector("#username")).not.toBeNull();
    expect(container.textContent).toContain("Continue with your administrator account.");
    expect(container.textContent).not.toContain("Connection to Center was interrupted");
  });

  it.each([
    new TypeError("Failed to fetch"),
    { message: "Failure", status: "409", code: "conflict", captchaRequired: false },
    { status: 409 },
  ])("retains the existing disconnected handling for network or invalid errors %#", async (error) => {
    const { container, form } = await save(error);
    expect(container.querySelector("form")).toBe(form);
    expect(form.textContent).toContain("Save failed; draft retained");
    expect(container.textContent).toContain("Connection to Center was interrupted");
  });
});
