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
  vi.useRealTimers();
});

const dashboard = (): AppData => ({
  status: { version: "test", agentInstallerAvailable: true, agentConnectionMode: "lan", agentConnectUrl: "https://center.example.com" },
  sources: [], organizations: [], routes: [], actions: [], integrations: [],
  sites: [{ id: "site", organizationId: "org", name: "Home", code: "home", description: "", timezone: "Asia/Singapore", domainSuffix: "home.example", status: "active", gatewayNodes: ["agent"], gatewayStatus: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }],
  agents: [{ id: "agent", name: "home-server", version: "test", status: "active", appliedInstallations: 1, enrolledAt: "2026-08-18T00:00:00Z", lastSeenAt: "2026-08-18T00:00:00Z", connected: true, siteId: "site", roles: ["worker", "gateway"], capabilities: { docker: true, gateway: true, tunnel: true, metrics: false, logs: false }, networkCandidates: [{ address: "192.168.1.2", interface: "eth0", family: "ipv4", kind: "lan", observedAt: "2026-08-18T00:00:00Z" }], networkProfile: { serviceAddress: "192.168.1.2", lanAddress: "192.168.1.2", enabledKinds: ["lan"], directPublic: false }, gatewayHealthy: true }],
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
  data.applications = [{ ...data.applications[0], id: "three-x-ui", name: "3x-ui", appKey: "vastora-official/3x-ui", installedVersion: "3.6.0", availableVersion: "3.6.0" }];
  return data;
};

function render(element: ReactNode) {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root?.render(element));
  return container;
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
    expect(defaultPublicationHostname(data, service)).toBe("manager.komari-agent.home.vastora.example.com");
    data.services.push({ ...service, id: "subscription", name: "订阅服务" });
    expect(defaultPublicationHostname(data, service)).toBe("manager.komari-agent.home.vastora.example.com");
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
    data.agents[0].networkCandidates = [{ address: "100.64.0.1", interface: "tailscale0", family: "ipv4", kind: "headscale", observedAt: "2026-08-18T00:00:00Z" }];
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
    const store = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("应用商店"));
    act(() => store?.click());
    expect(store?.getAttribute("aria-pressed")).toBe("true");
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
	data.agents[0].networkCandidates = [{ address: "203.0.113.10", interface: "eth0", family: "ipv4", kind: "public", observedAt: "2026-08-18T00:00:00Z" }];
	data.agents[0].networkProfile = { serviceAddress: "203.0.113.10", publicAddress: "203.0.113.10", enabledKinds: ["public"], directPublic: true };
	data.services = [{ id: "vless", applicationId: "running", siteId: "site", name: "VLESS", protocol: "tcp", containerPort: 2443, hostPort: 2443, endpoint: "203.0.113.10:2443", source: "observed", appProtocol: "vless/tcp", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
	const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
	const add = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加入口"));
	act(() => add?.click());
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

  it("offers one-click REALITY with a hierarchical connection hostname", async () => {
    const data = realityDashboard();
    vi.spyOn(api, "latestApplicationCommand").mockRejectedValue(new APIError("not found", 404, "not_found"));
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("创建 VLESS"))?.click();
      await Promise.resolve();
    });
    expect(document.body.textContent).toContain("目标站点、密钥、端口和共享 443 会自动配置");
    expect(document.querySelector<HTMLInputElement>("#reality-hostname")?.value).toBe("reality.home-server.home.vastora.example.com");
    expect(document.querySelector<HTMLSelectElement>("#reality-gateway")?.value).toBe("agent");
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
    expect(document.querySelector<HTMLInputElement>("#subscription-hostname")?.value).toBe("subscription.3x-ui.home.vastora.example.com");
    expect(document.querySelector<HTMLSelectElement>("#subscription-kind")?.value).toBe("cloudflare_tunnel");
  });

  it("manages 3x-ui clients and reveals links without opening the panel", async () => {
    const data = realityDashboard();
    const baseCommand: ApplicationCommand = { id: "client-command-list", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.clients.manage", state: "succeeded", hostname: "", dnsProvider: "manual", action: "list", clients: [{ email: "MacBook", enabled: true, totalBytes: 10 * 1024 ** 3, usedBytes: 1024, expiryTime: 0, limitIp: 2, inboundIds: [9], hasSubscription: true }], clientsObserved: true, inbounds: [{ id: 9, name: "inbound-9", connectHostname: "reality.example.test" }], subscriptionAvailable: true, resultAvailable: false, createdAt: "2026-08-22T00:00:00Z", updatedAt: "2026-08-22T00:00:01Z" };
    const create = vi.spyOn(api, "createThreeXUIClientCommand").mockImplementation(async (input) => input.action.startsWith("reveal_") ? { ...baseCommand, id: `client-command-${input.action}`, action: input.action, resultAvailable: true } : baseCommand);
    const reveal = vi.spyOn(api, "revealApplicationCommand").mockImplementation(async (id) => ({ shareUri: id.includes("clash") ? "https://subscription.example.test/clash/client-id" : "vless://one-time-client-link" }));
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
    expect(document.body.textContent).toContain("日常管理");
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("复制 VLESS"))?.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(reveal).toHaveBeenCalledWith("client-command-reveal_link");
    expect(writeText).toHaveBeenCalledWith("vless://one-time-client-link");
    expect(document.body.textContent).toContain("vless://one-time-client-link");
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("OpenClash"))?.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(create).toHaveBeenCalledWith({ applicationId: "three-x-ui", action: "reveal_clash_subscription", email: "MacBook", inboundId: undefined });
    expect(writeText).toHaveBeenCalledWith("https://subscription.example.test/clash/client-id");
    expect(document.body.textContent).toContain("OpenClash / Mihomo 订阅地址");
  });

  it("offers browser-trusted HTTPS only when Cloudflare is connected", () => {
    const data = dashboard();
    data.sites[0].domainSuffix = "vastora.example.com";
    data.services = [{ id: "manager", applicationId: "running", siteId: "site", name: "manager", protocol: "http", containerPort: 8317, hostPort: 8317, endpoint: "192.168.1.2:8317", source: "catalog", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    let container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加入口"))?.click());
    expect(document.body.textContent).toContain("连接 Cloudflare 后可开启");
    expect(document.querySelector<HTMLButtonElement>("#publication-tls")?.disabled).toBe(true);

    act(() => root?.unmount());
    root = undefined;
    document.body.replaceChildren();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, status: "configured" }];
    container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加入口"))?.click());
    expect(document.body.textContent).toContain("使用 Cloudflare DNS 验证申请公信证书");
    expect(document.querySelector<HTMLButtonElement>("#publication-tls")?.disabled).toBe(false);
  });

  it("reveals a REALITY client link only after explicit confirmation", async () => {
    const data = realityDashboard();
    vi.spyOn(api, "latestApplicationCommand").mockResolvedValue({ id: "application-command-1", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.reality.create", state: "succeeded", hostname: "reality.home-server.home.vastora.example.com", dnsProvider: "manual", target: "www.example.com:443", sniHostname: "www.example.com", resultAvailable: true, createdAt: "2026-08-20T00:00:00Z", updatedAt: "2026-08-20T00:00:01Z" });
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

  it("starts first-run onboarding with a real location and the browser timezone", () => {
    const container = render(<SetupWizard builtinHeadscaleAvailable cloudflareConfigured={false} cloudflareOAuthAvailable language="zh-CN" onComplete={async () => undefined} onLanguage={() => undefined} publicAddressCandidates={[{ address: "203.0.113.10", interface: "eth0", family: "ipv4", kind: "public", observedAt: "2026-08-19T00:00:00Z" }]} suggestedAgentConnectUrl="" />);
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
    expect(container.textContent).toContain("自动填写服务器能够访问的正式地址");
    expect(container.textContent).toContain("你准备在哪里使用 Vastora");
    expect(container.textContent).toContain("同一网络");
    expect(container.textContent).toContain("随时随地");
    act(() => container.querySelector<HTMLInputElement>('input[value="headscale"]')?.click());
    expect(container.textContent).toContain("设置安全连接");
    expect(container.textContent).toContain("登录 Cloudflare");
    expect(container.textContent).toContain("Center 地址");
    expect(container.textContent).toContain("私网地址");
    const advanced = [...container.querySelectorAll("details")].find((details) => details.textContent?.includes("Headscale 来源"));
    expect(advanced?.open).toBe(false);
    expect(container.querySelector("#setup-headscale-key")).toBeNull();
  });

  it("keeps a non-sensitive setup draft after a reload", () => {
    const props = { builtinHeadscaleAvailable: true, cloudflareConfigured: false, cloudflareOAuthAvailable: true, language: "zh-CN" as const, onComplete: async () => undefined, onLanguage: () => undefined, publicAddressCandidates: [{ address: "203.0.113.10", interface: "eth0", family: "ipv4" as const, kind: "public" as const, observedAt: "2026-08-19T00:00:00Z" }], suggestedAgentConnectUrl: "" };
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
      dnsMode: "cloudflare",
      publicAddress: "203.0.113.10"
    }));
    const props = { builtinHeadscaleAvailable: true, cloudflareConfigured: false, cloudflareOAuthAvailable: true, language: "zh-CN" as const, onComplete: async () => undefined, onLanguage: () => undefined, publicAddressCandidates: [{ address: "203.0.113.10", interface: "eth0", family: "ipv4" as const, kind: "public" as const, observedAt: "2026-08-19T00:00:00Z" }], suggestedAgentConnectUrl: "" };
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
      dnsMode: "cloudflare",
      publicAddress: "203.0.113.10"
    }));
    const container = render(<SetupWizard builtinHeadscaleAvailable cloudflareConfigured cloudflareOAuthAvailable cloudflareZone="kuddyx.com" language="zh-CN" onComplete={async () => undefined} onLanguage={() => undefined} publicAddressCandidates={[{ address: "203.0.113.10", interface: "eth0", family: "ipv4", kind: "public", observedAt: "2026-08-19T00:00:00Z" }]} suggestedAgentConnectUrl="" />);

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
    const container = render(<SetupWizard builtinHeadscaleAvailable cloudflareConfigured={false} cloudflareOAuthAvailable={false} language="zh-CN" onComplete={onComplete} onLanguage={() => undefined} publicAddressCandidates={[]} suggestedAgentConnectUrl="https://center.example.com" />);
    const fill = (selector: string, value: string) => {
      const input = container.querySelector<HTMLInputElement>(selector)!;
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    };
    act(() => fill("#setup-location-name", "Cloudlead"));
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续"))?.click());
    act(() => container.querySelector<HTMLInputElement>('input[value="headscale"]')?.click());
    act(() => fill("#setup-headscale-url", "https://headscale.example.com"));
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
      enrollment: { token: "one-time-token", siteId: "site", expiresAt: "2026-08-18T00:10:00Z" },
      installerAvailable: true
    });
    expect(command).toContain("curl -fsSL");
    expect(command).toContain("https://center.example.com/install/agent.sh");
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
    const container = render(<SettingsView data={dashboard()} language="zh-CN" mutate={async () => undefined} onLogout={async () => undefined} />);
    expect(container.textContent).toContain("数据与故障排查");
    expect(container.textContent).toContain("下载加密备份");
    expect(container.textContent).toContain("下载诊断报告");
    expect(container.textContent).toContain("不含 Token");
    expect(container.textContent).toContain("修改管理员密码");
  });
});
