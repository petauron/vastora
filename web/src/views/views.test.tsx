// @vitest-environment jsdom

import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { DashboardData } from "../App";
import { api } from "../api";
import { AppsView } from "./AppsView";
import { HomeView } from "./HomeView";
import { NetworkView } from "./NetworkView";
import { NodesView, agentInstallCommand, validCenterURL } from "./NodesView";
import { SettingsView } from "./SettingsView";
import { SetupWizard } from "./SetupWizard";
import { CloudflareOAuthConnect } from "./CloudflareOAuthConnect";

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

const dashboard = (): DashboardData => ({
  status: { version: "test", catalogSources: 1, catalogApps: 1, agents: 1, deployments: 1, agentInstallerAvailable: true, agentConnectionMode: "lan", agentConnectUrl: "https://center.example.com" },
  sources: [], organizations: [], routes: [], actions: [], integrations: [],
  sites: [{ id: "site", organizationId: "org", name: "Home", code: "home", description: "", timezone: "Asia/Singapore", domainSuffix: "home.example", status: "active", gatewayNodes: ["agent"], gatewayStatus: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }],
  agents: [{ id: "agent", name: "home-server", version: "test", status: "active", appliedInstallations: 1, enrolledAt: "2026-08-18T00:00:00Z", lastSeenAt: "2026-08-18T00:00:00Z", connected: true, siteId: "site", roles: ["worker", "gateway"], capabilities: { docker: true, gateway: true, tunnel: true, metrics: false, logs: false }, networkCandidates: [{ address: "192.168.1.2", interface: "eth0", family: "ipv4", kind: "lan", observedAt: "2026-08-18T00:00:00Z" }], networkProfile: { serviceAddress: "192.168.1.2", lanAddress: "192.168.1.2", enabledKinds: ["lan"], directPublic: false }, gatewayHealthy: true }],
  apps: [{ key: "vastora-official/komari-agent", sourceId: "vastora-official", fetchedAt: "2026-08-18T00:00:00Z", app: { id: "komari-agent", version: "1.2.60", name: { en: "Komari Agent", "zh-CN": "Komari 探针" }, description: { en: "Monitoring", "zh-CN": "监控探针" }, hostAccess: true, config: [] } }],
  applications: [
    { id: "running", name: "Komari Agent", nodeId: "agent", siteId: "site", appKey: "vastora-official/komari-agent", image: "image", status: "running", runtime: "docker", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" },
    { id: "failed", name: "Failed", nodeId: "agent", siteId: "site", appKey: "vastora-official/failed", image: "image", status: "failed", runtime: "docker", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }
  ],
  deployments: [], services: [], publications: []
});

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
    data.status.agents = 0;
    let destination = "";
    const container = render(<HomeView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={(screen) => { destination = screen; }} />);
    expect(container.textContent).toContain("完成首次设置");
    expect(container.textContent).not.toContain("管理员账号已创建");
    expect(container.textContent).toContain("一次只完成当前步骤");
    const add = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加节点"));
    act(() => add?.click());
    expect(destination).toBe("nodes");
  });

  it("shows LAN, Headscale, and public networking as simultaneous capabilities", () => {
    const data = dashboard();
    data.agents.push({ ...data.agents[0], id: "retired", name: "retired-node", status: "disabled", connected: false });
    const container = render(<NetworkView data={data} language="zh-CN" mutate={async () => undefined} />);
    expect(container.textContent).toContain("局域网");
    expect(container.textContent).toContain("Headscale");
    expect(container.textContent).toContain("公网与 Cloudflare");
    expect(container.textContent).toContain("同时使用局域网、Headscale 和公网");
    expect(container.textContent).not.toContain("retired-node");
  });

  it("shows only successful applications and marks host-privileged packages", () => {
    const container = render(<AppsView data={dashboard()} language="zh-CN" mutate={async () => undefined} />);
    expect(container.textContent).toContain("Komari 探针");
    expect(container.textContent).toContain("高权限");
    expect(container.textContent).not.toContain("Failed");
    expect(container.textContent).toContain("先把应用安装为私有服务");
    expect(container.textContent).toContain("所有可用节点都已安装此应用");
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
    expect(container.textContent).toContain("container could not start");
    expect(container.textContent).toContain("重试");
    expect(container.textContent).toContain("无需手动刷新");
  });

  it("guides a new administrator to add the first node", () => {
    const data = dashboard();
    data.agents = [];
    data.status.agents = 0;
    const container = render(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    expect(container.textContent).toContain("添加第一台节点");
    expect(container.textContent).toContain("复制一条命令");
    expect(container.textContent).toContain("添加节点");
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

  it("moves an unfinished built-in setup draft from public 8443 to standard HTTPS", () => {
    window.sessionStorage.setItem("vastora.initial-setup.v1", JSON.stringify({
      step: 2,
      name: "DMIT",
      timezone: "Asia/Singapore",
      domainSuffix: "example.com",
      mode: "headscale",
      agentConnectUrl: "https://center.example.com:8443",
      headscaleMode: "builtin",
      headscaleUrl: "https://headscale.example.com:8443",
      dnsMode: "manual",
      publicAddress: "203.0.113.10"
    }));
    const container = render(<SetupWizard builtinHeadscaleAvailable cloudflareConfigured={false} cloudflareOAuthAvailable language="zh-CN" onComplete={async () => undefined} onLanguage={() => undefined} publicAddressCandidates={[]} suggestedAgentConnectUrl="" />);
    expect(container.querySelector<HTMLInputElement>("#setup-center-url")?.value).toBe("https://center.example.com");
    expect(container.querySelector<HTMLInputElement>("#setup-headscale-url")?.value).toBe("https://headscale.example.com");
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

  it("generates a TLS-restricted one-line Agent installer", () => {
    const command = agentInstallCommand({
      capabilities: "docker,gateway",
      centerURL: "https://center.example.com",
      enrollment: { token: "one-time-token", siteId: "site", expiresAt: "2026-08-18T00:10:00Z" },
      installerAvailable: true,
      name: "home-node",
      roles: "worker,gateway"
    });
    expect(command).toContain("curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL");
    expect(command).toContain("https://center.example.com/install/agent.sh");
    expect(command).toContain("sudo sh -s --");
    expect(command).toContain("--token 'one-time-token'");
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
