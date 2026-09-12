// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { APIError } from "./api";
import { emptyAppData, loadScreenData, screenFromPath } from "./app-data";
import { ThemeProvider } from "./components/theme";
import type { AgentView, AppData, Application, AppView, CatalogSource, CenterStatus } from "./types";
import { AppStore } from "./views/AppStore";
import { AppsView } from "./views/AppsView";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const status: CenterStatus = { version: "test", agentInstallerAvailable: true, agentConnectionMode: "lan", agentConnectUrl: "https://center.example.com" };
const node = { id: "node", name: "Test node", connected: true, status: "active", siteId: "site", capabilities: { docker: true }, networkProfile: { serviceAddress: "10.0.0.2" } } as AgentView;
const official: CatalogSource = {
  id: "vastora-official", displayName: "Vastora Official", url: "https://downloads.example.com/vastora/catalog",
  publicKey: "", customCASet: false, bearerTokenSet: false, enabled: true, status: "pending", refreshIntervalSeconds: 3600,
};

function catalogApp(sourceId: string): AppView {
  const name = sourceId === "vastora-official" ? "示例应用" : "第三方工具";
  return { key: `${sourceId}/notes`, sourceId, fetchedAt: "2026-09-12T00:00:00Z", app: {
    id: "notes", version: "2.0.0", name: { "zh-CN": name, en: name }, description: { "zh-CN": "目录应用", en: "Catalog app" }, hostAccess: false,
    config: [{ key: "label", label: { "zh-CN": "名称", en: "Name" }, description: { "zh-CN": "显示名称", en: "Display name" }, type: "string", required: false, secret: false }],
  } };
}

const installed: Application = { id: "installed", name: "示例应用", nodeId: node.id, siteId: "site", appKey: "vastora-official/notes", image: "example/notes:1.0.0", status: "running", runtime: "docker", installedVersion: "1.0.0", availableVersion: "2.0.0", updateAvailable: true, createdAt: "2026-09-12T00:00:00Z", updatedAt: "2026-09-12T00:00:00Z" };

let root: Root | undefined;
let container: HTMLDivElement;

beforeEach(() => {
  window.history.replaceState({}, "", "/apps");
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  window.history.replaceState({}, "", "/");
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function mockCenter() {
  const state = { sources: [official], apps: [catalogApp("community")], applications: [] as Application[], sourceStatus: 200, appStatus: 200 };
  const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    if (init?.signal?.aborted) throw new DOMException("Aborted", "AbortError");
    let responseStatus = 200;
    let body: unknown;
    switch (path) {
      case "/api/v1/status": body = status; break;
      case "/api/v1/catalog/apps": responseStatus = state.appStatus; body = { apps: state.apps }; break;
      case "/api/v1/catalog/sources": responseStatus = state.sourceStatus; body = { sources: state.sources }; break;
      case "/api/v1/registry-credentials": body = { credentials: [] }; break;
      case "/api/v1/agents": body = { agents: [node] }; break;
      case "/api/v1/deployments": body = { deployments: [] }; break;
      case "/api/v1/applications": body = { applications: state.applications }; break;
      case "/api/v1/services": body = { services: [] }; break;
      case "/api/v1/publications": body = { publications: [] }; break;
      case "/api/v1/integrations": body = { integrations: [] }; break;
      case "/api/v1/sites": body = { sites: [] }; break;
      case "/api/v1/three-x-ui-migrations": body = { migrations: [] }; break;
      case "/api/v1/network/center-remote-access": body = { available: true, enabled: false, status: "disabled" }; break;
      default: throw new Error(`Unexpected endpoint: ${path}`);
    }
    return new Response(JSON.stringify(responseStatus === 200 ? body : { error: "Internal upstream detail" }), { status: responseStatus, headers: { "Content-Type": "application/json" } });
  });
  vi.stubGlobal("fetch", fetch);
  return { state, fetch };
}

async function loadStore(previous?: AppData, signal?: AbortSignal) {
  const patch = await loadScreenData(screenFromPath(), signal);
  // Keep the same partial-state merge used by App, including when revisiting.
  const data = { ...(previous ?? emptyAppData(patch.status)), ...patch };
  act(() => root?.render(<AppStore data={data} language="zh-CN" onInstall={vi.fn()} />));
  return data;
}

describe("apps loader to AppStore integration", () => {
  it.each([
    ["pending", "官方目录等待首次验证"],
    ["failed", "官方目录暂不可用"],
    ["healthy", ""],
    ["expired", "官方目录需要刷新"],
  ] as const)("loads %s official source on direct /apps alongside third-party apps", async (sourceStatus, expectedNotice) => {
    const { state, fetch } = mockCenter();
    state.sources = [{ ...official, status: sourceStatus }];
    if (sourceStatus === "healthy" || sourceStatus === "expired") state.apps.unshift({ ...catalogApp("vastora-official"), installBlocked: sourceStatus === "expired" });

    const data = await loadStore();

    expect(data.sources[0].status).toBe(sourceStatus);
    if (expectedNotice) expect(container.querySelector('[role="status"]')?.textContent).toContain(expectedNotice);
    else expect(container.querySelector('[role="status"]')).toBeNull();
    expect(container.textContent).toContain("第三方目录 · community");
    expect(container.querySelector<HTMLButtonElement>('[aria-label="安装 第三方工具"]')?.disabled).toBe(false);
    if (sourceStatus === "expired") expect(container.querySelector<HTMLButtonElement>('[aria-label="安装 示例应用"]')?.disabled).toBe(true);
    expect(fetch.mock.calls.filter(([path]) => path === "/api/v1/catalog/sources")).toHaveLength(1);
    expect(fetch.mock.calls.some(([path]) => String(path).includes("system/domain"))).toBe(false);
  });

  it("explains a first-fetch pending source even when no app has been cached", async () => {
    const { state } = mockCenter();
    state.apps = [];
    await loadStore();
    expect(container.querySelector('[role="status"]')?.textContent).toContain("官方目录等待首次验证");
    expect(container.textContent).toContain("已安装应用不受影响");
  });

  it("clears stale sources on API failure and replaces both error and metadata after refresh or reentry", async () => {
    const { state, fetch } = mockCenter();
    let data = await loadStore();
    state.sources = [{ ...official, status: "healthy", catalogRevision: 2 }];
    state.apps.unshift(catalogApp("vastora-official"));
    data = await loadStore(data);
    expect(container.querySelector('[role="status"]')).toBeNull();

    state.sourceStatus = 503;
    data = await loadStore(data);
    expect(data.sources).toEqual([]);
    expect(container.textContent).toContain("暂时无法读取目录状态");
    expect(container.textContent).not.toContain("Internal upstream detail");
    expect(data.apps).toHaveLength(2);

    state.sourceStatus = 200;
    state.sources = [{ ...official, status: "expired", catalogRevision: 2 }];
    state.apps[0] = { ...state.apps[0], installBlocked: true };
    data = await loadStore(data);
    expect(data.catalogSourcesError).toBeUndefined();
    expect(container.textContent).toContain("官方目录需要刷新");
    expect(container.textContent).not.toContain("暂时无法读取目录状态");

    window.history.replaceState({}, "", "/nodes");
    data = { ...data, ...await loadScreenData(screenFromPath()) };
    window.history.replaceState({}, "", "/apps");
    state.sources = [{ ...official, status: "healthy", catalogRevision: 3 }];
    state.apps[0] = { ...state.apps[0], installBlocked: false };
    data = await loadStore(data);
    expect(data.sources[0].catalogRevision).toBe(3);
    expect(container.querySelector('[role="status"]')).toBeNull();
    expect(fetch.mock.calls.filter(([path]) => path === "/api/v1/catalog/sources")).toHaveLength(5);
  });

  it("starts source and app requests independently and forwards the same cancellation signal", async () => {
    const { fetch } = mockCenter();
    const respond = fetch.getMockImplementation()!;
    let release!: () => void;
    const gate = new Promise<void>(resolve => { release = resolve; });
    fetch.mockImplementation(async (input, init) => {
      if (input === "/api/v1/catalog/sources") await gate;
      return respond(input, init);
    });
    const controller = new AbortController();
    const loading = loadStore(undefined, controller.signal);
    expect(fetch.mock.calls.some(([path]) => path === "/api/v1/catalog/apps")).toBe(true);
    expect(fetch.mock.calls.some(([path]) => path === "/api/v1/catalog/sources")).toBe(true);
    for (const [, init] of fetch.mock.calls) expect(init?.signal).toBe(controller.signal);
    release();
    await loading;
  });

  it("does not turn authentication or cancellation failures into a catalog warning", async () => {
    const { state } = mockCenter();
    state.sourceStatus = 401;
    await expect(loadScreenData("apps")).rejects.toMatchObject({ status: 401 });
    const controller = new AbortController();
    controller.abort();
    await expect(loadScreenData("apps", controller.signal)).rejects.toMatchObject({ name: "AbortError" });
  });

  it("does not publish a partial successful patch when the app catalog API fails", async () => {
    const { state } = mockCenter();
    await loadStore();
    state.appStatus = 503;
    await expect(loadStore()).rejects.toBeInstanceOf(APIError);
    expect(container.textContent).toContain("官方目录等待首次验证");
  });

  it("retains configure and uninstall management after source status fails without enabling expired upgrades", async () => {
    const { state } = mockCenter();
    state.sourceStatus = 503;
    state.apps = [{ ...catalogApp("vastora-official"), installBlocked: true }];
    state.applications = [installed];
    const data = await loadStore();
    act(() => root?.render(<ThemeProvider><AppsView data={data} language="zh-CN" mutate={async () => undefined} /></ThemeProvider>));
    const manage = container.querySelector<HTMLButtonElement>('[data-application-id="installed"] button[aria-label^="管理"]');
    expect(manage).not.toBeNull();
    act(() => manage?.click());
    const button = (text: string) => [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find(value => value.textContent?.includes(text));
    expect(button("升级到")?.disabled).toBe(true);
    expect(button("修改配置")?.disabled).toBe(false);
    expect(button("卸载")?.disabled).toBe(false);
    act(() => button("修改配置")?.click());
    const input = document.querySelector<HTMLInputElement>('[role="dialog"] #config-label');
    expect(input).not.toBeNull();
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(input, "Updated name");
      input?.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(document.querySelector<HTMLButtonElement>('[role="dialog"] button[type="submit"]')?.disabled).toBe(false);
  });
});
