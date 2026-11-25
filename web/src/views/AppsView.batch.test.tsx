// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AppData } from "../types";
import { ThemeProvider } from "../components/theme";
import { APIError, api } from "../api";
import { AppsView } from "./AppsView";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  window.sessionStorage.clear();
  vi.restoreAllMocks();
});

const appKey = "vastora-official/senlun-agent";
const manifestDigest = "b".repeat(64);

function fleetFixture(): AppData {
  const baseAgent = {
    version: "0.2.3",
    operatingSystem: "linux",
    architecture: "amd64",
    status: "active",
    appliedInstallations: 1,
    enrolledAt: "2026-09-01T00:00:00Z",
    lastSeenAt: "2026-09-11T00:00:00Z",
    siteId: "site-a",
    roles: ["worker"],
    connected: true,
    credentialRevoked: false,
    capabilities: {
      docker: false,
      gateway: false,
      tunnel: false,
      metrics: true,
      logs: false,
      executorVersions: { native: 2 },
      runtimeCapabilities: ["native-services", "host-tcp-budget", "shared-caddy"],
    },
    networkCandidates: [],
    networkProfile: { serviceAddress: "100.64.0.10", enabledKinds: ["headscale"], directPublic: false },
    gatewayHealthy: false,
    remoteUpdateSupported: true,
  };
  return {
    status: {},
    centerUpdate: {},
    systemDomain: {},
    centerRemoteAccess: null,
    registryCredentials: [],
    sources: [],
    organizations: [],
    sites: [{ id: "site-a", organizationId: "org", name: "Edge", code: "edge", description: "", timezone: "UTC", domainSuffix: "", status: "active", gatewayNodes: [], gatewayStatus: "inactive", createdAt: "", updatedAt: "" }],
    routes: [],
    actions: [],
    integrations: [],
    threeXUIControllerMigrations: [],
    services: [],
    publications: [],
    deployments: [],
    agents: [
      { ...baseAgent, id: "node-a", name: "Node Alpha" },
      { ...baseAgent, id: "node-b", name: "Node Beta" },
      { ...baseAgent, id: "node-c", name: "Node Gamma", connected: false },
      { ...baseAgent, id: "node-d", name: "Node Delta" },
    ],
    apps: [{
      key: appKey,
      sourceId: "vastora-official",
      fetchedAt: "2026-09-11T00:00:00Z",
      manifestSha256: manifestDigest,
      app: {
        id: "senlun-agent",
        version: "0.1.0-alpha.31",
        packageRevision: 9,
        runtime: { kind: "native", version: 2, requiredCapabilities: ["native-services"] },
        name: { en: "Senlun Agent", "zh-CN": "Senlun 探针" },
        description: { en: "Host monitoring", "zh-CN": "主机监控" },
        hostAccess: true,
        config: [],
      },
    }],
    applications: [
      { id: "app-a", appKey, nodeId: "node-a", siteId: "site-a", name: "Senlun Agent", image: "", runtime: "host", status: "running", installedVersion: "0.1.0-alpha.30", availableVersion: "0.1.0-alpha.31", updateAvailable: true, createdAt: "", updatedAt: "" },
      { id: "app-b", appKey, nodeId: "node-b", siteId: "site-a", name: "Senlun Agent", image: "", runtime: "host", status: "running", installedVersion: "0.1.0-alpha.30", availableVersion: "0.1.0-alpha.31", updateAvailable: true, createdAt: "", updatedAt: "" },
      { id: "app-c", appKey, nodeId: "node-c", siteId: "site-a", name: "Senlun Agent", image: "", runtime: "host", status: "running", installedVersion: "0.1.0-alpha.30", availableVersion: "0.1.0-alpha.31", updateAvailable: true, createdAt: "", updatedAt: "" },
      { id: "app-d", appKey, nodeId: "node-d", siteId: "site-a", name: "Senlun Agent", image: "", runtime: "host", status: "running", installedVersion: "0.1.0-alpha.31", availableVersion: "0.1.0-alpha.31", updateAvailable: false, createdAt: "", updatedAt: "" },
    ],
  } as unknown as AppData;
}

function renderApps(data: AppData, options: { workspaceKey?: string | null; mutate?: ReturnType<typeof vi.fn> } = {}) {
  const mutate = options.mutate ?? vi.fn(async (action: () => Promise<unknown>) => { await action(); });
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  const renderElement = (nextData: AppData) => (
    <ThemeProvider>
      <AppsView
        data={nextData}
        language="zh-CN"
        mutate={mutate as never}
        onOpenApp={vi.fn()}
        onSettings={vi.fn()}
        onStore={vi.fn()}
        workspaceKey={options.workspaceKey ?? null}
      />
    </ThemeProvider>
  );
  act(() => root?.render(renderElement(data)));
  return { container, mutate, rerender: (nextData: AppData) => act(() => root?.render(renderElement(nextData))) };
}

describe("batch application upgrades and stable task UI", () => {
  it("updates all eligible hosts from the App Store sheet with one capability approval and one refresh", async () => {
    const data = fleetFixture();
    const create = vi.spyOn(api, "createDeployment").mockResolvedValue({ id: "dep", state: "pending" } as never);
    const { container, mutate } = renderApps(data);

    act(() => container.querySelector<HTMLButtonElement>('[aria-label="管理 Senlun 探针"]')?.click());
    const batchButton = [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("全部更新 (2)"));
    expect(batchButton).toBeDefined();

    act(() => batchButton!.click());
    expect(document.body.textContent).toContain("批量更新 Senlun 探针");
    expect(document.body.textContent).toContain("已自动跳过 1 台不可更新主机");
    expect(document.body.textContent).toContain("Node Gamma");
    expect(document.body.textContent).toContain("节点离线");
    expect(document.body.textContent).not.toContain("Node Delta");

    const submit = () => document.querySelector<HTMLButtonElement>('[role="dialog"] button[type="submit"]');
    expect(submit()?.disabled).toBe(true);
    expect(document.body.textContent).toContain("管理宿主机原生服务");

    act(() => document.getElementById("batch-capability-native-services")?.click());
    expect(submit()?.disabled).toBe(false);

    await act(async () => {
      submit()?.click();
    });

    expect(create).toHaveBeenCalledTimes(2);
    expect(create).toHaveBeenNthCalledWith(1, "node-a", appKey, {}, "upgrade", false, undefined, undefined, undefined, ["native-services"], { packageRevision: 9, manifestSha256: manifestDigest });
    expect(create).toHaveBeenNthCalledWith(2, "node-b", appKey, {}, "upgrade", false, undefined, undefined, undefined, ["native-services"], { packageRevision: 9, manifestSha256: manifestDigest });
    expect(mutate).toHaveBeenCalledTimes(1);
    expect(mutate).toHaveBeenCalledWith(expect.any(Function), "已提交 2 台更新任务。", { reportError: false });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("supports deselecting individual hosts and keeps partial failures inline without auto-retrying", async () => {
    const data = fleetFixture();
    const create = vi.spyOn(api, "createDeployment")
      .mockResolvedValueOnce({ id: "dep-a", state: "pending" } as never)
      .mockRejectedValueOnce(new APIError("node busy", 409));
    const { container, mutate } = renderApps(data);

    act(() => container.querySelector<HTMLButtonElement>('[aria-label="管理 Senlun 探针"]')?.click());
    act(() => [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("全部更新 (2)"))?.click());

    const betaCheckbox = document.getElementById("batch-target-app-b")!;
    act(() => betaCheckbox.click());
    expect(document.body.textContent).toContain("已选择 1/2 台主机");
    expect(document.querySelector<HTMLButtonElement>('[role="dialog"] button[type="submit"]')?.textContent).toContain("开始更新 (1)");
    act(() => betaCheckbox.click());
    expect(document.body.textContent).toContain("已选择 2/2 台主机");

    act(() => document.getElementById("batch-capability-native-services")?.click());
    await act(async () => {
      document.querySelector<HTMLButtonElement>('[role="dialog"] button[type="submit"]')?.click();
    });

    expect(create).toHaveBeenCalledTimes(2);
    expect(mutate).toHaveBeenCalledTimes(1);
    expect(mutate).toHaveBeenCalledWith(expect.any(Function), undefined, { reportError: false });
    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog).not.toBeNull();
    expect(dialog?.textContent).toContain("已提交 1 台，1 台提交失败");
    expect(dialog?.textContent).toContain("Node Beta");
    expect(dialog?.querySelector<HTMLButtonElement>('button[type="submit"]')?.textContent).toContain("开始更新 (1)");
  });

  it("halts mid-batch on HTTP 401 and delegates authentication recovery to mutate", async () => {
    const data = fleetFixture();
    const unauthorized = new APIError("authentication required", 401, "authentication_required");
    const create = vi.spyOn(api, "createDeployment").mockRejectedValueOnce(unauthorized);
    const mutate = vi.fn(async (action: () => Promise<unknown>) => { await action(); });
    const { container } = renderApps(data, { mutate });

    act(() => container.querySelector<HTMLButtonElement>('[aria-label="管理 Senlun 探针"]')?.click());
    act(() => [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("全部更新 (2)"))?.click());
    act(() => document.getElementById("batch-capability-native-services")?.click());

    await act(async () => {
      document.querySelector<HTMLButtonElement>('[role="dialog"] button[type="submit"]')?.click();
    });

    expect(create).toHaveBeenCalledTimes(1);
    expect(mutate).toHaveBeenCalledTimes(1);
  });

  it("blocks batch submission when catalog version, revision, digest, or expiry changes while open", async () => {
    const data = fleetFixture();
    data.apps[0].catalogExpiresAt = "2030-01-01T00:00:00Z";
    const now = vi.spyOn(Date, "now").mockReturnValue(Date.parse("2029-12-31T23:59:59Z"));
    const create = vi.spyOn(api, "createDeployment");
    const { container, rerender } = renderApps(data);

    act(() => container.querySelector<HTMLButtonElement>('[aria-label="管理 Senlun 探针"]')?.click());
    act(() => [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("全部更新 (2)"))?.click());
    act(() => document.getElementById("batch-capability-native-services")?.click());

    // Simulate catalog expiry crossing Date.now() before another refresh.
    now.mockReturnValue(Date.parse("2030-01-01T00:00:00Z"));
    await act(async () => {
      document.querySelector<HTMLButtonElement>('[role="dialog"] button[type="submit"]')?.click();
    });
    expect(create).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("目录需要重新验证");

    // Reset clock and verify version/revision change forces reopen.
    now.mockReturnValue(Date.parse("2029-12-31T23:59:59Z"));
    const nextData = fleetFixture();
    nextData.apps[0].catalogExpiresAt = "2030-01-01T00:00:00Z";
    nextData.apps[0].app.packageRevision = 10;
    rerender(nextData);

    expect(document.getElementById("batch-upgrade-catalog-error")?.textContent).toContain("目录中的版本已更新");
    expect(document.querySelector<HTMLButtonElement>('[role="dialog"] button[type="submit"]')?.disabled).toBe(true);
    expect(create).not.toHaveBeenCalled();
  });

  it("disambiguates mixed recipes on the same host, ignores blocked up-to-date siblings, and resets approval when recipe scope expands", () => {
    const data = fleetFixture();
    data.apps = [
      {
        key: "vastora-official/glivela-entry",
        sourceId: "vastora-official",
        fetchedAt: "2026-09-11T00:00:00Z",
        manifestSha256: "c".repeat(64),
        permissionDetails: ["entry-scope"],
        app: {
          id: "glivela-entry",
          version: "0.2.0",
          packageRevision: 2,
          runtime: { kind: "native", version: 2, requiredCapabilities: ["native-services", "host-tcp-budget"] },
          name: { en: "Glivela Entry", "zh-CN": "Glivela 入口" },
          description: { en: "Entry", "zh-CN": "入口" },
          hostAccess: true,
          config: [],
        },
      },
      {
        key: "vastora-official/glivela-node",
        sourceId: "vastora-official",
        fetchedAt: "2026-09-11T00:00:00Z",
        manifestSha256: "d".repeat(64),
        permissionDetails: ["node-scope"],
        app: {
          id: "glivela-node",
          version: "0.2.0",
          packageRevision: 2,
          runtime: { kind: "native", version: 2, requiredCapabilities: ["native-services"] },
          name: { en: "Glivela Node", "zh-CN": "Glivela 节点" },
          description: { en: "Node", "zh-CN": "节点" },
          hostAccess: true,
          config: [],
        },
      },
      {
        key: "vastora-official/glivela-dual",
        sourceId: "vastora-official",
        fetchedAt: "2026-09-11T00:00:00Z",
        manifestSha256: "e".repeat(64),
        installBlocked: true,
        app: {
          id: "glivela-dual",
          version: "0.2.0",
          packageRevision: 2,
          runtime: { kind: "native", version: 2, requiredCapabilities: ["native-services"] },
          name: { en: "Glivela Dual", "zh-CN": "Glivela 双功能" },
          description: { en: "Dual", "zh-CN": "双功能" },
          hostAccess: true,
          config: [],
        },
      },
    ];
    data.applications = [
      { id: "g-entry", appKey: "vastora-official/glivela-entry", nodeId: "node-a", siteId: "site-a", name: "Glivela", image: "", runtime: "host", status: "running", installedVersion: "0.1.9", availableVersion: "0.2.0", updateAvailable: true, createdAt: "", updatedAt: "" },
      { id: "g-node", appKey: "vastora-official/glivela-node", nodeId: "node-a", siteId: "site-a", name: "Glivela", image: "", runtime: "host", status: "running", installedVersion: "0.1.9", availableVersion: "0.2.0", updateAvailable: true, createdAt: "", updatedAt: "" },
      { id: "g-dual", appKey: "vastora-official/glivela-dual", nodeId: "node-b", siteId: "site-a", name: "Glivela", image: "", runtime: "host", status: "running", installedVersion: "0.2.0", availableVersion: "0.2.0", updateAvailable: false, createdAt: "", updatedAt: "" },
    ];
    const { container } = renderApps(data);

    act(() => container.querySelector<HTMLButtonElement>('[aria-label="管理 Glivela"]')?.click());
    act(() => [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("全部更新 (2)"))?.click());

    // Blocked but already up-to-date sibling (glivela-dual) does not block eligible upgrades.
    expect(document.getElementById("batch-upgrade-catalog-error")).toBeNull();
    // Both packages on the same host have distinct labels.
    expect(document.querySelector('[aria-label="选择 Node Alpha · 接收客户端连接"]')).not.toBeNull();
    expect(document.querySelector('[aria-label="选择 Node Alpha · 提供出口访问"]')).not.toBeNull();

    // Uncheck entry -> approve native-services for node only -> re-check entry -> approval must reset.
    act(() => document.getElementById("batch-target-g-entry")?.click());
    expect(document.getElementById("batch-capability-host-tcp-budget")).toBeNull();
    act(() => document.getElementById("batch-capability-native-services")?.click());
    const submit = () => document.querySelector<HTMLButtonElement>('[role="dialog"] button[type="submit"]');
    expect(submit()?.disabled).toBe(false);

    act(() => document.getElementById("batch-target-g-entry")?.click());
    expect(submit()?.disabled).toBe(true);
    expect(document.getElementById("batch-capability-native-services")?.getAttribute("aria-checked")).toBe("false");
    expect(document.getElementById("batch-capability-host-tcp-budget")?.getAttribute("aria-checked")).toBe("false");
  });

  it("keeps routine active tasks in the compact Sheet trigger while leaving reconciliation recovery inline", () => {
    const data = fleetFixture();
    data.deployments = [
      { id: "dep-1", agentId: "node-a", appKey, appVersion: "0.1.0-alpha.31", state: "running", operation: "upgrade", deleteData: false, applicationId: "app-a", createdAt: "2026-09-11T00:00:00Z", updatedAt: "2026-09-11T00:00:00Z" },
    ];
    const { container, rerender } = renderApps(data, { workspaceKey: appKey });

    // Routine in-progress task does not inject a top-of-flow <details> block.
    expect(container.querySelector(".apps-workspace > details")).toBeNull();
    const activeButton = container.querySelector<HTMLButtonElement>('[aria-label="查看 1 个进行中的任务"]');
    expect(activeButton?.textContent).toContain("进行中 · 1");

    // Only Node Beta remains eligible (Node Alpha has an active task; Node Gamma is offline).
    const batchButton = [...container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("全部更新 (1)"));
    expect(batchButton).toBeDefined();

    // Per-row update action lives in the Actions cell rather than stacking in the Status cell.
    const betaRow = container.querySelector<HTMLTableRowElement>('[data-application-id="app-b"]')!;
    const cells = betaRow.querySelectorAll("td");
    expect(cells[1].querySelector('[aria-label="更新 Node Beta 的应用"]')).toBeNull();
    expect(cells[3].querySelector('[aria-label="更新 Node Beta 的应用"]')).not.toBeNull();

    // Opening the compact active-tasks trigger reveals the running task inside a Sheet.
    act(() => activeButton?.click());
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain("进行中的任务 · 1");
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain("Node Alpha");

    // A reconciliationRequired task stays immediately visible inline in an open <details> block.
    const withRecovery = fleetFixture();
    withRecovery.deployments = [
      ...data.deployments,
      { id: "dep-recover", agentId: "node-b", appKey, appVersion: "0.1.0-alpha.31", state: "failed", reconciliationRequired: true, operation: "upgrade", deleteData: false, applicationId: "app-b", createdAt: "2026-09-11T00:00:00Z", updatedAt: "2026-09-11T00:00:00Z" },
    ];
    rerender(withRecovery);
    expect(container.querySelector(".apps-workspace > details")?.textContent).toContain("核对并恢复");
  });
});
