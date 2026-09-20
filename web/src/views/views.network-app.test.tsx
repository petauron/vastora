// @vitest-environment jsdom

import { act } from "react";
import { describe, expect, it, vi } from "vitest";
import type { Publication } from "../types";
import { APIError, api } from "../api";
import { vastoraDomainDefaults } from "../lib/network";
import { AppsView } from "./AppsView";
import { HomeView } from "./HomeView";
import { NetworkView } from "./NetworkView";
import { CenterRemoteAccessSheet } from "./CenterRemoteAccessSheet";
import { defaultPublicationHostname } from "./appAccess";
import { CopyButton } from "./shared";

import { dashboard, rerender, openAppDetails, realityDashboard, render, renderAppDetails } from "./views.test-support";

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

  it("keeps private hostnames readable and leaves public hostnames to Center", () => {
    const data = dashboard();
    data.sites[0].domainSuffix = "vastora.example.com";
    const service = { id: "manager", applicationId: "running", siteId: "site", name: "Manager 页面", protocol: "http" as const, containerPort: 8317, hostPort: 8317, endpoint: "192.168.1.2:8317", source: "catalog" as const, management: true, status: "running", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" };
    data.services = [service];
    expect(defaultPublicationHostname(data, service)).toBe("manager-komari-agent.home.vastora.example.com");
    expect(defaultPublicationHostname(data, service, "cloudflare_tunnel")).toBe("");
    data.services.push({ ...service, id: "subscription", name: "订阅服务" });
    expect(defaultPublicationHostname(data, service)).toBe("manager-komari-agent.home.vastora.example.com");
    expect(defaultPublicationHostname(data, service, "cloudflare_tunnel")).toBe("");
  });

  it("keeps Cloudflare zones separate from the Vastora service namespace", () => {
    expect(vastoraDomainDefaults("Example.COM.")).toEqual({
      zone: "example.com",
      namespace: "vastora.example.com",
      centerURL: "https://center.vastora.example.com",
      headscaleURL: "https://headscale.vastora.example.com"
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

  it("enables an Agent-detected cloud NAT mapping without Center co-location", async () => {
    const data = dashboard();
    data.agents[0].publicEgress = { address: "198.51.100.27", bindAddress: "192.168.1.2", mode: "nat", observedAt: "2026-01-01T00:00:00Z" };
    const confirm = vi.spyOn(api, "confirmNetworkProfile").mockResolvedValue(data.agents[0].networkProfile!);
    const container = render(<NetworkView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} />);
    const nodeButton = [...container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("修改") && button.closest("div")?.parentElement?.textContent?.includes("home-server"));
    act(() => nodeButton?.click());
    const publicSwitch = document.querySelector<HTMLButtonElement>("#public-ingress-enabled")!;
    expect(publicSwitch.disabled).toBe(false);
    act(() => publicSwitch.click());
    expect(document.body.textContent).toContain("198.51.100.27 → 192.168.1.2 · 云 NAT");
    const saveButton = [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("保存配置"))!;
    await act(async () => {
      saveButton.click();
      await Promise.resolve();
    });
    expect(confirm).toHaveBeenCalledWith("agent", expect.objectContaining({ publicAddress: "198.51.100.27", publicBindAddress: "192.168.1.2", publicMode: "nat", directPublic: true, enabledKinds: expect.arrayContaining(["public"]) }));
  });

  it("keeps the public switch disabled until the Agent reports an egress mapping", () => {
    const data = dashboard();
    const container = render(<NetworkView data={data} language="zh-CN" mutate={async () => undefined} />);
    const nodeButton = [...container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("修改") && button.closest("div")?.parentElement?.textContent?.includes("home-server"));
    act(() => nodeButton?.click());
    expect(document.querySelector<HTMLButtonElement>("#public-ingress-enabled")?.disabled).toBe(true);
    expect(document.body.textContent).toContain("等待 Agent 启动检测公网出口");
    expect(document.body.textContent).not.toContain("与 Center 同机");
  });

  it("keeps the fixed Tailscale endpoint off by default and requires explicit UDP confirmation", async () => {
    const data = dashboard();
    data.integrations = [{ kind: "headscale", mode: "builtin", endpoint: "https://headscale.example.com", secretSet: true, status: "configured" }];
    data.tailscaleFixedEndpoint = {
      available: true,
      enabled: false,
      endpoint: "",
      localAddress: "",
      detectedEndpoint: "203.0.113.10:41641",
      detectedLocalAddress: "192.168.1.2",
      localAddressCandidates: [{ address: "192.168.1.2", interface: "eth0", kind: "lan", observedAt: "2026-08-28T00:00:00Z" }],
      status: "disabled"
    };
    const configure = vi.spyOn(api, "configureTailscaleFixedEndpoint").mockResolvedValue({ ...data.tailscaleFixedEndpoint, enabled: true, endpoint: "203.0.113.10:41641", localAddress: "192.168.1.2", status: "configured" });
    const container = render(<NetworkView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} />);
    expect(container.textContent).toContain("当前关闭，Tailscale 会通过 STUN 自动发现");
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("配置"))?.click());
    const enabled = document.querySelector<HTMLButtonElement>("#tailscale-fixed-endpoint-enabled")!;
    expect(document.body.textContent).not.toContain("HTTP/HTTPS 检测和 tailscale netcheck 都不能单独证明");
    act(() => enabled.click());
    expect(document.body.textContent).toContain("HTTP/HTTPS 检测和 tailscale netcheck 都不能单独证明");
    const save = [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("保存配置"))!;
    expect(save.disabled).toBe(true);
    act(() => document.querySelector<HTMLButtonElement>("#tailscale-fixed-endpoint-confirm")?.click());
    expect(save.disabled).toBe(false);
    await act(async () => {
      save.click();
      await Promise.resolve();
    });
    expect(configure).toHaveBeenCalledWith({ enabled: true, endpoint: "203.0.113.10:41641", localAddress: "192.168.1.2", confirmMapping: true });
  });

  it("shows the explicit adoption command only for a proven older Vastora Tailscale install", () => {
    const data = dashboard();
    data.integrations = [{ kind: "headscale", mode: "builtin", endpoint: "https://headscale.example.com", secretSet: true, status: "configured" }];
    data.tailscaleFixedEndpoint = {
      available: false,
      enabled: false,
      endpoint: "",
      localAddress: "",
      detectedEndpoint: "",
      detectedLocalAddress: "192.168.1.2",
      localAddressCandidates: [],
      status: "unavailable",
      lastError: "This older Agent reports external Tailscale ownership."
    };
    const container = render(<NetworkView data={data} language="zh-CN" mutate={async () => undefined} />);
    expect(container.textContent).toContain("接管旧版 Tailscale");
    expect(container.textContent).toContain("sudo vastora agent adopt-tailscale --confirm-vastora-ownership");
    expect(container.textContent).not.toContain("固定 Tailscale 直连端点");
  });

  it("manages the Center remote fallback independently from application tunnels", async () => {
    const data = dashboard();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, accessManagement: true, turnstileManagement: true, status: "configured" }];
    data.centerRemoteAccess = { available: true, enabled: false, status: "disabled" };
    const configure = vi.spyOn(api, "configureCenterRemoteAccess").mockResolvedValue({ available: true, enabled: true, hostname: "center-vastora.example.com", protectionMode: "native", turnstileSiteKey: "site-key", status: "configured" });
    const container = render(<NetworkView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} />);
    expect(container.textContent).toContain("Center 远程备用入口");
    const remoteAccessCard = [...container.querySelectorAll<HTMLElement>('[data-slot="card"]')].find((card) => card.textContent?.includes("Center 远程备用入口"));
    act(() => remoteAccessCard?.querySelector<HTMLButtonElement>("button")?.click());
    act(() => document.querySelector<HTMLButtonElement>("#center-remote-access-enabled")?.click());
    expect(document.body.textContent).toContain("center-vastora.example.com");
    expect(document.body.textContent).toContain("直达 Center 登录（推荐）");
    expect(document.querySelector("#center-remote-access-audience")).toBeNull();
    const save = [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("保存并启用"))!;
    expect(save.disabled).toBe(false);
    await act(async () => {
      save.click();
      await Promise.resolve();
    });
    expect(configure).toHaveBeenCalledWith({ enabled: true, protectionMode: "native" });
  });

  it("asks an existing Cloudflare connection to grant Turnstile management before enabling the direct fallback", () => {
    const data = dashboard();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, accessManagement: false, status: "configured" }];
    data.centerRemoteAccess = { available: true, enabled: false, status: "disabled" };
    const container = render(<NetworkView data={data} language="zh-CN" mutate={async () => undefined} />);
    const remoteAccessCard = [...container.querySelectorAll<HTMLElement>('[data-slot="card"]')].find((card) => card.textContent?.includes("Center 远程备用入口"));
    act(() => remoteAccessCard?.querySelector<HTMLButtonElement>("button")?.click());
    act(() => document.querySelector<HTMLButtonElement>("#center-remote-access-enabled")?.click());
    expect(document.body.textContent).toContain("重新连接");
    expect(document.body.textContent).toContain("需要补充 Cloudflare 授权");
    expect(document.body.textContent).toContain("创建专用 Turnstile 组件");
    expect([...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("保存并启用"))?.disabled).toBe(true);
  });

  it("saves Access duration and leaves partial synchronization visible for retry", async () => {
    const data = dashboard();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, accessManagement: true, status: "configured" }];
    const access = { available: true, enabled: true, hostname: "center-vastora.example.com", protectionMode: "access" as const, audienceKind: "email" as const, audienceValue: "admin@example.com", status: "configured" as const, accessSessionDuration: "6h" };
    data.centerRemoteAccess = access;
    const configure = vi.spyOn(api, "configureCenterRemoteAccess")
      .mockResolvedValueOnce({ ...access, accessSessionSync: { status: "partial", total: 2, updated: 1, failedHosts: ["panel.example.com"], policyOverrideHosts: ["panel.example.com"] } })
      .mockResolvedValueOnce({ ...access, accessSessionSync: { status: "synced", total: 2, updated: 2, policyOverrideHosts: ["panel.example.com"] } });
    const container = render(<NetworkView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} />);
    const card = [...container.querySelectorAll<HTMLElement>('[data-slot="card"]')].find((item) => item.textContent?.includes("Center 远程备用入口"));
    act(() => card?.querySelector<HTMLButtonElement>("button")?.click());
    expect(document.querySelector("#center-access-session-duration")).not.toBeNull();
    expect(document.body.textContent).toContain("6 小时");
    const save = [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("保存并同步"))!;
    await act(async () => { save.click(); });
    expect(configure).toHaveBeenLastCalledWith({ enabled: true, protectionMode: "access", audienceKind: "email", audienceValue: "admin@example.com", accessSessionDuration: "6h" });
    expect(document.body.textContent).toContain("部分入口尚未同步");
    expect(document.body.textContent).toContain("panel.example.com");
    expect(document.body.textContent).toContain("未被覆盖");
    expect(document.body.textContent).toContain("到期不一定需要重新输入邮箱验证码");
    expect(document.querySelector("#center-access-session-duration")).not.toBeNull();
    await act(async () => { save.click(); });
    expect(document.body.textContent).toContain("2/2 个入口已同步");
    expect(document.body.textContent).not.toContain("部分入口尚未同步");
  });

  it("defaults Access duration to 24h and hides it in native mode", async () => {
    const base = { available: true, enabled: true, protectionMode: "access" as const, audienceKind: "email" as const, audienceValue: "admin@example.com", status: "configured" as const };
    const cloudflare = { kind: "cloudflare" as const, mode: "oauth" as const, secretSet: true, accessManagement: true, turnstileManagement: true, status: "configured" as const };
    const save = vi.fn(async () => undefined);
    const props = { cloudflare, language: "zh-CN" as const, open: true, onClose: vi.fn(), onCloudflareConnected: async () => undefined, onSave: save };
    render(<CenterRemoteAccessSheet {...props} access={base} />);
    expect(document.body.textContent).toContain("24 小时（默认）");
    await act(async () => { document.querySelector("form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ accessSessionDuration: "24h" }));
    rerender(<CenterRemoteAccessSheet {...props} access={{ ...base, protectionMode: "native" }} />);
    expect(document.querySelector("#center-access-session-duration")).toBeNull();
    await act(async () => { document.querySelector("form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });
    expect(save).toHaveBeenLastCalledWith({ enabled: true, protectionMode: "native" });
  });

  it("blocks unsupported Access duration and keeps request errors in the sheet", async () => {
    const access = { available: true, enabled: true, protectionMode: "access" as const, audienceKind: "email" as const, audienceValue: "admin@example.com", status: "configured" as const, accessSessionDuration: "-1h" };
    const props = { access, cloudflare: { kind: "cloudflare" as const, mode: "oauth" as const, secretSet: true, accessManagement: true, status: "configured" as const }, language: "zh-CN" as const, open: true, onClose: vi.fn(), onCloudflareConnected: async () => undefined, onSave: vi.fn(async () => { throw new Error("request failed"); }) };
    render(<CenterRemoteAccessSheet {...props} />);
    await act(async () => { document.querySelector("form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });
    expect(props.onSave).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("请选择支持的会话时长");
    rerender(<CenterRemoteAccessSheet {...props} access={{ ...access, accessSessionDuration: "24h" }} />);
    await act(async () => { document.querySelector("form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });
    expect(props.onSave).toHaveBeenCalledOnce();
    expect(props.onClose).not.toHaveBeenCalled();
    expect(document.querySelector('[role="alert"]')).not.toBeNull();
  });

  it("shows only successful applications and marks host-privileged packages", () => {
    const container = render(<AppsView data={dashboard()} language="zh-CN" mutate={async () => undefined} />);
    expect(container.textContent).toContain("Komari 探针");
    expect(container.textContent).toContain("高权限");
    expect(container.textContent).not.toContain("Failed");
    expect(container.textContent).toContain("管理应用、订阅与各节点的访问入口");
    const installed = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("已安装"));
    const store = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("应用商店"));
    expect(installed?.getAttribute("aria-selected")).toBe("true");
    act(() => store?.click());
    expect(store?.getAttribute("aria-selected")).toBe("true");
    expect(store?.textContent).toContain("1");
    expect(container.textContent).toContain("所有可用节点都已安装或正在安装此应用");
  });

  it("shows one application workspace at a time and switches using named tabs", () => {
    const data = dashboard();
    data.apps.push({ ...data.apps[0], key: "vastora-official/z-app", app: { ...data.apps[0].app, id: "z-app", name: { en: "Second app", "zh-CN": "第二个应用" } } });
    data.applications.push({ ...data.applications[0], id: "second-app", appKey: "vastora-official/z-app", name: "第二个应用" });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    expect(container.querySelectorAll("[data-app-group]")).toHaveLength(1);
    expect(container.querySelector('[data-application-id="running"]')).not.toBeNull();
    const tab = [...container.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find((item) => item.textContent?.includes("第二个应用"));
    expect(tab).toBeDefined();
    act(() => tab?.click());
    expect(tab?.getAttribute("aria-selected")).toBe("true");
    expect(container.querySelectorAll("[data-app-group]")).toHaveLength(1);
    expect(container.querySelector('[data-application-id="second-app"]')).not.toBeNull();
    expect(container.querySelector('[data-application-id="running"]')).toBeNull();
  });

  it("keeps CPA installation one-click and protects reveal and rotation", async () => {
    const data = dashboard();
    const reauthentication = ["test", "reauth"].join("-");
    data.apps = [{ key: "vastora-official/cpa", sourceId: "vastora-official", fetchedAt: "2026-08-18T00:00:00Z", app: { id: "cpa", version: "7.2.130", name: { en: "CPA", "zh-CN": "CPA" }, description: { en: "Proxy API", "zh-CN": "代理 API" }, config: [{ key: "debug", label: { en: "Debug logging", "zh-CN": "调试日志" }, description: { en: "Extra logs", "zh-CN": "额外日志" }, type: "boolean", required: false, secret: false, default: false }] } }];
    data.applications = [];
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.trim() === "安装")?.click());
    expect(document.body.textContent).toContain("调试日志");
    expect(document.body.textContent).not.toContain("管理密钥");
    expect(document.body.textContent).not.toContain("客户端 API 密钥");
    expect(document.body.textContent).not.toContain("时区");
    act(() => [...document.querySelectorAll("button")].find((button) => button.textContent?.trim() === "取消")?.click());

    data.applications = [{ id: "cpa-application", name: "CPA", nodeId: "agent", siteId: "site", appKey: "vastora-official/cpa", image: "cpa", status: "running", runtime: "docker", installedVersion: "7.2.130", availableVersion: "7.2.130", updateAvailable: false, createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    const reveal = vi.spyOn(api, "revealApplicationCredentials").mockResolvedValue({ kind: "cpa", managementKey: "management-value", clientApiKey: "client-value" });
    const rotate = vi.spyOn(api, "rotateApplicationCredentials").mockResolvedValue({ id: "rotation-1", applicationId: "cpa-application", target: "management", state: "pending", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" });
    rerender(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    const installedTab = [...container.querySelectorAll("button")].find((button) => button.textContent?.startsWith("已安装"));
    expect(installedTab).toBeDefined();
    act(() => installedTab?.click());
    openAppDetails(container, "cpa-application");
    const credentialsButton = [...document.querySelectorAll("button")].find((button) => button.textContent?.trim() === "凭据");
    expect(credentialsButton).toBeDefined();
    act(() => credentialsButton?.click());
    const revealPassword = document.querySelector<HTMLInputElement>("#application-credential-reauthentication");
    if (!revealPassword) throw new Error("credential reauthentication input was not rendered");
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(revealPassword, reauthentication);
      revealPassword.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("验证并查看"))?.click();
      await Promise.resolve();
    });
    expect(reveal).toHaveBeenCalledWith("cpa-application", reauthentication);
    expect(document.querySelector<HTMLInputElement>("#cpa-management-key")?.type).toBe("password");
    expect(document.querySelector<HTMLInputElement>("#cpa-client-api-key")?.type).toBe("password");
    act(() => [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("轮换管理密钥"))?.click());
    const rotationPassword = document.querySelector<HTMLInputElement>("#application-credential-rotation-password");
    if (!rotationPassword) throw new Error("credential rotation password input was not rendered");
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(rotationPassword, reauthentication);
      rotationPassword.dispatchEvent(new Event("input", { bubbles: true }));
      document.querySelector<HTMLButtonElement>("#application-credential-rotation-confirm")?.click();
    });
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("验证并轮换"))?.click();
      await Promise.resolve();
    });
    expect(rotate).toHaveBeenCalledWith("cpa-application", "management", reauthentication, expect.any(String));
    expect(document.body.textContent).toContain("凭据轮换已排队");
  });

  it("keeps an installed app manageable after a failed change", () => {
    const data = dashboard();
    data.apps[0].app.config = [{ key: "endpoint", label: { en: "Endpoint", "zh-CN": "地址" }, description: { en: "Service endpoint", "zh-CN": "服务地址" }, type: "string", required: true, secret: false }];
    data.applications = [{ ...data.applications[1], appKey: "vastora-official/komari-agent", name: "Komari Agent", installedVersion: "1.2.60", availableVersion: "1.2.60", updateAvailable: false }];
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    expect(container.textContent).toContain("最近一次操作失败，应用仍保留");
    expect(container.textContent).toContain("修改配置");
    expect(container.textContent).toContain("卸载");
    expect(container.textContent).toContain("版本已是最新");
  });

  it("offers upgrade only when the catalog contains a newer version", () => {
    const data = dashboard();
    data.applications[0] = { ...data.applications[0], installedVersion: "1.2.59", availableVersion: "1.2.60", updateAvailable: true };
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    expect(container.textContent).toContain("升级到 v1.2.60");
    expect(container.textContent).not.toContain("版本已是最新");
  });

  it.each(["install", "upgrade"] as const)("blocks an open %s sheet when the catalog expires or disappears", async (operation) => {
    const data = dashboard();
    if (operation === "install") data.applications = [];
    else data.applications[0] = { ...data.applications[0], installedVersion: "1.2.59", availableVersion: "1.2.60", updateAvailable: true };
    const create = vi.spyOn(api, "createDeployment");
    const mutate = vi.fn(async () => undefined);
    const container = render(<AppsView data={data} language="zh-CN" mutate={mutate} />);
    if (operation === "upgrade") openAppDetails(container, "running");
    act(() => {
      const button = operation === "install"
        ? document.querySelector<HTMLButtonElement>('[aria-label="安装 Komari 探针"]')
        : [...document.querySelectorAll<HTMLButtonElement>("button")].find(value => value.textContent?.includes("升级到 v1.2.60"));
      button?.click();
    });
    const submitButton = () => document.querySelector<HTMLButtonElement>('[role="dialog"] button[type="submit"]');
    expect(submitButton()?.disabled).toBe(false);
    for (const apps of [[{ ...data.apps[0], installBlocked: true }], []]) {
      rerender(<AppsView data={{ ...data, apps }} language="zh-CN" mutate={mutate} />);
      expect(submitButton()?.disabled).toBe(true);
      expect(document.getElementById("deployment-catalog-error")?.textContent).toContain("刷新应用目录");
      await act(async () => document.querySelector('[role="dialog"] form')?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
      expect(mutate).not.toHaveBeenCalled();
      expect(create).not.toHaveBeenCalled();
    }
    rerender(<AppsView data={data} language="zh-CN" mutate={mutate} />);
    expect(submitButton()?.disabled).toBe(false);
  });

  it("requires reviewing a newly published version instead of silently upgrading from an open sheet", async () => {
    const data = dashboard();
    data.applications[0] = { ...data.applications[0], installedVersion: "1.2.59", availableVersion: "1.2.60", updateAvailable: true };
    const mutate = vi.fn(async () => undefined);
    renderAppDetails(<AppsView data={data} language="zh-CN" mutate={mutate} />);
    act(() => [...document.querySelectorAll<HTMLButtonElement>("button")].find(value => value.textContent?.includes("升级到 v1.2.60"))?.click());
    const updated = { ...data, apps: [{ ...data.apps[0], app: { ...data.apps[0].app, version: "1.2.61" } }] };
    rerender(<AppsView data={updated} language="zh-CN" mutate={mutate} />);
    expect(document.querySelector<HTMLButtonElement>('[role="dialog"] button[type="submit"]')?.disabled).toBe(true);
    expect(document.getElementById("deployment-catalog-error")?.textContent).toContain("目录中的版本已更新");
    await act(async () => document.querySelector('[role="dialog"] form')?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(mutate).not.toHaveBeenCalled();
  });

  it("rechecks catalog expiry on submit even before another dashboard refresh", async () => {
    const data = dashboard();
    data.applications = [];
    data.apps[0].catalogExpiresAt = "2030-01-01T00:00:00Z";
    const now = vi.spyOn(Date, "now").mockReturnValue(Date.parse("2029-12-31T23:59:59Z"));
    const mutate = vi.fn(async () => undefined);
    render(<AppsView data={data} language="zh-CN" mutate={mutate} />);
    act(() => document.querySelector<HTMLButtonElement>('[aria-label="安装 Komari 探针"]')?.click());
    const submit = document.querySelector<HTMLButtonElement>('[role="dialog"] button[type="submit"]');
    expect(submit?.disabled).toBe(false);
    now.mockReturnValue(Date.parse("2030-01-01T00:00:00Z"));
    await act(async () => submit?.click());
    expect(mutate).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("目录需要重新验证");
  });

  it("permits recovery and uninstall when no catalog is available", async () => {
    const data = dashboard();
    data.apps = [];
    data.deployments = [{ id: "recover", agentId: "agent", appKey: "vastora-official/komari-agent", appVersion: "1.2.60", state: "failed", operation: "upgrade", deleteData: false, applicationId: "running", reconciliationRequired: true, createdAt: "2026-09-12T00:00:00Z", updatedAt: "2026-09-12T00:00:00Z" }];
    const recover = vi.spyOn(api, "retryTaskReconciliation").mockResolvedValue({ taskId: "recover", kind: "application.apply", queued: true });
    const create = vi.spyOn(api, "createDeployment").mockResolvedValue({ ...data.deployments[0], id: "uninstall", operation: "uninstall", state: "pending", reconciliationRequired: false });
    const mutate = async (action: () => Promise<unknown>) => { await action(); };
    const container = render(<AppsView data={data} language="zh-CN" mutate={mutate} />);
    await act(async () => [...container.querySelectorAll<HTMLButtonElement>("button")].find(value => value.textContent?.includes("继续恢复"))?.click());
    expect(recover).toHaveBeenCalledWith("recover");
    rerender(<AppsView data={{ ...data, deployments: [] }} language="zh-CN" mutate={mutate} />);
    openAppDetails(container, "running");
    act(() => [...document.querySelectorAll<HTMLButtonElement>("button")].find(value => value.textContent?.trim() === "卸载")?.click());
    const uninstall = [...document.querySelectorAll<HTMLButtonElement>("button")].find(value => value.textContent?.trim() === "卸载并保留数据");
    expect(uninstall?.disabled).toBe(false);
    await act(async () => uninstall?.click());
    expect(create).toHaveBeenCalledWith("agent", "vastora-official/komari-agent", {}, "uninstall", false);
  });

  it("keeps configure and uninstall available while expired catalog blocks upgrade", async () => {
    const data = dashboard();
    data.apps[0].installBlocked = true;
    data.apps[0].app.config = [{ key: "endpoint", label: { en: "Endpoint", "zh-CN": "地址" }, description: { en: "Service endpoint", "zh-CN": "服务地址" }, type: "string", required: true, secret: false }];
    data.applications[0] = { ...data.applications[0], installedVersion: "1.2.59", availableVersion: "1.2.60", updateAvailable: true };
    const create = vi.spyOn(api, "createDeployment").mockResolvedValue({ id: "configure", agentId: "agent", appKey: data.apps[0].key, appVersion: "1.2.59", state: "pending", operation: "configure", deleteData: false, createdAt: "2026-09-12T00:00:00Z", updatedAt: "2026-09-12T00:00:00Z" });
    const details = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async action => { await action(); }} />);
    const button = (text: string) => [...details.querySelectorAll<HTMLButtonElement>("button")].find(value => value.textContent?.includes(text));
    expect(button("升级到")?.disabled).toBe(true);
    expect(button("卸载")?.disabled).toBe(false);
    expect(button("修改配置")?.disabled).toBe(false);
    act(() => button("修改配置")?.click());
    const input = document.querySelector<HTMLInputElement>("#config-endpoint");
    expect(input).not.toBeNull();
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(input, "https://monitor.example");
      input?.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(document.getElementById("deployment-catalog-error")).toBeNull();
    const submit = document.querySelector<HTMLButtonElement>('[role="dialog"] button[type="submit"]');
    expect(submit?.disabled).toBe(false);
    await act(async () => submit?.click());
    expect(create).toHaveBeenCalledWith("agent", data.apps[0].key, { endpoint: "https://monitor.example" }, "configure", false, undefined, undefined, undefined);
  });

  it.each(["zh-CN", "en"] as const)("separates version status from aligned controller actions: %s", (language) => {
    const data = realityDashboard();
    data.apps[0].app.config = [{ key: "port", label: { en: "Port", "zh-CN": "端口" }, description: { en: "Service port", "zh-CN": "服务端口" }, type: "string", required: true, secret: false }];
    data.applications.push({ ...data.applications[0], id: "worker", role: "worker", controllerApplicationId: "three-x-ui", nodeSyncStatus: "ready" });
    const container = render(<AppsView data={data} language={language} mutate={async () => undefined} />);
    const manage = container.querySelector<HTMLButtonElement>('[data-slot="subscription-controller"] button[aria-label]');
    expect(manage).not.toBeNull();
    act(() => manage!.click());
    const footer = document.querySelector<HTMLElement>('[data-slot="sheet-footer"]');
    const actions = footer?.querySelector<HTMLElement>('[role="group"]');
    const version = footer?.querySelector<HTMLElement>('[data-slot="badge"]');
    expect(version?.textContent).toBe(language === "zh-CN" ? "版本已是最新" : "Version up to date");
    expect(actions?.contains(version!)).toBe(false);
    expect(version?.parentElement).toBe(actions?.parentElement);
    expect(version?.parentElement?.classList.contains("items-center")).toBe(true);
    expect(actions?.classList.contains("items-center")).toBe(true);
    expect(actions?.classList.contains("flex-wrap")).toBe(true);
    expect(actions?.getAttribute("aria-label")).toBe(language === "zh-CN" ? "应用操作" : "Application actions");
    const buttons = [...(actions?.querySelectorAll<HTMLButtonElement>("button") ?? [])];
    expect(buttons.map((button) => button.textContent)).toEqual(language === "zh-CN" ? ["迁移订阅主机", "修改配置", "卸载"] : ["Move subscription host", "Change settings", "Uninstall"]);
    expect(buttons.at(-1)?.disabled).toBe(true);
  });

  it("keeps uninstall available when an installed app leaves the catalog", () => {
    const data = dashboard();
    data.apps = [];
    data.applications = [data.applications[0]];
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
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

  it("offers an automatic node-direct shared 443 listener for raw TLS services", () => {
	const data = dashboard();
	data.agents[0].networkCandidates = [{ address: "203.0.113.10", interface: "eth0", kind: "public", observedAt: "2026-08-18T00:00:00Z" }];
	data.agents[0].networkProfile = { serviceAddress: "203.0.113.10", publicAddress: "203.0.113.10", publicBindAddress: "203.0.113.10", publicMode: "direct", enabledKinds: ["public"], directPublic: true };
	data.services = [{ id: "vless", applicationId: "running", siteId: "site", name: "VLESS", protocol: "tcp", containerPort: 2443, hostPort: 2443, endpoint: "203.0.113.10:2443", source: "observed", appProtocol: "vless/tcp", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
	const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
	const add = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加入口"));
	act(() => add?.click());
	act(() => document.querySelector<HTMLButtonElement>("#publication-kind")?.click());
		expect(document.body.textContent).toContain("节点直连 443");
	expect(document.body.textContent).toContain("自动启用本机 HAProxy");
		act(() => [...document.querySelectorAll<HTMLElement>('[role="option"]')].find((option) => option.textContent?.includes("节点直连 443"))?.click());
	expect(document.body.textContent).toContain("普通应用与入口位于同一节点时");
  });

  it("explains the managed REALITY container-port 443 exception", () => {
    const data = realityDashboard();
    data.services = [{ id: "reality", applicationId: "three-x-ui", siteId: "site", name: "inbound-9", protocol: "tcp", containerPort: 443, hostPort: 443, endpoint: "10.0.0.10:443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", guardStatus: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加入口"))?.click());
    act(() => document.querySelector<HTMLButtonElement>("#publication-kind")?.click());
    act(() => [...document.querySelectorAll<HTMLElement>('[role="option"]')].find((option) => option.textContent?.includes("节点直连 443"))?.click());
    expect(document.body.textContent).toContain("Xray 只监听节点的私有服务地址");
    expect(document.body.textContent).toContain("宿主机公网 443 由 HAProxy 独占");
    expect(document.body.textContent).not.toContain("应用内部端口不能是 443");
  });

  it.each([
    ["random-code", "请输入完整域名"],
    ["https://panel.example.com/", "不要包含 https://、端口或路径"],
    ["panel.example.com:8080", "不要包含 https://、端口或路径"],
    ["panel.example.net", "域名必须属于当前 Cloudflare 域名 example.com"],
    ["not-example.example.net", "域名必须属于当前 Cloudflare 域名 example.com"],
  ])("explains invalid publication hostname %s without creating an entry", async (hostname, message) => {
    const data = dashboard();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, accessManagement: true, status: "configured" }];
    data.centerRemoteAccess = { available: true, enabled: true, protectionMode: "access", status: "configured" };
    data.services = [{ id: "panel", applicationId: "running", siteId: "site", name: "panel", protocol: "http", containerPort: 8317, hostPort: 8317, endpoint: "192.168.1.2:8317", source: "catalog", management: true, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    const create = vi.spyOn(api, "createPublication").mockRejectedValue(new Error("unexpected publication"));
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} />);
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加入口"))?.click());
    act(() => document.querySelector<HTMLInputElement>('input[value="public_web"]')?.click());
    const input = document.querySelector<HTMLInputElement>("#publication-hostname")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, hostname);
      input.dispatchEvent(new Event("input", { bubbles: true }));
      input.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
    });

    expect(input.getAttribute("aria-invalid")).toBe("true");
    expect(input.getAttribute("aria-describedby")).toContain("publication-hostname-error");
    expect(document.querySelector("#publication-hostname-error")?.textContent).toContain(message);
    expect([...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("创建访问方式"))?.disabled).toBe(true);
    await act(async () => {
      input.closest("form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(create).not.toHaveBeenCalled();

    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "  PANEL.EXAMPLE.COM.  ");
      input.dispatchEvent(new Event("input", { bubbles: true }));
      input.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
    });
    expect(input.value).toBe("panel.example.com");
    expect(input.getAttribute("aria-invalid")).toBe("false");
    expect(document.querySelector("#publication-hostname-error")).toBeNull();
    await act(async () => { input.closest("form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });
    expect(create).toHaveBeenCalledWith(expect.objectContaining({ hostname: "panel.example.com" }));
  });

  it("keeps public access submission errors inside the open sheet", async () => {
    const data = dashboard();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, accessManagement: true, status: "configured" }];
    data.centerRemoteAccess = { available: true, enabled: true, hostname: "center-vastora.example.com", protectionMode: "access", audienceKind: "email", audienceValue: "admin@example.com", status: "configured" };
    data.services = [{ id: "panel", applicationId: "running", siteId: "site", name: "panel", protocol: "http", containerPort: 8317, hostPort: 8317, endpoint: "192.168.1.2:8317", source: "catalog", management: true, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    const create = vi.spyOn(api, "createPublication").mockRejectedValue(new APIError("center: publication failed", 400, "invalid_request"));
    const mutate = vi.fn(async (operation: () => Promise<unknown>) => { await operation(); });
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={mutate} />);

    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加入口"))?.click());
    act(() => document.querySelector<HTMLInputElement>('input[value="public_web"]')?.click());
    const submit = [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("创建访问方式"))!;
    expect(submit.disabled).toBe(false);
    await act(async () => {
      submit.click();
      await Promise.resolve();
    });

    expect(create).toHaveBeenCalledWith(expect.objectContaining({ kind: "cloudflare_tunnel", hostname: undefined }));
    expect(mutate).toHaveBeenCalledWith(expect.any(Function), "访问入口已创建。", { reportError: false });
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain("填写内容不完整或格式不正确");
  });

  it.each([
    [{ available: true, enabled: false, status: "disabled" as const }, undefined, "请先启用 Center 远程入口"],
    [{ available: true, enabled: true, status: "pending" as const }, undefined, "Center 远程入口正在配置"],
    [{ available: true, enabled: true, status: "failed" as const, lastError: "Access application is missing" }, undefined, "Center 远程入口配置失败"],
    [{ available: true, enabled: true, protectionMode: "native" as const, status: "configured" as const }, undefined, "应用入口需要 Cloudflare Access 模式"],
    [null, "status unavailable", "无法读取 Center 远程入口状态"]
  ])("blocks Cloudflare publication with an accurate remote entry status", (remoteAccess, loadError, expected) => {
    const data = dashboard();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, accessManagement: true, status: "configured" }];
    data.centerRemoteAccess = remoteAccess;
    data.centerRemoteAccessError = loadError;
    data.services = [{ id: "panel", applicationId: "running", siteId: "site", name: "panel", protocol: "http", containerPort: 8317, hostPort: 8317, endpoint: "192.168.1.2:8317", source: "catalog", management: true, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);

    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加入口"))?.click());
    act(() => document.querySelector<HTMLInputElement>('input[value="public_web"]')?.click());

    expect(document.body.textContent).toContain(expected);
    expect([...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("创建访问方式"))?.disabled).toBe(true);
  });

  it("loads a missing Center remote entry status when the publication sheet opens", async () => {
    const data = dashboard();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, accessManagement: true, status: "configured" }];
    data.centerRemoteAccess = null;
    data.services = [{ id: "panel", applicationId: "running", siteId: "site", name: "panel", protocol: "http", containerPort: 8317, hostPort: 8317, endpoint: "192.168.1.2:8317", source: "catalog", management: true, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    const status = vi.spyOn(api, "centerRemoteAccess").mockResolvedValue({ available: true, enabled: true, hostname: "center-vastora.example.com", protectionMode: "access", audienceKind: "email", audienceValue: "admin@example.com", status: "configured" });
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);

    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加入口"))?.click();
      await Promise.resolve();
    });
    act(() => document.querySelector<HTMLInputElement>('input[value="public_web"]')?.click());

    expect(status).toHaveBeenCalledOnce();
    expect(document.body.textContent).not.toContain("尚未读取 Center 远程入口状态");
    expect([...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("创建访问方式"))?.disabled).toBe(false);
  });

  it("opens the CPA client API through a dedicated Tunnel flow without requiring Center Access", async () => {
    const data = dashboard();
    data.apps = [{ key: "vastora-official/cpa", sourceId: "vastora-official", fetchedAt: "2026-08-18T00:00:00Z", app: { id: "cpa", version: "7.2.130", name: { en: "CPA", "zh-CN": "CPA" }, description: { en: "Proxy API", "zh-CN": "代理 API" }, config: [] } }];
    data.applications = [{ ...data.applications[0], id: "cpa-application", name: "CPA", appKey: "vastora-official/cpa", image: "cpa", runtime: "docker", installedVersion: "7.2.130", availableVersion: "7.2.130" }];
    data.services = [
      { id: "cpa-api", applicationId: "cpa-application", siteId: "site", name: "api", protocol: "http", containerPort: 8317, hostPort: 8317, endpoint: "192.168.1.2:8317", source: "catalog", management: true, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" },
      { id: "cpa-client-api", applicationId: "cpa-application", siteId: "site", name: "client-api", protocol: "http", containerPort: 8317, hostPort: 8317, endpoint: "192.168.1.2:8317", source: "catalog", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }
    ];
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, accessManagement: true, status: "configured" }];
    data.centerRemoteAccess = null;
    data.centerRemoteAccessError = "Center Access is unavailable";
    const status = vi.spyOn(api, "centerRemoteAccess").mockRejectedValue(new Error("unexpected Center Access request"));
    const created: Publication = { id: "cpa-public-api", serviceId: "cpa-client-api", kind: "cloudflare_tunnel", ingress: { owner: "tunnel_connector", entryNodeId: "agent" }, hostname: "cpa.example.com", dnsProvider: "cloudflare", tlsEnabled: true, desiredRevision: 1, appliedRevision: 0, status: "pending", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" };
    const create = vi.spyOn(api, "createPublication").mockResolvedValue(created);
    const mutate = vi.fn(async (operation: () => Promise<unknown>) => { await operation(); });
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={mutate} />);

    act(() => [...container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("开启公网 API"))?.click());

    expect(document.body.textContent).toContain("开启 CPA 公网 API");
    expect(document.body.textContent).toContain("只转发 /v1 请求");
    expect(document.body.textContent).toContain("API 域名（可自定义）");
    expect(document.body.textContent).not.toContain("谁需要访问");
    expect(document.body.textContent).not.toContain("Center 远程入口状态");
    expect(status).not.toHaveBeenCalled();
    const submit = [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "开启公网 API")!;
    expect(submit.disabled).toBe(false);
    await act(async () => {
      submit.click();
      await Promise.resolve();
    });

    expect(create).toHaveBeenCalledWith(expect.objectContaining({ serviceId: "cpa-client-api", kind: "cloudflare_tunnel", ingress: { owner: "tunnel_connector", entryNodeId: "agent" }, hostname: undefined, dnsProvider: "cloudflare" }));
    expect(mutate).toHaveBeenCalledWith(expect.any(Function), "公网 API 已开启。", { reportError: false });
  });

	it("keeps terminal failures in Activity instead of presenting them as live app operations", () => {
    const data = dashboard();
    data.deployments = [{ id: "failed-install", agentId: "agent", appKey: "vastora-official/komari-agent", appVersion: "1.2.60", state: "failed", operation: "install", deleteData: false, error: "container could not start", applicationId: "failed", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
		expect(container.textContent).not.toContain("最近操作");
		expect(container.textContent).not.toContain("container could not start");
		expect(container.textContent).not.toContain("操作未完成，请检查填写内容后重试");
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
		data.publications = [{ id: "private-panel", serviceId: "manager", kind: "headscale_gateway", ingress: { owner: "site_gateway", entryNodeId: "agent" }, hostname: "panel.home.example", dnsProvider: "headscale", tlsEnabled: false, desiredRevision: 2, appliedRevision: 1, status: "degraded", accessUrl: "http://panel.home.example/", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
		const stop = vi.spyOn(api, "stopPublication").mockResolvedValue({ stopped: true });
		const verify = vi.spyOn(api, "verifyPublication");
		const updateTLS = vi.spyOn(api, "updatePublicationTLS");
		const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} />);

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

});
