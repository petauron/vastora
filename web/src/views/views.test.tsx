// @vitest-environment jsdom

import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AppData } from "../App";
import type { ApplicationCommand } from "../types";
import { APIError, api } from "../api";
import { vastoraDomainDefaults } from "../lib/network";
import { AppsView } from "./AppsView";
import { HomeView } from "./HomeView";
import { NetworkView } from "./NetworkView";
import { NodesView, agentInstallCommand, validCenterURL } from "./NodesView";
import { SettingsView } from "./SettingsView";
import { SetupWizard } from "./SetupWizard";
import { CloudflareOAuthConnect } from "./CloudflareOAuthConnect";
import { CenterUpdateCard } from "./CenterUpdateCard";
import { ThemeProvider } from "../components/theme";
import { defaultPublicationHostname, defaultRealityHostname } from "./appAccess";
import { CopyButton, userError } from "./shared";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  window.sessionStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

const dashboard = (): AppData => ({
  status: { version: "test", agentInstallerAvailable: true, agentConnectionMode: "lan", agentConnectUrl: "https://center.example.com" },
  centerUpdate: { currentVersion: "test", latestVersion: "test", updateAvailable: false, automatic: true, state: "idle", checkedAt: "2026-08-18T00:00:00Z" },
  sources: [], organizations: [], routes: [], actions: [], integrations: [], threeXUIControllerMigrations: [],
  systemDomain: { namespace: "vastora.example.com", centerUrl: "https://center.vastora.example.com", headscaleUrl: "https://headscale.vastora.example.com", cloudflareZone: "example.com", aliases: [], activePublications: 0, pendingCleanup: 0, builtinHeadscale: true, cloudflareOAuthAvailable: true },
  sites: [{ id: "site", organizationId: "org", name: "Home", code: "home", description: "", timezone: "Asia/Singapore", domainSuffix: "home.example", status: "active", gatewayNodes: ["agent"], gatewayStatus: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }],
  agents: [{ id: "agent", name: "home-server", version: "test", operatingSystem: "linux", architecture: "amd64", status: "active", appliedInstallations: 1, enrolledAt: "2026-08-18T00:00:00Z", lastSeenAt: "2026-08-18T00:00:00Z", connected: true, siteId: "site", roles: ["worker", "gateway"], capabilities: { docker: true, gateway: true, tunnel: true, metrics: false, logs: false }, networkCandidates: [{ address: "192.168.1.2", interface: "eth0", kind: "lan", observedAt: "2026-08-18T00:00:00Z" }], networkProfile: { serviceAddress: "192.168.1.2", lanAddress: "192.168.1.2", enabledKinds: ["lan"], directPublic: false }, gatewayHealthy: true }],
  apps: [{ key: "vastora-official/komari-agent", sourceId: "vastora-official", fetchedAt: "2026-08-18T00:00:00Z", app: { id: "komari-agent", version: "1.2.60", name: { en: "Komari Agent", "zh-CN": "Komari 探针" }, description: { en: "Monitoring", "zh-CN": "监控探针" }, hostAccess: true, config: [] } }],
  applications: [
    { id: "running", name: "Komari Agent", nodeId: "agent", siteId: "site", appKey: "vastora-official/komari-agent", image: "image", status: "running", runtime: "docker", installedVersion: "1.2.60", availableVersion: "1.2.60", updateAvailable: false, createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" },
    { id: "failed", name: "Failed", nodeId: "agent", siteId: "site", appKey: "vastora-official/failed", image: "image", status: "failed", runtime: "docker", updateAvailable: false, createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }
  ],
  deployments: [], services: [], publications: []
});

const realityDashboard = () => {
  const data = dashboard();
  data.agents[0].networkProfile = { serviceAddress: "10.0.0.10", publicAddress: "203.0.113.10", enabledKinds: ["lan", "public"], directPublic: true };
  data.sites[0].domainSuffix = "vastora.example.com";
  data.apps = [{ key: "vastora-official/3x-ui", sourceId: "vastora-official", fetchedAt: "2026-08-18T00:00:00Z", app: { id: "3x-ui", version: "3.6.0", name: { en: "3x-ui", "zh-CN": "3x-ui" }, description: { en: "Proxy management", "zh-CN": "代理管理" }, hostAccess: true, config: [] } }];
  data.applications = [{ ...data.applications[0], id: "three-x-ui", name: "3x-ui", appKey: "vastora-official/3x-ui", role: "master", installedVersion: "3.6.0", availableVersion: "3.6.0" }];
  return data;
};

function render(element: ReactNode) {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root?.render(<ThemeProvider>{element}</ThemeProvider>));
  return container;
}

function mockCommandEvent(command: ApplicationCommand) {
  class CommandEventSource {
    onmessage: ((event: MessageEvent<string>) => void) | null = null;

    constructor(readonly url: string, readonly init?: EventSourceInit) {
      queueMicrotask(() => this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(command) })));
    }

    close() {}
  }
  vi.stubGlobal("EventSource", CommandEventSource);
}

describe("network and app views", () => {
  it("shows one current action at a time during first-time setup", () => {
    const data = dashboard();
    data.agents = [];
    data.applications = [];
    let destination = "";
    const container = render(<HomeView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={(screen) => { destination = screen; }} />);
    expect(container.textContent).toContain("完成首次设置");
    expect(container.textContent).not.toContain("管理员账号已创建");
    expect(container.textContent).toContain("一次只完成当前步骤");
    const add = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加节点"));
    act(() => add?.click());
    expect(destination).toBe("nodes");
  });

  it("groups recent task events into one home activity", () => {
    const data = dashboard();
    data.actions = [
      { id: "3", taskId: "install", agentId: "agent", kind: "application.apply", revision: 1, event: "succeeded", message: "install vastora-official/komari-agent", createdAt: "2026-08-18T00:03:00Z" },
      { id: "2", taskId: "install", agentId: "agent", kind: "application.apply", revision: 1, event: "claimed", message: "install vastora-official/komari-agent", createdAt: "2026-08-18T00:02:00Z" },
      { id: "1", taskId: "install", agentId: "agent", kind: "application.apply", revision: 1, event: "queued", message: "install vastora-official/komari-agent", createdAt: "2026-08-18T00:01:00Z" }
    ];
    const container = render(<HomeView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    expect(container.textContent?.match(/应用变更/g)).toHaveLength(1);
    expect(container.textContent).toContain("成功");
  });

  it("creates lowercase DNS-safe default service hostnames", () => {
    const data = dashboard();
    data.sites[0].domainSuffix = "vastora.example.com";
    const service = { id: "manager", applicationId: "running", siteId: "site", name: "Manager 页面", protocol: "http" as const, containerPort: 8317, hostPort: 8317, endpoint: "192.168.1.2:8317", source: "catalog" as const, management: true, status: "running", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" };
    data.services = [service];
    expect(defaultPublicationHostname(data, service)).toBe("manager-komari-agent.home.vastora.example.com");
    data.services.push({ ...service, id: "subscription", name: "订阅服务" });
    expect(defaultPublicationHostname(data, service)).toBe("manager-komari-agent.home.vastora.example.com");
    expect(defaultRealityHostname(data, data.applications[0])).toBe("reality.home-server.home.vastora.example.com");
  });

  it("keeps Cloudflare zones separate from the Vastora service namespace", () => {
    expect(vastoraDomainDefaults("Kuddyx.COM.")).toEqual({
      zone: "kuddyx.com",
      namespace: "vastora.kuddyx.com",
      centerURL: "https://center.vastora.kuddyx.com",
      headscaleURL: "https://headscale.vastora.kuddyx.com"
    });
  });

  it("shows LAN, Headscale, and public networking as simultaneous capabilities", () => {
    const data = dashboard();
    data.agents.push({ ...data.agents[0], id: "retired", name: "retired-node", status: "disabled", connected: false });
    const container = render(<NetworkView data={data} language="zh-CN" mutate={async () => undefined} />);
    expect(container.textContent).toContain("局域网");
    expect(container.textContent).toContain("安全私网");
    expect(container.textContent).toContain("公网地址");
    expect(container.textContent).toContain("外部服务");
    expect(container.textContent).toContain("Cloudflare");
    expect(container.textContent).toContain("同时具备局域网、安全私网和公网能力");
    expect(container.textContent).not.toContain("retired-node");
  });

  it("recognizes a reported Headscale address before the network profile is confirmed", () => {
    const data = dashboard();
    data.agents[0].networkProfile = undefined;
    data.agents[0].networkCandidates = [{ address: "100.64.0.1", interface: "tailscale0", kind: "headscale", observedAt: "2026-08-18T00:00:00Z" }];
    const container = render(<NetworkView data={data} language="zh-CN" mutate={async () => undefined} />);
    expect(container.textContent).toContain("私网已连接，待确认");
    expect(container.textContent).toContain("确认推荐配置");
    expect([...container.querySelectorAll("button")].some((button) => button.textContent?.includes("加入安全私网"))).toBe(false);
  });

  it("shows only successful applications and marks host-privileged packages", () => {
    const container = render(<AppsView data={dashboard()} language="zh-CN" mutate={async () => undefined} />);
    expect(container.textContent).toContain("Komari 探针");
    expect(container.textContent).toContain("高权限");
    expect(container.textContent).not.toContain("Failed");
    expect(container.textContent).toContain("先把应用安装为私有服务");
    const installed = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("已安装"));
    const store = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("应用商店"));
    expect(installed?.querySelector('[data-slot="app-section-count"]')?.getAttribute("data-active")).toBe("true");
    act(() => store?.click());
    expect(store?.getAttribute("aria-pressed")).toBe("true");
    expect(store?.querySelector('[data-slot="app-section-count"]')?.getAttribute("data-active")).toBe("true");
    expect(store?.querySelector('[data-slot="app-section-count"]')?.textContent).toBe("1");
    expect(container.textContent).toContain("所有可用节点都已安装或正在安装此应用");
  });

  it("keeps an installed app manageable after a failed change", () => {
    const data = dashboard();
    data.apps[0].app.config = [{ key: "endpoint", label: { en: "Endpoint", "zh-CN": "地址" }, description: { en: "Service endpoint", "zh-CN": "服务地址" }, type: "string", required: true, secret: false }];
    data.applications = [{ ...data.applications[1], appKey: "vastora-official/komari-agent", name: "Komari Agent", installedVersion: "1.2.60", availableVersion: "1.2.60", updateAvailable: false }];
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    expect(container.textContent).toContain("最近一次操作失败，应用仍保留");
    expect(container.textContent).toContain("修改配置");
    expect(container.textContent).toContain("卸载");
    expect(container.textContent).toContain("版本已是最新");
  });

  it("offers upgrade only when the catalog contains a newer version", () => {
    const data = dashboard();
    data.applications[0] = { ...data.applications[0], installedVersion: "1.2.59", availableVersion: "1.2.60", updateAvailable: true };
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    expect(container.textContent).toContain("升级到 v1.2.60");
    expect(container.textContent).not.toContain("版本已是最新");
  });

  it("keeps uninstall available when an installed app leaves the catalog", () => {
    const data = dashboard();
    data.apps = [];
    data.applications = [data.applications[0]];
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    expect(container.textContent).toContain("Komari Agent");
    expect(container.textContent).toContain("卸载");
    expect(container.textContent).not.toContain("升级到");
  });

  it("confirms that a command was copied", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    const container = render(<CopyButton label="复制命令" language="zh-CN" value="vastora agent install" />);
    await act(async () => { container.querySelector("button")?.click(); await Promise.resolve(); });
    expect(writeText).toHaveBeenCalledWith("vastora agent install");
    expect(container.textContent).toContain("已复制");
  });

  it("offers an automatic shared 443 gateway for raw TLS services", () => {
	const data = dashboard();
	data.agents[0].networkCandidates = [{ address: "203.0.113.10", interface: "eth0", kind: "public", observedAt: "2026-08-18T00:00:00Z" }];
	data.agents[0].networkProfile = { serviceAddress: "203.0.113.10", publicAddress: "203.0.113.10", enabledKinds: ["public"], directPublic: true };
	data.services = [{ id: "vless", applicationId: "running", siteId: "site", name: "VLESS", protocol: "tcp", containerPort: 2443, hostPort: 2443, endpoint: "203.0.113.10:2443", source: "observed", appProtocol: "vless/tcp", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
	const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
	const add = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加入口"));
	act(() => add?.click());
	act(() => document.querySelector<HTMLButtonElement>("#publication-kind")?.click());
	expect(document.body.textContent).toContain("共享 443");
	expect(document.body.textContent).toContain("自动启用 HAProxy");
  });

	it("shows a failed install with its reason and a retry action", () => {
    const data = dashboard();
    data.deployments = [{ id: "failed-install", agentId: "agent", appKey: "vastora-official/komari-agent", appVersion: "1.2.60", state: "failed", operation: "install", deleteData: false, error: "container could not start", applicationId: "failed", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    expect(container.textContent).toContain("最近操作");
    expect(container.textContent).toContain("操作未完成，请检查填写内容后重试");
    const technical = [...container.querySelectorAll("details")].find((details) => details.textContent?.includes("container could not start"));
    expect(technical?.open).toBe(false);
    expect(container.textContent).toContain("重试");
		expect(container.textContent).toContain("无需手动刷新");
	});

	it("requeues a quarantined deployment with the same task", async () => {
		const data = dashboard();
		data.deployments = [{ id: "deploy-recovery", agentId: "agent", appKey: "vastora-official/komari-agent", appVersion: "1.2.60", state: "failed", reconciliationRequired: true, operation: "upgrade", deleteData: false, error: "container outcome is unknown", applicationId: "running", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
		const retry = vi.spyOn(api, "retryTaskReconciliation").mockResolvedValue({ taskId: "deploy-recovery", kind: "application.apply", queued: true });
		const container = render(<AppsView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} />);
		expect(container.textContent).toContain("需恢复");
		expect(container.textContent).toContain("不会重复安装");
		await act(async () => {
			[...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续恢复"))?.click();
			await Promise.resolve();
		});
		expect(retry).toHaveBeenCalledWith("deploy-recovery");
	});

	it("locks service changes during deployment recovery but still allows stopping an existing access point", async () => {
		const data = dashboard();
		data.apps[0].app.config = [{ key: "endpoint", label: { en: "Endpoint", "zh-CN": "地址" }, description: { en: "Service endpoint", "zh-CN": "服务地址" }, type: "string", required: true, secret: false }];
		data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, status: "configured" }];
		data.deployments = [{ id: "deploy-recovery", agentId: "agent", appKey: "vastora-official/komari-agent", appVersion: "1.2.60", state: "failed", reconciliationRequired: true, operation: "configure", deleteData: false, error: "container outcome is unknown", applicationId: "running", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
		data.services = [{ id: "manager", applicationId: "running", siteId: "site", name: "manager", protocol: "http", containerPort: 8317, hostPort: 8317, endpoint: "192.168.1.2:8317", source: "catalog", management: true, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
		data.publications = [{ id: "private-panel", serviceId: "manager", kind: "headscale_gateway", gatewayNodeId: "agent", hostname: "panel.home.example", dnsProvider: "headscale", tlsEnabled: false, desiredRevision: 2, appliedRevision: 1, status: "degraded", accessUrl: "http://panel.home.example/", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
		const stop = vi.spyOn(api, "stopPublication").mockResolvedValue({ stopped: true });
		const verify = vi.spyOn(api, "verifyPublication");
		const updateTLS = vi.spyOn(api, "updatePublicationTLS");
		const container = render(<AppsView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} />);

		expect(container.textContent).toContain("已有入口仍可停止");
		expect([...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加入口"))?.disabled).toBe(true);
		expect([...container.querySelectorAll("button")].find((button) => button.textContent?.includes("检查"))?.disabled).toBe(true);
		expect([...container.querySelectorAll("button")].find((button) => button.textContent?.includes("修改配置"))?.disabled).toBe(true);
		const tlsSwitch = container.querySelector<HTMLElement>('[role="switch"]');
		expect(tlsSwitch?.getAttribute("aria-disabled")).toBe("true");

		const stopButton = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("停止"));
		expect(stopButton?.disabled).toBe(false);
		await act(async () => { stopButton?.click(); await Promise.resolve(); });
		expect(stop).toHaveBeenCalledWith("private-panel");
		expect(verify).not.toHaveBeenCalled();
		expect(updateTLS).not.toHaveBeenCalled();
	});

  it("guides a new administrator to add the first node", () => {
    const data = dashboard();
    data.agents = [];
    const container = render(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    expect(container.textContent).toContain("添加第一台节点");
    expect(container.textContent).toContain("复制一条命令");
    expect(container.textContent).toContain("当前 Center 主机");
    expect(container.textContent).toContain("添加节点");
  });

  it("groups nodes by location and shows the location code on the page", () => {
    const data = dashboard();
    data.sites.push({ ...data.sites[0], id: "site-sg", name: "Singapore", code: "singapore", gatewayNodes: [] });
    data.agents.push({ ...data.agents[0], id: "agent-sg", name: "sg-edge", siteId: "site-sg" });
    const container = render(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    expect(container.textContent).toContain("Home");
    expect(container.textContent).toContain("home");
    expect(container.textContent).toContain("Singapore");
    expect(container.textContent).toContain("singapore");
    expect(container.textContent).toContain("位置：Home");
    expect(container.textContent).toContain("位置：Singapore");
  });

  it("shows the native architecture of each node", () => {
    const data = dashboard();
    data.agents.push({ ...data.agents[0], id: "arm-node", name: "arm-edge", architecture: "arm64" });
    const container = render(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    expect(container.textContent).toContain("x64");
    expect(container.textContent).toContain("ARM64");
  });

  it("offers one-click REALITY with a hierarchical connection hostname", async () => {
    const data = realityDashboard();
    vi.spyOn(api, "latestApplicationCommand").mockRejectedValue(new APIError("not found", 404, "not_found"));
    vi.spyOn(api, "regions").mockResolvedValue({ regions: [{ code: "US", nameZh: "美国", prefix: "🇺🇸 美国" }] });
    vi.spyOn(api, "agentRegionSuggestion").mockResolvedValue({ agentId: "agent", publicAddress: "203.0.113.10", regionCode: "US", prefix: "🇺🇸 美国", source: "country.is" });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("创建 VLESS"))?.click();
      await Promise.resolve();
    });
    expect(document.body.textContent).toContain("自动识别地区并生成标准节点名");
    expect(document.querySelector<HTMLInputElement>("#reality-name")?.value).toBe("home-server");
    expect(document.body.textContent).toContain("🇺🇸 美国home-server");
    expect(document.querySelector<HTMLInputElement>("#reality-client-name")?.value).toBe("我的设备");
    expect(document.body.textContent).toContain("当前节点套餐");
    expect(document.body.textContent).toContain("订阅总额度");
    expect(document.querySelector<HTMLInputElement>("#reality-inbound-quota")).not.toBeNull();
    expect(document.querySelector<HTMLInputElement>("#reality-subscription-quota")).not.toBeNull();
    expect(document.querySelector<HTMLInputElement>("#reality-hostname")?.value).toBe("reality.home-server.home.vastora.example.com");
    expect(document.querySelector<HTMLButtonElement>("#reality-gateway")?.textContent).toContain("home-server");
    expect(document.body.textContent).toContain("高级：自定义伪装目标");
  });

  it("offers a separate one-click public 3x-ui subscription", async () => {
    const data = realityDashboard();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, status: "configured" }];
    data.services = [{ id: "subscription", applicationId: "three-x-ui", siteId: "site", name: "subscription", protocol: "http", containerPort: 2096, hostPort: 2096, endpoint: "10.0.0.10:2096", source: "catalog", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    vi.spyOn(api, "latestApplicationCommand").mockRejectedValue(new APIError("not found", 404, "not_found"));
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("开启订阅"))?.click();
      await Promise.resolve();
    });
    expect(document.body.textContent).toContain("发布独立订阅服务");
    expect(document.body.textContent).toContain("管理面板仍只在私网开放");
    expect(document.querySelector<HTMLInputElement>("#subscription-hostname")?.value).toBe("subscription-3x-ui.home.vastora.example.com");
    expect(document.querySelector<HTMLButtonElement>("#subscription-kind")?.textContent).toContain("Cloudflare Tunnel");
  });

  it("turns a completed subscription command into an actionable access check", async () => {
    const data = realityDashboard();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, status: "configured" }];
    data.services = [{ id: "subscription", applicationId: "three-x-ui", siteId: "site", name: "subscription", protocol: "http", containerPort: 2096, hostPort: 2096, endpoint: "10.0.0.10:2096", source: "catalog", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    const publication = { id: "subscription-publication", serviceId: "subscription", kind: "cloudflare_tunnel" as const, gatewayNodeId: "agent", hostname: "subscription.example.test", dnsProvider: "cloudflare" as const, tlsEnabled: true, desiredRevision: 2, appliedRevision: 2, status: "applying" as const, createdAt: "2026-08-24T00:00:00Z", updatedAt: "2026-08-24T00:00:01Z" };
    data.publications = [publication];
    vi.spyOn(api, "latestApplicationCommand").mockResolvedValue({ id: "subscription-command", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.subscription.configure", state: "succeeded", hostname: publication.hostname, dnsProvider: "cloudflare", publicationId: publication.id, resultAvailable: false, createdAt: "2026-08-24T00:00:00Z", updatedAt: "2026-08-24T00:00:01Z" });
    const verify = vi.spyOn(api, "verifyPublication").mockResolvedValue({ ...publication, status: "ready", accessUrl: `https://${publication.hostname}/` });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} />);

    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("公网订阅"))?.click();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(document.body.textContent).toContain("3x-ui 已配置，入口尚未确认");
    expect(document.body.textContent).toContain("不需要继续等待");
    expect(document.body.textContent).not.toContain("正在自动配置");
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("立即检查入口"))?.click();
      await Promise.resolve();
    });
    expect(verify).toHaveBeenCalledWith("subscription-publication");
  });

  it("surfaces degraded subscription details and a configuration retry", async () => {
    const data = realityDashboard();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, status: "configured" }];
    data.services = [{ id: "subscription", applicationId: "three-x-ui", siteId: "site", name: "subscription", protocol: "http", containerPort: 2096, hostPort: 2096, endpoint: "10.0.0.10:2096", source: "catalog", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    data.publications = [{ id: "subscription-publication", serviceId: "subscription", kind: "cloudflare_tunnel", gatewayNodeId: "agent", hostname: "subscription.example.test", dnsProvider: "cloudflare", tlsEnabled: true, desiredRevision: 2, appliedRevision: 2, status: "degraded", lastError: "TLS health check failed", createdAt: "2026-08-24T00:00:00Z", updatedAt: "2026-08-24T00:00:01Z" }];
    vi.spyOn(api, "latestApplicationCommand").mockResolvedValue({ id: "subscription-command", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.subscription.configure", state: "succeeded", hostname: "subscription.example.test", dnsProvider: "cloudflare", publicationId: "subscription-publication", resultAvailable: false, createdAt: "2026-08-24T00:00:00Z", updatedAt: "2026-08-24T00:00:01Z" });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);

    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("公网订阅"))?.click();
      await Promise.resolve();
    });

    expect(document.body.textContent).toContain("订阅入口需要处理");
    expect(document.body.textContent).toContain("TLS health check failed");
    expect([...document.querySelectorAll("button")].some((button) => button.textContent?.includes("重试配置"))).toBe(true);
  });

  it("automatically installs later 3x-ui instances as VLESS-only nodes", async () => {
    const data = realityDashboard();
    data.agents.push({ ...data.agents[0], id: "worker", name: "edge-worker", networkProfile: { serviceAddress: "100.64.0.20", headscaleAddress: "100.64.0.20", enabledKinds: ["headscale"], directPublic: false } });
    const create = vi.spyOn(api, "createDeployment").mockResolvedValue({ id: "worker-deployment", agentId: "worker", appKey: "vastora-official/3x-ui", appVersion: "3.6.0", state: "pending", operation: "install", deleteData: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" });
    const mutate = async (operation: () => Promise<unknown>) => { await operation(); };
    const container = render(<AppsView data={data} language="zh-CN" mutate={mutate} />);
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("应用商店"))?.click());
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.trim() === "安装")?.click());
    expect(document.body.textContent).toContain("将作为 VLESS 节点");
    expect(document.body.textContent).toContain("不会再创建独立面板或订阅地址");
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("开始安装"))?.click();
      await Promise.resolve();
    });
    expect(create).toHaveBeenCalledWith("worker", "vastora-official/3x-ui", {}, "install", false, "worker");
  });

	it("shows one Site controller and keeps worker controls focused on VLESS", () => {
    const data = realityDashboard();
    data.agents.push({ ...data.agents[0], id: "worker", name: "edge-worker" });
    data.applications.push({ ...data.applications[0], id: "three-x-ui-worker", nodeId: "worker", role: "worker", controllerApplicationId: "three-x-ui", nodeSyncStatus: "ready" });
    data.services.push(
      { id: "controller-inbound", applicationId: "three-x-ui", siteId: "site", name: "inbound-1", protocol: "tcp", containerPort: 30443, hostPort: 30443, endpoint: "10.0.0.10:30443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" },
      { id: "worker-inbound", applicationId: "three-x-ui-worker", siteId: "site", name: "inbound-2", protocol: "tcp", containerPort: 31443, hostPort: 31443, endpoint: "10.0.0.20:31443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" }
    );
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    expect(container.textContent).toContain("订阅主机");
    expect(container.textContent).toContain("VLESS 节点");
    expect(container.textContent).toContain("当前订阅包含 2 个 VLESS 节点");
    expect(container.textContent).not.toContain("统一管理 1 个 VLESS 节点");
    expect([...container.querySelectorAll("button")].filter((button) => button.textContent?.includes("管理客户端"))).toHaveLength(1);
    expect([...container.querySelectorAll("button")].filter((button) => button.textContent?.includes("创建 VLESS"))).toHaveLength(2);
		expect(container.textContent).toContain("客户端和订阅由当前位置的订阅主机统一管理");
	});

	it("renames an existing REALITY node from Center", async () => {
		vi.useFakeTimers();
		const data = realityDashboard();
		data.services = [{ id: "reality-service", applicationId: "three-x-ui", siteId: "site", name: "inbound-9", displayName: "🇺🇸 美国Old name", regionCode: "US", protocol: "tcp", containerPort: 30443, hostPort: 30443, endpoint: "10.0.0.10:30443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" }];
		const pending: ApplicationCommand = { id: "rename-command", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.reality.rename", state: "pending", hostname: "", dnsProvider: "manual", action: "rename", regionCode: "US", displayName: "🇺🇸 美国Oracle", inboundId: 9, resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" };
		vi.spyOn(api, "regions").mockResolvedValue({ regions: [{ code: "US", nameZh: "美国", prefix: "🇺🇸 美国" }] });
		const rename = vi.spyOn(api, "renameRealityCommand").mockResolvedValue(pending);
		mockCommandEvent({ ...pending, state: "succeeded", updatedAt: "2026-08-23T00:00:01Z" });
		const mutate = vi.fn(async (operation: () => Promise<unknown>) => { await operation(); });
		const container = render(<AppsView data={data} language="zh-CN" mutate={mutate} />);
		expect(container.textContent).toContain("🇺🇸 美国Old name");
		act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("重命名"))?.click());
		const input = document.querySelector<HTMLInputElement>("#reality-rename-name")!;
		act(() => {
			Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(input, "Oracle");
			input.dispatchEvent(new Event("input", { bubbles: true }));
		});
		await act(async () => {
			[...document.querySelectorAll("button")].find((button) => button.textContent?.includes("保存名称"))?.click();
			await Promise.resolve();
		});
		expect(rename).toHaveBeenCalledWith("reality-service", "US", "Oracle");
		await act(async () => {
			vi.advanceTimersByTime(1300);
			await Promise.resolve();
			await Promise.resolve();
		});
		expect(document.body.textContent).toContain("现在显示为“🇺🇸 美国Oracle”");
		expect(mutate).toHaveBeenCalled();
	});

	it("offers a guided manual subscription-host migration", () => {
    const data = realityDashboard();
    data.agents.push({ ...data.agents[0], id: "worker", name: "edge-worker", connected: true });
    data.applications.push({ ...data.applications[0], id: "three-x-ui-worker", nodeId: "worker", role: "worker", controllerApplicationId: "three-x-ui", nodeSyncStatus: "ready" });
    data.threeXUIControllerMigrations.push({ id: "migration", siteId: "site", sourceApplicationId: "three-x-ui", targetApplicationId: "three-x-ui-worker", backupRevision: 2, state: "backing_up", step: "backup", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:01Z" });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("迁移订阅主机"))?.click());
    expect(document.body.textContent).toContain("正在安全迁移");
    expect(document.body.textContent).toContain("保存最新配置");
    expect(document.body.textContent).toContain("恢复到新主机");
    expect(document.body.textContent).toContain("清理旧主机并同步节点");
  });

  it("manages 3x-ui clients and reveals links without opening the panel", async () => {
    const data = realityDashboard();
    const baseCommand: ApplicationCommand = { id: "client-command-list", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.clients.manage", state: "succeeded", hostname: "", dnsProvider: "manual", action: "list", clients: [{ email: "MacBook", enabled: true, totalBytes: 10 * 1024 ** 3, usedBytes: 1024, expiryTime: 0, resetDays: 0, limitIp: 2, inboundIds: [9], hasSubscription: true }], clientsObserved: true, inbounds: [{ id: 9, serviceId: "reality-service", name: "inbound-9", nodeName: "edge-worker", connectHostname: "reality.example.test", totalBytes: 200 * 1024 ** 3, usedBytes: 12 * 1024 ** 3, resetDays: 30, nextResetAt: "2026-09-22T00:00:00Z" }, { id: 10, serviceId: "reality-service-2", name: "inbound-10", nodeName: "oracle-worker", connectHostname: "reality.oracle.example.test", totalBytes: 0, usedBytes: 0, resetDays: 0 }], inboundsObserved: true, subscriptionAvailable: true, resultAvailable: false, createdAt: "2026-08-22T00:00:00Z", updatedAt: "2026-08-22T00:00:01Z" };
    const create = vi.spyOn(api, "createThreeXUIClientCommand").mockImplementation(async (input) => input.action.startsWith("reveal_") ? { ...baseCommand, id: `client-command-${input.action}`, action: input.action, resultAvailable: true } : baseCommand);
    const reveal = vi.spyOn(api, "revealApplicationCommand").mockImplementation(async (id) => ({ shareUri: id.includes("subscription") ? "https://subscription.example.test/sub/client-id" : "vless://one-time-client-link" }));
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("管理客户端"))?.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(create).toHaveBeenCalledWith({ applicationId: "three-x-ui", action: "list" });
    expect(document.body.textContent).toContain("MacBook");
    expect(document.body.textContent).toContain("已接入 1 个节点：edge-worker");
    expect(document.body.textContent).toContain("全节点已用");
    expect(document.body.textContent).toContain("日常管理");
    act(() => [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("编辑") || button.querySelector(".sr-only")?.textContent === "编辑")?.click());
    expect(document.body.textContent).toContain("此客户端的订阅总额度（所有节点合计）");
    expect(document.querySelector<HTMLInputElement>("#three-x-ui-client-quota")?.value).toBe("10");
    expect(document.querySelector<HTMLInputElement>("#three-x-ui-client-reset-days")?.value).toBe("0");
    expect([...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("保存修改"))?.disabled).toBe(true);
    const nodeChoices = [...document.querySelectorAll<HTMLElement>('[role="checkbox"]')];
    expect(nodeChoices).toHaveLength(2);
    expect(nodeChoices[0].getAttribute("aria-checked")).toBe("true");
    expect(nodeChoices[1].getAttribute("aria-checked")).toBe("false");
    act(() => nodeChoices[1].click());
    expect([...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("保存修改"))?.disabled).toBe(false);
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("保存修改"))?.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(create).toHaveBeenCalledWith({ applicationId: "three-x-ui", action: "update", email: "MacBook", newEmail: "MacBook", inboundIds: [9, 10], totalBytes: 10 * 1024 ** 3, resetDays: 0, expiryTime: 0, limitIp: 2 });
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("复制 VLESS"))?.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(reveal).toHaveBeenCalledWith("client-command-reveal_link");
    expect(writeText).toHaveBeenCalledWith("vless://one-time-client-link");
    expect(document.body.textContent).toContain("vless://one-time-client-link");
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("复制订阅"))?.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(create).toHaveBeenCalledWith({ applicationId: "three-x-ui", action: "reveal_subscription", email: "MacBook", inboundId: undefined });
    expect(writeText).toHaveBeenCalledWith("https://subscription.example.test/sub/client-id");
    expect(document.body.textContent).toContain("订阅地址");
    expect([...document.querySelectorAll("button")].filter((button) => button.textContent?.trim() === "复制订阅")).toHaveLength(1);
    expect([...document.querySelectorAll("button")].some((button) => button.textContent?.trim() === "OpenClash")).toBe(false);
    const resetCallsBeforeConfirmation = create.mock.calls.filter(([input]) => input.action === "reset_traffic").length;
    act(() => document.querySelector<HTMLButtonElement>('button[title="重置流量"]')?.click());
    expect(document.body.textContent).toContain("所有 VLESS 节点上的合计用量清零");
    expect(create.mock.calls.filter(([input]) => input.action === "reset_traffic")).toHaveLength(resetCallsBeforeConfirmation);
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("确认重置"))?.click();
      await Promise.resolve();
    });
    expect(create).toHaveBeenCalledWith({ applicationId: "three-x-ui", action: "reset_traffic", email: "MacBook" });
  });

  it("shows cached clients immediately while refreshing and formats expiry in the Site timezone", async () => {
    class IdleEventSource {
      onopen: (() => void) | null = null;
      onmessage: ((event: MessageEvent<string>) => void) | null = null;
      onerror: (() => void) | null = null;
      close() {}
    }
    vi.stubGlobal("EventSource", IdleEventSource);
    const data = realityDashboard();
    const cached: ApplicationCommand = { id: "cached-clients", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.clients.manage", state: "succeeded", hostname: "", dnsProvider: "manual", action: "list", clients: [{ email: "MacBook", enabled: true, totalBytes: 0, usedBytes: 1024, expiryTime: Date.parse("2026-08-23T16:30:00Z"), resetDays: 0, limitIp: 0, inboundIds: [9], hasSubscription: true }], clientsObserved: true, inbounds: [{ id: 9, serviceId: "reality-service", name: "inbound-9", nodeName: "edge-worker", connectHostname: "reality.example.test" }], inboundsObserved: true, subscriptionAvailable: true, resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:01Z" };
    vi.spyOn(api, "latestApplicationCommand").mockResolvedValue(cached);
    vi.spyOn(api, "createThreeXUIClientCommand").mockResolvedValue({ ...cached, id: "refresh-clients", state: "pending", clients: undefined, clientsObserved: false, inbounds: undefined, inboundsObserved: false });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);

    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("管理客户端"))?.click();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(document.body.textContent).toContain("MacBook");
    expect(document.body.textContent).toContain("正在后台刷新");
    expect(document.body.textContent).toContain("2026年8月24日");
  });

  it("offers browser-trusted HTTPS only when Cloudflare is connected", () => {
    const data = dashboard();
    data.sites[0].domainSuffix = "vastora.example.com";
    data.services = [{ id: "manager", applicationId: "running", siteId: "site", name: "manager", protocol: "http", containerPort: 8317, hostPort: 8317, endpoint: "192.168.1.2:8317", source: "catalog", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    let container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加入口"))?.click());
    expect(document.body.textContent).toContain("连接 Cloudflare 后可以开启");
    expect(document.querySelector<HTMLElement>('[role="switch"][aria-label="使用 HTTPS"]')?.getAttribute("aria-disabled")).toBe("true");

    act(() => root?.unmount());
    root = undefined;
    document.body.replaceChildren();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, status: "configured" }];
    container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加入口"))?.click());
    expect(document.body.textContent).toContain("使用 Cloudflare DNS 验证申请可信证书");
    const tlsSwitch = document.querySelector<HTMLElement>('[role="switch"][aria-label="使用 HTTPS"]');
    expect(tlsSwitch?.getAttribute("aria-disabled")).not.toBe("true");
    expect(tlsSwitch?.getAttribute("aria-checked")).toBe("true");
  });

  it("upgrades an existing private HTTP access point from its HTTPS switch", async () => {
    const data = dashboard();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, status: "configured" }];
    data.services = [{ id: "manager", applicationId: "running", siteId: "site", name: "manager", protocol: "http", containerPort: 8317, hostPort: 8317, endpoint: "192.168.1.2:8317", source: "catalog", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    data.publications = [{ id: "private-panel", serviceId: "manager", kind: "headscale_gateway", gatewayNodeId: "agent", hostname: "panel.home.example", dnsProvider: "headscale", tlsEnabled: false, desiredRevision: 1, appliedRevision: 1, status: "ready", accessUrl: "http://panel.home.example/", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    const update = vi.spyOn(api, "updatePublicationTLS").mockResolvedValue({ ...data.publications[0], tlsEnabled: true });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} />);

    expect(container.textContent).toContain("安全私网 · HTTP");
    const tlsSwitch = container.querySelector<HTMLElement>('[role="switch"][aria-label="开启 HTTPS"]');
    expect(tlsSwitch?.getAttribute("aria-label")).toBe("开启 HTTPS");
    await act(async () => { tlsSwitch?.click(); await Promise.resolve(); });
    expect(update).toHaveBeenCalledWith("private-panel", true);
  });

	it("reveals a REALITY client link only after explicit confirmation", async () => {
    const data = realityDashboard();
	    vi.spyOn(api, "latestApplicationCommand").mockResolvedValue({ id: "application-command-1", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.reality.create", state: "succeeded", hostname: "reality.home-server.home.vastora.example.com", dnsProvider: "manual", target: "www.example.com:443", sniHostname: "www.example.com", clientCreated: true, resultAvailable: true, createdAt: "2026-08-20T00:00:00Z", updatedAt: "2026-08-20T00:00:01Z" });
    const reveal = vi.spyOn(api, "revealApplicationCommand").mockResolvedValue({ shareUri: "vless://one-time-client-link" });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("创建 VLESS"))?.click();
      await Promise.resolve();
    });
    expect(reveal).not.toHaveBeenCalled();
    const revealButton = [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("显示一次性链接"));
    await act(async () => {
      revealButton?.click();
      await Promise.resolve();
    });
    expect(reveal).toHaveBeenCalledWith("application-command-1");
		expect(document.body.textContent).toContain("vless://one-time-client-link");
	});

	it("keeps the one-time REALITY link available when only public access fails", async () => {
		const data = realityDashboard();
		vi.spyOn(api, "latestApplicationCommand").mockResolvedValue({ id: "application-command-degraded", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.reality.create", state: "succeeded", hostname: "reality.home-server.home.vastora.example.com", dnsProvider: "cloudflare", displayName: "🇺🇸 美国Edge", target: "www.example.com:443", sniHostname: "www.example.com", clientCreated: true, error: "center: create REALITY access entry: SNI conflict", resultAvailable: true, createdAt: "2026-08-20T00:00:00Z", updatedAt: "2026-08-20T00:00:01Z" });
		const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
		await act(async () => {
			[...container.querySelectorAll("button")].find((button) => button.textContent?.includes("创建 VLESS"))?.click();
			await Promise.resolve();
		});
		expect(document.body.textContent).toContain("REALITY 已创建，公网入口待处理");
		expect(document.body.textContent).toContain("客户端凭据已安全保留");
		expect([...document.querySelectorAll("button")].some((button) => button.textContent?.includes("显示一次性链接"))).toBe(true);
	});

	it("returns to the REALITY form after a previous creation failed", async () => {
		const data = realityDashboard();
		vi.spyOn(api, "latestApplicationCommand").mockResolvedValue({ id: "failed-reality", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.reality.create", state: "failed", hostname: "reality.failed.example.test", dnsProvider: "manual", action: "create", error: "node plan rejected", resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:01Z" });
		const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
		await act(async () => {
			[...container.querySelectorAll("button")].find((button) => button.textContent?.includes("创建 VLESS"))?.click();
			await Promise.resolve();
		});
		expect(document.querySelector("#reality-name")).not.toBeNull();
		expect(document.body.textContent).not.toContain("node plan rejected");
	});

	it("continues a quarantined REALITY task without creating a duplicate command", async () => {
		const data = realityDashboard();
		const quarantined: ApplicationCommand = { id: "reality-recovery", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.reality.create", state: "failed", reconciliationRequired: true, hostname: "reality.home-server.home.vastora.example.com", dnsProvider: "manual", action: "create", error: "remote outcome is unknown", resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:01Z" };
		const pending = { ...quarantined, state: "pending" as const, reconciliationRequired: false };
		vi.spyOn(api, "latestApplicationCommand").mockResolvedValue(quarantined);
		const retry = vi.spyOn(api, "retryTaskReconciliation").mockResolvedValue({ taskId: "reality-recovery", kind: "application.command", queued: true });
		vi.spyOn(api, "applicationCommand").mockResolvedValue(pending);
		const create = vi.spyOn(api, "createRealityCommand");
		mockCommandEvent({ ...pending, state: "succeeded" });
		const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
		await act(async () => {
			[...container.querySelectorAll("button")].find((button) => button.textContent?.includes("创建 VLESS"))?.click();
			await Promise.resolve();
		});
		expect(document.body.textContent).toContain("需要继续恢复");
		await act(async () => {
			[...document.querySelectorAll("button")].find((button) => button.textContent?.includes("继续恢复"))?.click();
			await Promise.resolve();
			await Promise.resolve();
		});
		expect(retry).toHaveBeenCalledWith("reality-recovery");
		expect(create).not.toHaveBeenCalled();
	});

	it("keeps a REALITY task locally pending when recovery was queued but the immediate refresh fails", async () => {
		const data = realityDashboard();
		const quarantined: ApplicationCommand = { id: "reality-recovery", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.reality.create", state: "failed", reconciliationRequired: true, hostname: "reality.home-server.home.vastora.example.com", dnsProvider: "manual", action: "create", error: "remote outcome is unknown", resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:01Z" };
		vi.spyOn(api, "latestApplicationCommand").mockResolvedValue(quarantined);
		const retry = vi.spyOn(api, "retryTaskReconciliation").mockResolvedValue({ taskId: "reality-recovery", kind: "application.command", queued: true });
		vi.spyOn(api, "applicationCommand").mockRejectedValue(new APIError("temporary refresh failure", 503, "unavailable"));
		const create = vi.spyOn(api, "createRealityCommand");
		const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
		await act(async () => {
			[...container.querySelectorAll("button")].find((button) => button.textContent?.includes("创建 VLESS"))?.click();
			await Promise.resolve();
		});

		expect(document.body.textContent).toContain("需要继续恢复");
		await act(async () => {
			[...document.querySelectorAll("button")].find((button) => button.textContent?.includes("继续恢复"))?.click();
			await Promise.resolve();
			await Promise.resolve();
		});

		expect(retry).toHaveBeenCalledTimes(1);
		expect(document.body.textContent).toContain("正在自动配置");
		expect(document.body.textContent).toContain("等待 Agent 接收任务");
		expect(document.body.textContent).not.toContain("需要继续恢复");
		expect([...document.querySelectorAll("button")].some((button) => button.textContent?.includes("继续恢复"))).toBe(false);
		expect(create).not.toHaveBeenCalled();
	});

	it("specifies separate subscription-node and initial-client names when creating REALITY", async () => {
		const data = realityDashboard();
		vi.spyOn(api, "latestApplicationCommand").mockRejectedValue(new Error("not found"));
		vi.spyOn(api, "regions").mockResolvedValue({ regions: [{ code: "US", nameZh: "美国", prefix: "🇺🇸 美国" }] });
		vi.spyOn(api, "agentRegionSuggestion").mockResolvedValue({ agentId: "agent", publicAddress: "203.0.113.10", regionCode: "US", prefix: "🇺🇸 美国", source: "country.is" });
		const pending: ApplicationCommand = { id: "create-reality", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.reality.create", state: "pending", hostname: "reality.home-server.home.vastora.example.com", dnsProvider: "manual", action: "create", regionCode: "US", displayName: "🇺🇸 美国Oracle", resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" };
		const create = vi.spyOn(api, "createRealityCommand").mockResolvedValue(pending);
		mockCommandEvent(pending);
		const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
		await act(async () => {
			[...container.querySelectorAll("button")].find((button) => button.textContent?.includes("创建 VLESS"))?.click();
			await Promise.resolve();
		});
		const nodeName = document.querySelector<HTMLInputElement>("#reality-name")!;
		expect(nodeName.value).toBe("home-server");
		expect(document.querySelector<HTMLInputElement>("#reality-client-name")?.value).toBe("我的设备");
		act(() => {
			Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(nodeName, "Oracle");
			nodeName.dispatchEvent(new Event("input", { bubbles: true }));
		});
		await act(async () => {
			[...document.querySelectorAll("button")].find((button) => button.textContent?.includes("自动创建"))?.click();
			await Promise.resolve();
		});
			expect(create).toHaveBeenCalledWith({ applicationId: "three-x-ui", regionCode: "US", name: "Oracle", clientName: "我的设备", gatewayNodeId: "agent", hostname: "reality.home-server.home.vastora.example.com", dnsProvider: "manual", target: undefined, sniHostname: undefined, inboundTotalBytes: 0, inboundResetDays: 0, clientTotalBytes: 0, clientResetDays: 0, clientExpiryTime: 0 });
		});

  it("keeps subscriber quotas on the controller even when a worker creates the first VLESS node", async () => {
    const data = realityDashboard();
    data.sites[0].gatewayNodes.push("worker");
    data.agents.push({ ...data.agents[0], id: "worker", name: "oracle-worker", networkProfile: { ...data.agents[0].networkProfile!, serviceAddress: "10.0.0.20", publicAddress: "203.0.113.20" } });
    data.applications.push({ ...data.applications[0], id: "three-x-ui-worker", nodeId: "worker", role: "worker", controllerApplicationId: "three-x-ui", nodeSyncStatus: "ready" });
    vi.spyOn(api, "latestApplicationCommand").mockRejectedValue(new APIError("not found", 404, "not_found"));
    vi.spyOn(api, "regions").mockResolvedValue({ regions: [{ code: "US", nameZh: "美国", prefix: "🇺🇸 美国" }] });
    vi.spyOn(api, "agentRegionSuggestion").mockResolvedValue({ agentId: "worker", publicAddress: "203.0.113.20", regionCode: "US", prefix: "🇺🇸 美国", source: "country.is" });
    const create = vi.spyOn(api, "createRealityCommand").mockResolvedValue({ id: "worker-reality", applicationId: "three-x-ui-worker", gatewayNodeId: "worker", kind: "3xui.reality.create", state: "succeeded", hostname: "reality.oracle-worker.home.vastora.example.com", dnsProvider: "manual", action: "create", clientCreated: false, resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:01Z" });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    await act(async () => {
      [...container.querySelectorAll("button")].filter((button) => button.textContent?.includes("创建 VLESS"))[1]?.click();
      await Promise.resolve();
    });
    expect(document.body.textContent).toContain("当前节点套餐");
    expect(document.body.textContent).toContain("订阅额度在主订阅机管理");
    expect(document.body.textContent).toContain("如果还没有用户");
    expect(document.querySelector("#reality-client-name")).toBeNull();
    expect(document.querySelector("#reality-subscription-quota")).toBeNull();
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("自动创建"))?.click();
      await Promise.resolve();
    });
    expect(create).toHaveBeenCalledWith({ applicationId: "three-x-ui-worker", regionCode: "US", name: "oracle-worker", gatewayNodeId: "worker", hostname: "reality.oracle-worker.home.vastora.example.com", dnsProvider: "manual", target: undefined, sniHostname: undefined, inboundTotalBytes: 0, inboundResetDays: 0 });
    expect(document.body.textContent).not.toContain("客户端链接只显示一次");
  });

  it("still bootstraps the first controller client after a worker REALITY node exists", async () => {
    const data = realityDashboard();
    data.services = [{ id: "worker-reality", applicationId: "three-x-ui-worker", siteId: "site", name: "inbound-90", displayName: "🇺🇸 美国Worker", protocol: "tcp", containerPort: 30443, hostPort: 30443, endpoint: "10.0.0.20:30443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" }];
    vi.spyOn(api, "latestApplicationCommand").mockRejectedValue(new APIError("not found", 404, "not_found"));
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("创建 VLESS"))?.click();
      await Promise.resolve();
    });
    expect(document.querySelector<HTMLInputElement>("#reality-client-name")?.value).toBe("我的设备");
    expect(document.querySelector("#reality-subscription-quota")).not.toBeNull();
  });

  it("edits an independent VLESS node plan from its service card", async () => {
    const data = realityDashboard();
    const service = { id: "reality-service", applicationId: "three-x-ui", siteId: "site", name: "inbound-9", displayName: "🇺🇸 美国CloudLead", protocol: "tcp" as const, containerPort: 30443, hostPort: 30443, endpoint: "10.0.0.10:30443", source: "observed" as const, appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" };
    data.services = [service];
    const current: ApplicationCommand = { id: "traffic-list", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.clients.manage", state: "succeeded", hostname: "", dnsProvider: "manual", action: "list_inbounds", clients: [], clientsObserved: false, inbounds: [{ id: 9, serviceId: "reality-service", name: "inbound-9", displayName: "🇺🇸 美国CloudLead", totalBytes: 200 * 1024 ** 3, usedBytes: 12 * 1024 ** 3, resetDays: 30, nextResetAt: "2026-09-22T00:00:00Z", planStatus: "active" }], inboundsObserved: true, resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:01Z" };
    const updated = { ...current, id: "traffic-update", action: "update_inbound" as const, inbounds: [{ ...current.inbounds![0], totalBytes: 300 * 1024 ** 3, resetDays: 31 }] };
    const command = vi.spyOn(api, "createThreeXUIClientCommand").mockImplementation(async (input) => input.action === "update_inbound" ? updated : current);
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("节点套餐"))?.click();
      await Promise.resolve();
    });
    expect(command).toHaveBeenCalledWith({ applicationId: "three-x-ui", action: "list_inbounds" });
    expect(document.body.textContent).toContain("VLESS 节点套餐");
    expect(document.body.textContent).toContain("200.0 GB");
    act(() => [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("修改节点套餐"))?.click());
    const quota = document.querySelector<HTMLInputElement>("#inbound-plan-quota")!;
    const resetDays = document.querySelector<HTMLInputElement>("#inbound-plan-reset-days")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(quota, "300");
      quota.dispatchEvent(new Event("input", { bubbles: true }));
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(resetDays, "31");
      resetDays.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("取消"))?.click());
    act(() => [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("修改节点套餐"))?.click());
    const restoredQuota = document.querySelector<HTMLInputElement>("#inbound-plan-quota")!;
    const restoredResetDays = document.querySelector<HTMLInputElement>("#inbound-plan-reset-days")!;
    expect(restoredQuota.value).toBe("200");
    expect(restoredResetDays.value).toBe("30");
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(restoredQuota, "300");
      restoredQuota.dispatchEvent(new Event("input", { bubbles: true }));
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(restoredResetDays, "31");
      restoredResetDays.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("保存套餐"))?.click();
      await Promise.resolve();
    });
    expect(command).toHaveBeenCalledWith({ applicationId: "three-x-ui", action: "update_inbound", serviceId: "reality-service", inboundId: 9, inboundTotalBytes: 300 * 1024 ** 3, inboundResetDays: 31 });
    expect(document.body.textContent).toContain("节点套餐已保存");
  });

  it("keeps a cached node plan visible while the live refresh is pending", async () => {
    class IdleEventSource {
      onopen: (() => void) | null = null;
      onmessage: ((event: MessageEvent<string>) => void) | null = null;
      onerror: (() => void) | null = null;
      close() {}
    }
    vi.stubGlobal("EventSource", IdleEventSource);
    const data = realityDashboard();
    data.services = [{ id: "reality-service", applicationId: "three-x-ui", siteId: "site", name: "inbound-9", displayName: "🇺🇸 美国CloudLead", protocol: "tcp", containerPort: 30443, hostPort: 30443, endpoint: "10.0.0.10:30443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" }];
    const cached: ApplicationCommand = { id: "cached-traffic", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.clients.manage", state: "succeeded", hostname: "", dnsProvider: "manual", action: "list_inbounds", clients: [], clientsObserved: false, inbounds: [{ id: 9, serviceId: "reality-service", name: "inbound-9", displayName: "🇺🇸 美国CloudLead", totalBytes: 200 * 1024 ** 3, usedBytes: 12 * 1024 ** 3, resetDays: 30, nextResetAt: "2026-09-22T00:00:00Z" }], inboundsObserved: true, resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:01Z" };
    vi.spyOn(api, "latestApplicationCommand").mockResolvedValue(cached);
    vi.spyOn(api, "createThreeXUIClientCommand").mockResolvedValue({ ...cached, id: "refresh-traffic", state: "pending", inbounds: undefined, inboundsObserved: false });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);

    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("节点套餐"))?.click();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(document.body.textContent).toContain("200.0 GB");
    expect(document.body.textContent).toContain("正在后台刷新，当前套餐仍可查看");
  });

  it("keeps service origin details collapsed by default", () => {
    const data = realityDashboard();
    data.services = [{ id: "reality-service", applicationId: "three-x-ui", siteId: "site", name: "inbound-9", displayName: "🇺🇸 美国CloudLead", protocol: "tcp", containerPort: 30443, hostPort: 30443, endpoint: "10.0.0.10:30443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" }];
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    const details = [...container.querySelectorAll("details")].find((value) => value.querySelector("summary")?.textContent?.includes("技术信息"));
    expect(details?.open).toBe(false);
    expect(details?.textContent).toContain("tcp · 10.0.0.10:30443");
  });

  it("shows a recovery path when a node plan renewal fails", async () => {
    const data = realityDashboard();
    data.services = [{ id: "reality-service", applicationId: "three-x-ui", siteId: "site", name: "inbound-9", protocol: "tcp", containerPort: 30443, hostPort: 30443, endpoint: "10.0.0.10:30443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" }];
    vi.spyOn(api, "createThreeXUIClientCommand").mockResolvedValue({ id: "traffic-list", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.clients.manage", state: "succeeded", hostname: "", dnsProvider: "manual", action: "list_inbounds", clients: [], clientsObserved: false, inbounds: [{ id: 9, serviceId: "reality-service", name: "inbound-9", totalBytes: 200 * 1024 ** 3, usedBytes: 200 * 1024 ** 3, resetDays: 30, nextResetAt: "2026-08-23T00:00:00Z", planStatus: "failed", planError: "Agent did not confirm the inbound reset" }], inboundsObserved: true, resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:01Z" });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("节点套餐"))?.click();
      await Promise.resolve();
    });
    expect(document.body.textContent).toContain("节点套餐续期失败");
    expect(document.body.textContent).toContain("Agent did not confirm the inbound reset");
    expect(document.querySelector<HTMLAnchorElement>('a[href="/activity"]')?.textContent).toContain("前往活动查看详情");
  });

		it("starts first-run onboarding with a real location and the browser timezone", () => {
    const addresses = [{ address: "203.0.113.10", interface: "eth0", kind: "public" as const, observedAt: "2026-08-19T00:00:00Z" }];
    const container = render(<SetupWizard builtinHeadscaleAvailable cloudflareConfigured={false} cloudflareOAuthAvailable gatewayAddressCandidates={addresses} language="zh-CN" observedPublicAddress="203.0.113.10" onComplete={async () => undefined} onLanguage={() => undefined} publicAddressCandidates={addresses} publicAddressDetection="direct" suggestedAgentConnectUrl="" suggestedGatewayAddress="203.0.113.10" />);
    expect(container.textContent).toContain("创建第一个位置");
    expect(container.textContent).toContain("位置通常是一处家庭、办公室或数据中心");
    expect(container.querySelector<HTMLInputElement>("#setup-timezone")?.value).not.toBe("");
    expect(container.textContent).not.toContain("Default");
    const location = container.querySelector<HTMLInputElement>("#setup-location-name")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(location, "DMIT");
      location.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续"))?.click());
    expect(container.textContent).toContain("当前是临时访问地址");
		expect(container.textContent).toContain("安全私网模式会让 Center 只在 Headscale 网络内开放");
    expect(container.textContent).toContain("你准备在哪里使用 Vastora");
    expect(container.textContent).toContain("同一网络");
    expect(container.textContent).toContain("随时随地");
    act(() => container.querySelector<HTMLInputElement>('input[value="headscale"]')?.click());
    expect(container.textContent).toContain("设置安全连接");
    expect(container.textContent).toContain("登录 Cloudflare");
		expect(container.textContent).toContain("Center 私网地址");
		expect(container.textContent).toContain("Headscale 公网地址");
    const advanced = [...container.querySelectorAll("details")].find((details) => details.textContent?.includes("高级设置"));
    expect(advanced?.open).toBe(false);
    expect(container.querySelector("#setup-headscale-key")).toBeNull();
  });

  it("allows the detected setup timezone to be searched and changed", async () => {
    const addresses = [{ address: "203.0.113.10", interface: "eth0", kind: "public" as const, observedAt: "2026-08-19T00:00:00Z" }];
    const container = render(<SetupWizard builtinHeadscaleAvailable cloudflareConfigured={false} cloudflareOAuthAvailable gatewayAddressCandidates={addresses} language="zh-CN" observedPublicAddress="203.0.113.10" onComplete={async () => undefined} onLanguage={() => undefined} publicAddressCandidates={addresses} publicAddressDetection="direct" suggestedAgentConnectUrl="" suggestedGatewayAddress="203.0.113.10" />);
    const timezone = container.querySelector<HTMLInputElement>("#setup-timezone")!;
    await act(async () => {
      container.querySelector<HTMLButtonElement>('[aria-label="打开时区列表"]')?.click();
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(timezone, "Tokyo");
      timezone.dispatchEvent(new Event("input", { bubbles: true }));
      await Promise.resolve();
    });
    const option = [...document.body.querySelectorAll<HTMLElement>('[role="option"]')].find((item) => item.textContent?.includes("Asia/Tokyo"));
    expect(option).toBeDefined();
    await act(async () => {
      option?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });
    expect(timezone.value).toBe("Asia/Tokyo");
  });

  it("shows a cloud public mapping as unverified before the pre-install probe", () => {
    const gatewayAddresses = [{ address: "10.0.0.157", interface: "enp0s6", kind: "lan" as const, observedAt: "2026-08-24T00:00:00Z" }];
    const container = render(<SetupWizard builtinHeadscaleAvailable cloudflareConfigured cloudflareOAuthAvailable cloudflareZone="example.com" gatewayAddressCandidates={gatewayAddresses} language="zh-CN" observedPublicAddress="192.9.143.79" onComplete={async () => undefined} onLanguage={() => undefined} publicAddressCandidates={[]} publicAddressDetection="cloud_mapping_candidate" suggestedAgentConnectUrl="" suggestedGatewayAddress="10.0.0.157" />);
    const location = container.querySelector<HTMLInputElement>("#setup-location-name")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(location, "Oracle ARM");
      location.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续"))?.click());
    act(() => container.querySelector<HTMLInputElement>('input[value="headscale"]')?.click());

    expect(container.textContent).toContain("发现云公网地址 192.9.143.79");
    expect(container.textContent).toContain("本机 10.0.0.157");
    expect(container.textContent).toContain("这还不代表公网能够访问");
    expect(container.textContent).toContain("不需要先安装 Caddy");
    expect(container.querySelector<HTMLInputElement>("#setup-public-address")?.value).toBe("192.9.143.79");
    expect(container.querySelector<HTMLButtonElement>("#setup-gateway-address")?.textContent).toContain("10.0.0.157");
    expect(container.querySelector<HTMLButtonElement>("#setup-nat-confirmed")?.disabled).toBe(true);
  });

  it("verifies temporary public listeners before showing the setup review", async () => {
    let completeVerification: (() => void) | undefined;
    const verification = new Promise<{ status: "ready"; publicAddress: string; gatewayAddress: string; ports: number[] }>((resolve) => {
      completeVerification = () => resolve({ status: "ready", publicAddress: "192.9.143.79", gatewayAddress: "10.0.0.157", ports: [80, 443] });
    });
    const verify = vi.spyOn(api, "verifySetupPublicEntry").mockReturnValue(verification);
    const gatewayAddresses = [{ address: "10.0.0.157", interface: "enp0s6", kind: "lan" as const, observedAt: "2026-08-24T00:00:00Z" }];
    const container = render(<SetupWizard builtinHeadscaleAvailable cloudflareConfigured cloudflareOAuthAvailable cloudflareZone="example.com" gatewayAddressCandidates={gatewayAddresses} language="zh-CN" observedPublicAddress="192.9.143.79" onComplete={async () => undefined} onLanguage={() => undefined} publicAddressCandidates={[]} publicAddressDetection="cloud_mapping_candidate" suggestedAgentConnectUrl="" suggestedGatewayAddress="10.0.0.157" />);
    const location = container.querySelector<HTMLInputElement>("#setup-location-name")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(location, "Oracle ARM");
      location.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续"))?.click());
    act(() => container.querySelector<HTMLInputElement>('input[value="headscale"]')?.click());

    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续"))?.click();
      await Promise.resolve();
    });
    expect(verify).toHaveBeenCalledWith({ publicAddress: "192.9.143.79", gatewayAddress: "10.0.0.157", natConfirmed: false });
    expect(container.textContent).toContain("正在检测公网入口");
    expect(container.textContent).toContain("完成后会立即释放端口");

    await act(async () => {
      completeVerification?.();
      await verification;
    });
    expect(container.textContent).toContain("公网入口已验证");
    expect(container.textContent).toContain("80、443 已验证");
  });

  it("keeps setup on the network step when the public probe fails", async () => {
    vi.spyOn(api, "verifySetupPublicEntry").mockRejectedValue(new APIError("center: public ports 80 and 443 are not reachable", 400, "invalid_request"));
    const addresses = [{ address: "203.0.113.10", interface: "eth0", kind: "public" as const, observedAt: "2026-08-19T00:00:00Z" }];
    const container = render(<SetupWizard builtinHeadscaleAvailable cloudflareConfigured cloudflareOAuthAvailable cloudflareZone="example.com" gatewayAddressCandidates={addresses} language="zh-CN" observedPublicAddress="203.0.113.10" onComplete={async () => undefined} onLanguage={() => undefined} publicAddressCandidates={addresses} publicAddressDetection="direct" suggestedAgentConnectUrl="" suggestedGatewayAddress="203.0.113.10" />);
    const location = container.querySelector<HTMLInputElement>("#setup-location-name")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(location, "Public host");
      location.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续"))?.click());
    act(() => container.querySelector<HTMLInputElement>('input[value="headscale"]')?.click());
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续"))?.click();
      await Promise.resolve();
    });
    expect(container.textContent).toContain("你准备在哪里使用 Vastora");
    expect(container.textContent).not.toContain("确认首次设置");
    expect(container.textContent).toContain("public ports 80 and 443 are not reachable");
  });

  it("keeps a non-sensitive setup draft after a reload", () => {
    const addresses = [{ address: "203.0.113.10", interface: "eth0", kind: "public" as const, observedAt: "2026-08-19T00:00:00Z" }];
    const props = { builtinHeadscaleAvailable: true, cloudflareConfigured: false, cloudflareOAuthAvailable: true, gatewayAddressCandidates: addresses, language: "zh-CN" as const, observedPublicAddress: "203.0.113.10", onComplete: async () => undefined, onLanguage: () => undefined, publicAddressCandidates: addresses, publicAddressDetection: "direct" as const, suggestedAgentConnectUrl: "", suggestedGatewayAddress: "203.0.113.10" };
    let container = render(<SetupWizard {...props} />);
    const location = container.querySelector<HTMLInputElement>("#setup-location-name")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(location, "DMIT");
      location.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续"))?.click());
    act(() => container.querySelector<HTMLInputElement>('input[value="headscale"]')?.click());
    act(() => root?.unmount());
    root = undefined;
    document.body.replaceChildren();

    container = render(<SetupWizard {...props} />);
    expect(container.textContent).toContain("你准备在哪里使用 Vastora");
    expect(container.querySelector<HTMLInputElement>('input[value="headscale"]')?.checked).toBe(true);
    expect(window.sessionStorage.getItem("vastora.initial-setup.v1")).not.toContain("apiKey");
  });

  it("upgrades legacy zone-level setup defaults", () => {
    window.sessionStorage.setItem("vastora.initial-setup.v1", JSON.stringify({
      step: 2,
      name: "Cloudlead",
      timezone: "Asia/Singapore",
      domainSuffix: "kuddyx.com",
      mode: "headscale",
      agentConnectUrl: "https://center.kuddyx.com",
      headscaleMode: "builtin",
      headscaleUrl: "https://headscale.kuddyx.com",
      publicAddress: "203.0.113.10"
    }));
    const addresses = [{ address: "203.0.113.10", interface: "eth0", kind: "public" as const, observedAt: "2026-08-19T00:00:00Z" }];
    const props = { builtinHeadscaleAvailable: true, cloudflareConfigured: false, cloudflareOAuthAvailable: true, gatewayAddressCandidates: addresses, language: "zh-CN" as const, observedPublicAddress: "203.0.113.10", onComplete: async () => undefined, onLanguage: () => undefined, publicAddressCandidates: addresses, publicAddressDetection: "direct" as const, suggestedAgentConnectUrl: "", suggestedGatewayAddress: "203.0.113.10" };
    const container = render(<SetupWizard {...props} />);

    expect(container.querySelector<HTMLInputElement>("#setup-center-url")?.value).toBe("https://center.vastora.kuddyx.com");
    expect(container.querySelector<HTMLInputElement>("#setup-headscale-url")?.value).toBe("https://headscale.vastora.kuddyx.com");
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("返回"))?.click());
    expect(container.querySelector<HTMLInputElement>("#setup-domain")?.value).toBe("vastora.kuddyx.com");
    expect(container.textContent).toContain("服务域名空间");
  });

  it("preserves custom setup hostnames after Cloudflare is connected", () => {
    window.sessionStorage.setItem("vastora.initial-setup.v1", JSON.stringify({
      step: 2,
      name: "Cloudlead",
      timezone: "Asia/Singapore",
      domainSuffix: "services.ops.example.net",
      mode: "headscale",
      agentConnectUrl: "https://control.ops.example.net",
      headscaleMode: "builtin",
      headscaleUrl: "https://mesh.ops.example.net",
      publicAddress: "203.0.113.10"
    }));
    const addresses = [{ address: "203.0.113.10", interface: "eth0", kind: "public" as const, observedAt: "2026-08-19T00:00:00Z" }];
    const container = render(<SetupWizard builtinHeadscaleAvailable cloudflareConfigured cloudflareOAuthAvailable cloudflareZone="kuddyx.com" gatewayAddressCandidates={addresses} language="zh-CN" observedPublicAddress="203.0.113.10" onComplete={async () => undefined} onLanguage={() => undefined} publicAddressCandidates={addresses} publicAddressDetection="direct" suggestedAgentConnectUrl="" suggestedGatewayAddress="203.0.113.10" />);

    expect(container.querySelector<HTMLInputElement>("#setup-center-url")?.value).toBe("https://control.ops.example.net");
    expect(container.querySelector<HTMLInputElement>("#setup-headscale-url")?.value).toBe("https://mesh.ops.example.net");
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("返回"))?.click());
    expect(container.querySelector<HTMLInputElement>("#setup-domain")?.value).toBe("services.ops.example.net");
  });

  it("explains a conflicting DNS record instead of reporting an invalid form", () => {
    const error = new APIError("center: DNS record center.vastora.kuddyx.com already exists with a different value", 400, "dns_record_conflict");
    expect(userError("zh-CN", error)).toContain("已有指向其他服务器的 DNS 记录");
    expect(userError("en", error)).toContain("did not overwrite");
  });

  it("keeps the technical reason when initial setup fails", async () => {
    const failure = new APIError("center: verify Headscale: dial tcp: lookup headscale.example.com: no such host", 400, "invalid_request");
    const onComplete = vi.fn().mockRejectedValue(failure);
    const container = render(<SetupWizard builtinHeadscaleAvailable cloudflareConfigured={false} cloudflareOAuthAvailable={false} gatewayAddressCandidates={[]} language="zh-CN" observedPublicAddress="" onComplete={onComplete} onLanguage={() => undefined} publicAddressCandidates={[]} publicAddressDetection="unavailable" suggestedAgentConnectUrl="https://center.example.com" suggestedGatewayAddress="" />);
    const fill = (selector: string, value: string) => {
      const input = container.querySelector<HTMLInputElement>(selector)!;
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    };
    act(() => fill("#setup-location-name", "Cloudlead"));
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续"))?.click());
		act(() => container.querySelector<HTMLInputElement>('input[value="headscale"]')?.click());
		const advanced = [...container.querySelectorAll("details")].find((details) => details.textContent?.includes("高级设置"))!;
		act(() => { advanced.open = true; advanced.dispatchEvent(new Event("toggle", { bubbles: true })); });
		act(() => container.querySelector<HTMLButtonElement>("#setup-external-headscale")?.click());
		act(() => fill("#setup-headscale-url", "https://headscale.example.com"));
		act(() => fill("#setup-headscale-key", "hskey-api-abcdefghijklmnopqrstuvwxyz"));
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续"))?.click());
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("完成并添加节点"))?.click();
      await Promise.resolve();
    });

    expect(onComplete).toHaveBeenCalledOnce();
    expect(container.textContent).toContain("安全私网地址的 DNS 尚未生效");
    expect(container.textContent).toContain("查看技术详情");
    expect(container.textContent).toContain("lookup headscale.example.com: no such host");
  });

  it("opens Cloudflare in a normal tab and offers recovery actions", async () => {
    vi.useFakeTimers();
    const popupDocument = document.implementation.createHTMLDocument("Cloudflare");
    const replace = vi.fn();
    const close = vi.fn();
    const popup = { close, document: popupDocument, location: { replace }, opener: window } as unknown as Window;
    const open = vi.spyOn(window, "open").mockReturnValue(popup);
    vi.spyOn(api, "startCloudflareOAuth").mockResolvedValue({ sessionId: "session", authorizationUrl: "https://dash.cloudflare.test/oauth2/auth", expiresAt: new Date(Date.now() + 60_000).toISOString() });
    vi.spyOn(api, "pollCloudflareOAuth").mockResolvedValue({ status: "pending" });
    const container = render(<CloudflareOAuthConnect available connected={false} language="zh-CN" onConnected={() => undefined} />);

    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("登录 Cloudflare"))?.click();
      await Promise.resolve();
    });

    expect(open).toHaveBeenCalledWith("about:blank", "_blank");
    expect(replace).toHaveBeenCalledWith("https://dash.cloudflare.test/oauth2/auth");
    expect(container.textContent).toContain("Cloudflare 首次加载可能需要几秒");
    expect(container.textContent).toContain("重新打开登录页");
    expect(container.textContent).toContain("复制登录链接");
    expect(container.textContent).toContain("取消");
  });

  it("uses the saved Center address when adding a node and keeps editing advanced", () => {
    const data = dashboard();
    data.agents = [];
    const container = render(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    const add = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加节点"));
    act(() => add?.click());
    expect(document.querySelector<HTMLInputElement>("#new-node-center")?.value).toBe("https://center.example.com");
    expect(document.body.textContent).toContain("Agent 将连接");
    const advanced = [...document.querySelectorAll("details")].find((details) => details.textContent?.includes("Center 地址"));
    expect(advanced?.open).toBe(false);
  });

  it("downloads and directly runs the executable Agent installer", () => {
    const command = agentInstallCommand({
      centerURL: "https://center.example.com",
      enrollment: { token: "one-time-token", siteId: "site", installerUrl: "https://headscale.example.com", expiresAt: "2026-08-18T00:10:00Z" },
      installerAvailable: true
    });
    expect(command).toContain("curl -fsSL");
    expect(command).toContain("https://headscale.example.com/install/agent.sh");
    expect(command).toContain("-o /tmp/vastora-agent-install.sh");
    expect(command).toContain("chmod +x /tmp/vastora-agent-install.sh");
    expect(command).toContain("/tmp/vastora-agent-install.sh 'one-time-token'");
    expect(command).not.toContain("sudo");
    expect(command).not.toContain("| sh");
    expect(command).not.toContain("Authorization: Bearer");
    expect(command).not.toContain("--proto");
    expect(command).not.toContain("--tlsv1.2");
    expect(command).not.toContain("--name");
    expect(command).not.toContain("--roles");
    expect(command).not.toContain("--capabilities");
  });

  it("generates one safe command for node purpose changes and Agent updates", () => {
    const container = render(<NodesView data={dashboard()} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    const manage = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("管理"));
    act(() => manage?.click());
    expect(document.body.textContent).toContain("节点用途");
    expect(document.body.textContent).toContain("重新安装当前版本");
    const update = [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("重新安装当前版本"));
    act(() => update?.click());
    expect(document.body.textContent).toContain("agent update");
    expect(document.body.textContent).toContain("--center-url 'https://center.example.com'");
    const tunnel = document.querySelector<HTMLElement>("#manage-node-tunnel");
    act(() => tunnel?.click());
    const generate = [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("生成修改命令"));
    act(() => generate?.click());
    expect(document.body.textContent).toContain("agent configure");
    expect(document.body.textContent).toContain("--capabilities 'docker,gateway'");
  });

  it("does not ask for integration secrets again when editing", () => {
    const data = dashboard();
    data.integrations = [
      { kind: "headscale", mode: "external", endpoint: "https://headscale.example.com:8443", secretSet: true, status: "configured" },
      { kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "a".repeat(32), zoneId: "b".repeat(32), secretSet: true, status: "configured" }
    ];
    const container = render(<NetworkView data={data} language="zh-CN" mutate={async () => undefined} />);
    const editButtons = [...container.querySelectorAll("button")].filter((button) => button.textContent?.includes("修改"));
    act(() => editButtons[0]?.click());
    expect(document.querySelector<HTMLInputElement>("#headscale-key")?.required).toBe(false);
    expect(document.body.textContent).toContain("留空会继续使用原 Key");
  });

  it("installs bundled Headscale without asking for a key or a shell command", () => {
    const data = dashboard();
    data.integrations = [];
    const container = render(<NetworkView data={data} language="zh-CN" mutate={async () => undefined} />);
    const setup = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("设置"));
    act(() => setup?.click());
    expect(document.body.textContent).toContain("无需命令和 API Key");
    expect(document.body.textContent).toContain("安装并连接");
    expect(document.querySelector("#headscale-key")).toBeNull();
    expect(document.body.textContent).not.toContain("docker compose exec headscale");
  });

  it("requires a secure Agent-reachable Center address", () => {
    expect(validCenterURL("https://center.example.com")).toBe(true);
    expect(validCenterURL("http://127.0.0.1:8080")).toBe(true);
    expect(validCenterURL("http://100.64.0.1:8080")).toBe(false);
    expect(validCenterURL("https://user:password@center.example.com")).toBe(false);
    expect(validCenterURL("https://center.example.com/api")).toBe(false);
  });

  it("makes backup and diagnostics discoverable without the CLI", () => {
    const container = render(<SettingsView data={dashboard()} language="zh-CN" mutate={async () => undefined} onCenterUpdateStatus={() => undefined} onLogout={async () => undefined} onRefresh={async () => undefined} />);
    expect(container.textContent).toContain("数据与故障排查");
    expect(container.textContent).toContain("下载加密备份");
    expect(container.textContent).toContain("下载诊断报告");
    expect(container.textContent).toContain("不含 Token");
    expect(container.textContent).toContain("修改管理员密码");
    const changePassword = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("修改管理员密码"));
    act(() => changePassword?.click());
    expect(document.querySelector<HTMLInputElement>("#new-password")?.minLength).toBe(10);
    expect(document.body.textContent).toContain("至少 10 个字符。");
  });

  it("offers one safe workflow for switching the Vastora domain", async () => {
    const startOAuth = vi.spyOn(api, "startCloudflareOAuth");
    const listZones = vi.spyOn(api, "cloudflareZones").mockResolvedValue({ zones: [
      { id: "current", name: "example.com", accountId: "account", accountName: "Personal" },
      { id: "new", name: "new.example", accountId: "account", accountName: "Personal" }
    ] });
    const container = render(<SettingsView data={dashboard()} language="zh-CN" mutate={async () => undefined} onCenterUpdateStatus={() => undefined} onLogout={async () => undefined} onRefresh={async () => undefined} />);
    expect(container.textContent).toContain("Vastora 域名");
    expect(container.textContent).toContain("https://center.vastora.example.com");
    const changeDomain = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("切换域名"));
    await act(async () => {
      changeDomain?.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(document.body.textContent).toContain("旧地址不会立即失效");
    expect(document.body.textContent).toContain("使用现有 Cloudflare 授权");
    expect(document.body.textContent).toContain("切换域名无需重新登录");
    expect(document.body.textContent).toContain("new.example");
    expect(document.body.textContent).not.toContain("登录 Cloudflare");
    expect(document.body.textContent).toContain("下一次心跳验证新地址后自动切换");
    expect(document.body.textContent).toContain("应用访问入口需在切换后重新创建");
    expect(listZones).toHaveBeenCalledOnce();
    expect(startOAuth).not.toHaveBeenCalled();
  });

  it("shows a confirmed Center update instead of exposing Docker access", () => {
    const data = dashboard();
    data.centerUpdate = { currentVersion: "0.1.0-alpha.47", latestVersion: "0.1.0-alpha.48", updateAvailable: true, automatic: true, state: "idle", checkedAt: "2026-08-25T00:00:00Z" };
    const container = render(<SettingsView data={data} language="zh-CN" mutate={async () => undefined} onCenterUpdateStatus={() => undefined} onLogout={async () => undefined} onRefresh={async () => undefined} />);
    expect(container.textContent).toContain("Center 更新");
    expect(container.textContent).toContain("0.1.0-alpha.48");
    const update = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("更新 Center"));
    act(() => update?.click());
    expect(document.body.textContent).toContain("预计短暂断开连接");
    expect(document.body.textContent).toContain("开始更新");
  });

  it("uses the update status as the single displayed Center version", () => {
    const data = dashboard();
    data.status.version = "0.1.0-alpha.50";
    data.centerUpdate.currentVersion = "0.1.0-alpha.51";
    const container = render(<SettingsView data={data} language="zh-CN" mutate={async () => undefined} onCenterUpdateStatus={() => undefined} onLogout={async () => undefined} onRefresh={async () => undefined} />);
    expect(container.textContent).not.toContain("0.1.0-alpha.50");
    expect(container.textContent?.match(/0\.1\.0-alpha\.51/g)).toHaveLength(2);
  });

  it("refreshes the complete settings data as soon as a Center update succeeds", async () => {
    const status = { ...dashboard().centerUpdate, latestVersion: "0.1.0-alpha.51", updateAvailable: true, state: "applying" as const };
    const onRefresh = vi.fn().mockResolvedValue(undefined);
    const onStatusChange = vi.fn();
    vi.spyOn(api, "centerUpdate").mockResolvedValue({ ...status, currentVersion: "0.1.0-alpha.51", updateAvailable: false, state: "succeeded" });
    render(<CenterUpdateCard language="zh-CN" onRefresh={onRefresh} onStatusChange={onStatusChange} status={status} />);
    await act(async () => { await Promise.resolve(); });
    expect(onRefresh).toHaveBeenCalledOnce();
    expect(onStatusChange).not.toHaveBeenCalled();
  });
});
