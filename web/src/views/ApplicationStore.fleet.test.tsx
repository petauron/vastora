// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AppData } from "../types";
import { ThemeProvider } from "../components/theme";
import { api } from "../api";
import { AppsView } from "./AppsView";
import { BatchUpgradeSheet, createBatchUpgradeRequest } from "./apps/BatchUpgradeSheet";
import { installedAppGroups } from "./installed-apps-model";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  window.sessionStorage.clear();
  vi.restoreAllMocks();
});

const agentAppKey = "vastora-official/senlun-agent";
const cpaAppKey = "vastora-official/cpa";
const telegramAppKey = "vastora-official/telegram-bot";

function multiSiteFleetFixture(): AppData {
  const baseAgent = {
    version: "0.2.3",
    operatingSystem: "linux",
    architecture: "amd64",
    status: "active",
    appliedInstallations: 2,
    enrolledAt: "2026-09-01T00:00:00Z",
    lastSeenAt: "2026-09-11T00:00:00Z",
    roles: ["worker"],
    connected: true,
    credentialRevoked: false,
    capabilities: {
      docker: true,
      gateway: false,
      tunnel: false,
      metrics: true,
      logs: false,
      executorVersions: { docker: 1, native: 2 },
      runtimeCapabilities: ["native-services"],
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
    sites: [
      { id: "site-jp", organizationId: "org", name: "日本-东京", code: "jp-tyo", description: "", timezone: "Asia/Tokyo", domainSuffix: "", status: "active", gatewayNodes: [], gatewayStatus: "inactive", createdAt: "", updatedAt: "" },
      { id: "site-us", organizationId: "org", name: "美西-洛杉矶", code: "us-lax", description: "", timezone: "America/Los_Angeles", domainSuffix: "", status: "active", gatewayNodes: [], gatewayStatus: "inactive", createdAt: "", updatedAt: "" },
    ],
    routes: [],
    actions: [],
    integrations: [],
    threeXUIControllerMigrations: [],
    services: [],
    publications: [],
    deployments: [],
    agents: [
      { ...baseAgent, id: "node-jp-1", name: "jp-edge-01", siteId: "site-jp" },
      { ...baseAgent, id: "node-jp-2", name: "jp-edge-02", siteId: "site-jp", connected: false },
      { ...baseAgent, id: "node-us-1", name: "us-west-01", siteId: "site-us" },
      { ...baseAgent, id: "node-us-2", name: "us-west-02", siteId: "site-us" },
    ],
    apps: [
      {
        key: agentAppKey,
        sourceId: "vastora-official",
        fetchedAt: "2026-09-11T00:00:00Z",
        manifestSha256: "a".repeat(64),
        app: {
          id: "senlun-agent",
          version: "0.2.0",
          packageRevision: 2,
          runtime: { kind: "native", version: 2, requiredCapabilities: ["native-services"] },
          name: { en: "Senlun Agent", "zh-CN": "Senlun 探针" },
          description: { en: "Host monitoring", "zh-CN": "主机监控" },
          hostAccess: true,
          config: [],
        },
      },
      {
        key: cpaAppKey,
        sourceId: "vastora-official",
        fetchedAt: "2026-09-11T00:00:00Z",
        manifestSha256: "b".repeat(64),
        app: {
          id: "cpa",
          version: "7.3.0",
          packageRevision: 1,
          runtime: { kind: "docker", version: 1 },
          name: { en: "CPA", "zh-CN": "CPA" },
          description: { en: "API gateway", "zh-CN": "API 网关" },
          hostAccess: false,
          config: [],
        },
      },
      {
        key: telegramAppKey,
        sourceId: "vastora-official",
        fetchedAt: "2026-09-11T00:00:00Z",
        manifestSha256: "c".repeat(64),
        app: {
          id: "telegram-bot",
          version: "1.1.0",
          packageRevision: 1,
          runtime: { kind: "docker", version: 1 },
          name: { en: "Telegram Bot", "zh-CN": "Telegram Bot" },
          description: { en: "Notifications", "zh-CN": "消息通知" },
          hostAccess: false,
          config: [],
        },
      },
    ],
    applications: [
      { id: "probe-jp-1", appKey: agentAppKey, nodeId: "node-jp-1", siteId: "site-jp", name: "Senlun Agent", image: "", runtime: "host", status: "running", installedVersion: "0.1.9", availableVersion: "0.2.0", updateAvailable: true, createdAt: "", updatedAt: "" },
      { id: "probe-jp-2", appKey: agentAppKey, nodeId: "node-jp-2", siteId: "site-jp", name: "Senlun Agent", image: "", runtime: "host", status: "running", installedVersion: "0.1.9", availableVersion: "0.2.0", updateAvailable: true, createdAt: "", updatedAt: "" },
      { id: "probe-us-1", appKey: agentAppKey, nodeId: "node-us-1", siteId: "site-us", name: "Senlun Agent", image: "", runtime: "host", status: "running", installedVersion: "0.1.9", availableVersion: "0.2.0", updateAvailable: true, createdAt: "", updatedAt: "" },
      { id: "probe-us-2", appKey: agentAppKey, nodeId: "node-us-2", siteId: "site-us", name: "Senlun Agent", image: "", runtime: "host", status: "running", installedVersion: "0.2.0", availableVersion: "0.2.0", updateAvailable: false, createdAt: "", updatedAt: "" },
      { id: "cpa-us-1", appKey: cpaAppKey, nodeId: "node-us-1", siteId: "site-us", name: "CPA", image: "cpa", runtime: "docker", status: "running", installedVersion: "7.2.0", availableVersion: "7.3.0", updateAvailable: true, createdAt: "", updatedAt: "" },
      { id: "tg-jp-1", appKey: telegramAppKey, nodeId: "node-jp-1", siteId: "site-jp", name: "Telegram Bot", image: "tg", runtime: "docker", status: "running", installedVersion: "1.1.0", availableVersion: "1.1.0", updateAvailable: false, createdAt: "", updatedAt: "" },
    ],
  } as unknown as AppData;
}

function renderStore(data: AppData, onRefresh = vi.fn()) {
  const mutate = vi.fn(async (action: () => Promise<unknown>) => { await action(); });
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root?.render(
    <ThemeProvider>
      <AppsView
        data={data}
        language="zh-CN"
        mutate={mutate as never}
        onOpenApp={vi.fn()}
        onRefresh={onRefresh}
        onSettings={vi.fn()}
        onStore={vi.fn()}
        workspaceKey={null}
      />
    </ThemeProvider>,
  ));
  return { container, mutate, onRefresh };
}

describe("Synology-style App Store fleet UX", () => {
  it("deduplicates updatable suites from the installed grid and supports inline host preview and cross-suite batch update", async () => {
    const data = multiSiteFleetFixture();
    const create = vi.spyOn(api, "createDeployment").mockResolvedValue({ id: "dep", state: "pending" } as never);
    const { container } = renderStore(data);

    // Sidebar badge reflects 2 updatable suites (Senlun Agent + CPA).
    const updateBadge = container.querySelector(".store-nav-badge-update");
    expect(updateBadge?.textContent).toBe("2");

    // Available updates section has 2 rows; Installed grid below has only Telegram Bot (no duplicates).
    const updatesSection = container.querySelector('[aria-labelledby="store-available-updates-heading"]');
    const installedSection = container.querySelector('[aria-labelledby="store-installed-grid-heading"]');
    expect(updatesSection?.querySelectorAll('[role="article"]')).toHaveLength(2);
    expect(installedSection?.querySelectorAll('[role="article"]')).toHaveLength(1);
    expect(installedSection?.textContent).toContain("Telegram Bot");
    expect(installedSection?.textContent).not.toContain("Senlun 探针");
    expect(installedSection?.textContent).not.toContain("CPA");

    // Expand inline host preview on Senlun Agent row.
    const expandButton = [...container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("查看待更新节点 (3)"));
    expect(expandButton).toBeDefined();
    act(() => expandButton?.click());
    expect(updatesSection?.textContent).toContain("jp-edge-01");
    expect(updatesSection?.textContent).toContain("jp-edge-02");
    expect(updatesSection?.textContent).toContain("节点离线");

    // Cross-suite "更新全部 (3)" opens grouped BatchUpgradeSheet across Senlun Agent (2 online) and CPA (1 online).
    const updateAll = [...container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("更新全部 (3)"));
    expect(updateAll).toBeDefined();
    act(() => updateAll?.click());

    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog?.textContent).toContain("批量更新 全部可用套件");
    expect(dialog?.textContent).toContain("已选择 3/3 台主机");
    expect(dialog?.textContent).toContain("已自动跳过 1 台不可更新主机");
    expect(dialog?.textContent).toContain("日本-东京");
    expect(dialog?.textContent).toContain("美西-洛杉矶");

    act(() => document.getElementById("batch-capability-native-services")?.click());
    await act(async () => {
      document.querySelector<HTMLButtonElement>('[role="dialog"] button[type="submit"]')?.click();
    });
    expect(create).toHaveBeenCalledTimes(3);
  });

  it("opens full-width Suite Detail Workbench with status filters, multi-select preselection, and back/forward navigation", () => {
    const data = multiSiteFleetFixture();
    const { container } = renderStore(data);

    // Back/forward disabled on initial screen.
    const backButton = () => container.querySelector<HTMLButtonElement>('[aria-label="返回上一页"]');
    const forwardButton = () => container.querySelector<HTMLButtonElement>('[aria-label="前进"]');
    expect(backButton()?.disabled).toBe(true);
    expect(forwardButton()?.disabled).toBe(true);

    // Navigate into Senlun Agent workbench.
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="管理 Senlun 探针"]')?.click());
    expect(backButton()?.disabled).toBe(false);
    expect(container.querySelectorAll("tbody tr[data-application-id]")).toHaveLength(4);

    // Filter to offline hosts only.
    const offlineChip = [...container.querySelectorAll<HTMLButtonElement>('[aria-label="按状态筛选"] button')].find((button) => button.textContent?.includes("离线"));
    act(() => offlineChip?.click());
    expect(container.querySelectorAll("tbody tr[data-application-id]")).toHaveLength(1);
    expect(container.querySelector("tbody")?.textContent).toContain("jp-edge-02");

    // Reset to updatable and select a single eligible host, then click "更新所选 (1)".
    const updatableChip = [...container.querySelectorAll<HTMLButtonElement>('[aria-label="按状态筛选"] button')].find((button) => button.textContent?.includes("可更新"));
    act(() => updatableChip?.click());
    expect(container.querySelectorAll("tbody tr[data-application-id]")).toHaveLength(3);

    act(() => container.querySelector<HTMLElement>('[aria-label="勾选 us-west-01"]')?.click());
    const updateSelected = [...container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("更新所选 (1)"));
    expect(updateSelected).toBeDefined();
    act(() => updateSelected?.click());

    // BatchUpgradeSheet opens with ONLY the preselected host checked out of 2 eligible hosts.
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain("已选择 1/2 台主机");
    act(() => [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find((button) => button.textContent === "取消")?.click());

    // Back returns to Installed home; Forward returns to workbench.
    act(() => backButton()?.click());
    expect(container.querySelector('[aria-labelledby="store-available-updates-heading"]')).not.toBeNull();
    expect(forwardButton()?.disabled).toBe(false);

    act(() => forwardButton()?.click());
    expect(container.querySelector(".store-detail-workbench")).not.toBeNull();
  });

  it("opens searchable site-grouped node picker when installing onto another host", () => {
    const data = multiSiteFleetFixture();
    const { container } = renderStore(data);

    act(() => container.querySelector<HTMLButtonElement>('[aria-label="管理 Telegram Bot"]')?.click());
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="安装 Telegram Bot"]')?.click());

    const combobox = document.querySelector<HTMLInputElement>('[role="dialog"] [role="combobox"]');
    expect(combobox).not.toBeNull();
    expect(combobox?.disabled).toBe(false);
    // jp-edge-01 already has Telegram Bot and jp-edge-02 is offline, so eligible nodes start with us-west-01.
    expect(combobox?.value).toBe("us-west-01");
  });

  it("groups active tasks by suite and jumps directly to the host in the suite workbench", () => {
    const data = multiSiteFleetFixture();
    data.deployments = [
      { id: "dep-1", agentId: "node-us-1", appKey: agentAppKey, appVersion: "0.2.0", state: "running", operation: "upgrade", deleteData: false, applicationId: "probe-us-1", createdAt: "2026-09-11T00:00:00Z", updatedAt: "2026-09-11T00:00:00Z" },
      { id: "dep-2", agentId: "node-jp-1", appKey: agentAppKey, appVersion: "0.2.0", state: "pending", operation: "upgrade", deleteData: false, applicationId: "probe-jp-1", createdAt: "2026-09-11T00:00:00Z", updatedAt: "2026-09-11T00:00:00Z" },
    ];
    const { container } = renderStore(data);

    act(() => container.querySelector<HTMLButtonElement>('[aria-label="查看 2 个进行中的任务"]')?.click());
    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog?.textContent).toContain("进行中的任务 · 2");
    expect(dialog?.textContent).toContain("执行中 1");
    expect(dialog?.textContent).toContain("等待中 1");

    act(() => dialog?.querySelector<HTMLButtonElement>('[aria-label="在 Senlun 探针 中定位 us-west-01"]')?.click());
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(container.querySelector(".store-detail-workbench")).not.toBeNull();
    expect(container.querySelectorAll("tbody tr[data-application-id]")).toHaveLength(1);
    expect(container.querySelector("tbody")?.textContent).toContain("us-west-01");
  });

  it("never widens an explicit batch preselection to all eligible hosts when preselected hosts become ineligible", () => {
    const data = multiSiteFleetFixture();
    const groups = installedAppGroups(data);
    const senlunGroup = groups.find((group) => group.appKey === agentAppKey)!;
    const instancesByID = new Map(senlunGroup.instances.map((instance) => [instance.application.id, instance]));
    // Explicitly preselect only probe-jp-2 (which is offline/ineligible), while probe-jp-1 and probe-us-1 remain eligible.
    const request = createBatchUpgradeRequest("Senlun 探针", senlunGroup.instances, data, ["probe-jp-2"]);

    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    act(() => root?.render(
      <ThemeProvider>
        <BatchUpgradeSheet
          data={data}
          instancesByID={instancesByID}
          language="zh-CN"
          onClose={vi.fn()}
          onSubmit={vi.fn(async () => ({ queued: 0, failed: [] }))}
          request={request}
        />
      </ThemeProvider>,
    ));

    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog?.textContent).toContain("已选择 0/2 台主机");
    expect(dialog?.querySelector<HTMLButtonElement>('button[type="submit"]')?.disabled).toBe(true);
  });

  it("locates fresh-install standard apps and Glivela controller tasks without dead ends and reports offline/stopped status accurately in All apps", () => {
    const data = multiSiteFleetFixture();
    // Remove Telegram Bot installation so it becomes an active fresh install, and make CPA stopped on an offline host.
    data.applications = data.applications.filter((item) => item.id !== "tg-jp-1");
    data.applications = data.applications.map((item) =>
      item.id === "cpa-us-1"
        ? { ...item, nodeId: "node-jp-2", siteId: "site-jp", updateAvailable: false, installedVersion: "7.3.0", availableVersion: "7.3.0" }
        : item,
    );
    data.deployments = [
      { id: "dep-fresh", agentId: "node-us-2", appKey: telegramAppKey, appVersion: "1.1.0", state: "running", operation: "install", deleteData: false, createdAt: "2026-09-11T00:00:00Z", updatedAt: "2026-09-11T00:00:00Z" },
    ];
    const { container } = renderStore(data);

    // All apps tab must report offline CPA as offline, never "运行中".
    const allAppsNav = [...container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("所有应用"));
    act(() => allAppsNav?.click());
    const cpaCard = [...container.querySelectorAll('[role="article"]')].find((card) => card.querySelector("h3")?.textContent === "CPA");
    expect(cpaCard?.textContent).toContain("1 台离线");
    expect(cpaCard?.textContent).not.toContain("运行中");

    // Locating a fresh-install task opens Telegram Bot's SuiteDetailView cleanly instead of "该套件已移除或暂不可用".
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="查看 1 个进行中的任务"]')?.click());
    act(() => document.querySelector<HTMLButtonElement>('[aria-label="在 Telegram Bot 中定位 us-west-02"]')?.click());
    expect(container.querySelector(".store-detail-workbench")).not.toBeNull();
    expect(container.textContent).toContain("Telegram Bot");
    expect(container.textContent).not.toContain("该套件已移除或暂不可用");
  });

  it("shows same-version revision summaries on Store Home and grouped cross-suite batch headers", () => {
    const data = multiSiteFleetFixture();
    data.apps[0].app.version = "0.2.1";
    data.apps[0].app.packageRevision = 10;
    data.applications = data.applications.map((item) => {
      if (item.appKey !== agentAppKey) return item;
      if (item.updateAvailable) {
        return {
          ...item,
          installedVersion: "0.2.1",
          installedPackageRevision: 9,
          availableVersion: "0.2.1",
          availablePackageRevision: 10,
        };
      }
      return {
        ...item,
        installedVersion: "0.2.1",
        installedPackageRevision: 10,
        availableVersion: "0.2.1",
        availablePackageRevision: 10,
      };
    });
    const { container } = renderStore(data);

    const updatesSection = container.querySelector('[aria-labelledby="store-available-updates-heading"]');
    expect(updatesSection?.textContent).toContain("最新在线版本：v0.2.1 (r10)");
    expect(updatesSection?.textContent).toContain("当前 v0.2.1 (r9)");

    const updateAll = [...container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("更新全部 (3)"));
    expect(updateAll).toBeDefined();
    act(() => updateAll?.click());

    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog?.textContent).toContain("→ v0.2.1 (r10)");
    expect(dialog?.textContent).toContain("v0.2.1 (r9 → r10)");
    expect(dialog?.textContent).toContain("→ v7.3.0");
    expect(dialog?.textContent).toContain("v7.2.0 → v7.3.0");
  });
});
