// @vitest-environment jsdom

import { act } from "react";
import { describe, expect, it, vi } from "vitest";
import type { ApplicationCommand } from "../types";
import { APIError, api } from "../api";
import { AppsView } from "./AppsView";
import { NodesView } from "./NodesView";
import { SetupWizard } from "./SetupWizard";
import { commandSecretScope, secretOperation } from "../secret-delivery";

import { dashboard, rerender, resetRender, mockCommandEvent, openAppDetails, openRealityCreation, realityDashboard, render, renderAppDetails } from "./views.test-support";

describe("network and app views", () => {
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
    expect(container.querySelector('table[aria-label="节点全局状态"]')).not.toBeNull();
    expect(container.textContent).not.toContain("IP 质量");
    expect(container.textContent).not.toContain("Netflix");
    expect(container.textContent).not.toContain("ChatGPT");
    expect(container.querySelectorAll("tbody tr").length).toBeGreaterThanOrEqual(4);
  });

  it("shows the native architecture of each node", () => {
    const data = dashboard();
    data.agents.push({ ...data.agents[0], id: "arm-node", name: "arm-edge", architecture: "arm64" });
    const container = render(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    expect(container.textContent).toContain("x64");
    expect(container.textContent).toContain("ARM64");
  });

  it.each([false, true])("requires confirmation before removing the controller local node: %s", async (confirmed) => {
    const data = realityDashboard();
    data.services = [{ id: "local-node", applicationId: "three-x-ui", siteId: "site", name: "inbound-9", displayName: "Local VLESS", protocol: "tcp", containerPort: 443, hostPort: 443, endpoint: "10.0.0.10:443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    const remove = vi.spyOn(api, "removeRealityCommand").mockResolvedValue({ id: "remove-local", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.reality.remove", action: "remove", state: "succeeded", hostname: "", dnsProvider: "manual", resultAvailable: false, createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" });
    const mutate = vi.fn(async () => undefined);
    const details = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={mutate} />);
    const open = [...details.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("移除本机节点"));
    expect(open).toBeDefined();
    act(() => open!.click());
    const dialog = document.querySelector<HTMLElement>('[role="alertdialog"]');
    expect(dialog?.textContent).toContain("订阅地址、用户和其他节点保留");
    expect(remove).not.toHaveBeenCalled();
    await act(async () => {
      const action = [...dialog!.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent === (confirmed ? "移除节点" : "取消"));
      expect(action).toBeDefined();
      action!.click();
      await Promise.resolve();
    });
    if (confirmed) {
      expect(remove).toHaveBeenCalledExactlyOnceWith("local-node");
      expect(mutate).toHaveBeenCalled();
    } else {
      expect(remove).not.toHaveBeenCalled();
    }
  });

  it("offers guided REALITY with a hierarchical connection hostname", async () => {
    const data = realityDashboard();
    vi.spyOn(api, "latestApplicationCommand").mockRejectedValue(new APIError("not found", 404, "not_found"));
    vi.spyOn(api, "regions").mockResolvedValue({ regions: [{ code: "US", nameZh: "美国", prefix: "🇺🇸 美国" }] });
    vi.spyOn(api, "agentRegionSuggestion").mockResolvedValue({ agentId: "agent", publicAddress: "203.0.113.10", regionCode: "US", prefix: "🇺🇸 美国", source: "configured_helper" });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    await openRealityCreation(container);
		expect(document.body.textContent).toContain("填写节点名称和套餐，再选择当前节点可用的连接目标");
    expect(document.querySelector<HTMLInputElement>("#reality-name")?.value).toBe("home-server");
    expect(document.body.textContent).toContain("🇺🇸 美国｜home-server");
    expect(document.querySelector<HTMLInputElement>("#reality-client-name")?.value).toBe("我的设备");
    expect(document.body.textContent).toContain("VPS 月流量套餐");
    expect(document.body.textContent).toContain("客户端额度（可选）");
    expect(document.querySelector<HTMLInputElement>("#reality-inbound-quota")).not.toBeNull();
    expect(document.querySelector<HTMLInputElement>("#reality-subscription-quota")).not.toBeNull();
		expect(document.querySelector<HTMLInputElement>("#reality-hostname")?.value).toBe("");
		expect(document.body.textContent).toContain("home-server");
		expect(document.body.textContent).toContain("查找可用目标");
    expect(document.body.textContent).toContain("REALITY 回落目标（必填）");
		expect(document.querySelector<HTMLInputElement>("#reality-target-host")?.value).toBe("");
		expect(document.querySelector<HTMLInputElement>("#reality-server-name")?.value).toBe("");
    expect([...document.querySelectorAll("button")].find((button) => button.textContent?.includes("创建节点"))?.disabled).toBe(true);
  });

  it("offers an approved NAT-mapped node-direct entry for REALITY", async () => {
    const data = realityDashboard();
    data.agents[0].networkProfile = { ...data.agents[0].networkProfile!, publicBindAddress: "10.0.0.10", publicMode: "nat" };
    vi.spyOn(api, "latestApplicationCommand").mockRejectedValue(new APIError("not found", 404, "not_found"));
    vi.spyOn(api, "agentRegionSuggestion").mockResolvedValue({ agentId: "agent", publicAddress: "203.0.113.10", regionCode: "US", prefix: "🇺🇸 美国", source: "configured_helper" });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);

    await openRealityCreation(container);

		expect(document.body.textContent).toContain("home-server");
  });

  it("offers a separate one-click public 3x-ui subscription", async () => {
    const data = realityDashboard();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, status: "configured" }];
    data.services = [{ id: "subscription", applicationId: "three-x-ui", siteId: "site", name: "subscription", protocol: "http", containerPort: 2096, hostPort: 2096, endpoint: "10.0.0.10:2096", source: "catalog", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    vi.spyOn(api, "latestApplicationCommand").mockRejectedValue(new APIError("not found", 404, "not_found"));
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    expect([...container.querySelectorAll("button")].some((button) => button.textContent?.trim() === "添加入口")).toBe(false);
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("开启订阅"))?.click();
      await Promise.resolve();
    });
    expect(document.body.textContent).toContain("公网订阅");
    expect(document.body.textContent).toContain("迁移完成前，Vastora 仍会把公网域名同步到旧订阅服务");
    expect(document.querySelector<HTMLInputElement>("#subscription-hostname")?.value).toBe("");
    expect(document.querySelector<HTMLInputElement>("#subscription-hostname")?.placeholder).toBe("留空时自动生成");
    expect(document.querySelector<HTMLButtonElement>("#subscription-kind")?.textContent).toContain("Cloudflare Tunnel");
  });

  it("resynchronizes an existing public subscription without replacing its address", async () => {
    const data = realityDashboard();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, status: "configured" }];
    data.services = [{ id: "subscription", applicationId: "three-x-ui", siteId: "site", name: "subscription", protocol: "http", containerPort: 2096, hostPort: 2096, endpoint: "10.0.0.10:2096", source: "catalog", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    data.publications = [{ id: "subscription-publication", serviceId: "subscription", kind: "cloudflare_tunnel", ingress: { owner: "tunnel_connector", entryNodeId: "agent" }, hostname: "subscription.example.test", dnsProvider: "cloudflare", tlsEnabled: true, desiredRevision: 2, appliedRevision: 2, status: "ready", accessUrl: "https://subscription.example.test/sub/", createdAt: "2026-08-24T00:00:00Z", updatedAt: "2026-08-24T00:00:01Z" }];
    vi.spyOn(api, "latestApplicationCommand").mockRejectedValue(new APIError("not found", 404, "not_found"));
    const create = vi.spyOn(api, "createSubscriptionCommand").mockResolvedValue({ id: "subscription-resync", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.subscription.configure", state: "succeeded", hostname: "subscription.example.test", dnsProvider: "cloudflare", publicationId: "subscription-publication", resultAvailable: false, createdAt: "2026-08-24T00:00:02Z", updatedAt: "2026-08-24T00:00:03Z" });
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} />);
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("公网订阅"))?.click();
      await Promise.resolve();
    });
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("同步订阅设置"))?.click();
      await Promise.resolve();
    });
    expect(create).toHaveBeenCalledWith({ applicationId: "three-x-ui", gatewayNodeId: "agent", hostname: "subscription.example.test", kind: "cloudflare_tunnel", dnsProvider: "cloudflare" });
  });

  it("turns a completed subscription command into an actionable access check", async () => {
    const data = realityDashboard();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, status: "configured" }];
    data.services = [{ id: "subscription", applicationId: "three-x-ui", siteId: "site", name: "subscription", protocol: "http", containerPort: 2096, hostPort: 2096, endpoint: "10.0.0.10:2096", source: "catalog", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    const publication = { id: "subscription-publication", serviceId: "subscription", kind: "cloudflare_tunnel" as const, ingress: { owner: "tunnel_connector" as const, entryNodeId: "agent" }, hostname: "subscription.example.test", dnsProvider: "cloudflare" as const, tlsEnabled: true, desiredRevision: 2, appliedRevision: 2, status: "applying" as const, createdAt: "2026-08-24T00:00:00Z", updatedAt: "2026-08-24T00:00:01Z" };
    data.publications = [publication];
    vi.spyOn(api, "latestApplicationCommand").mockResolvedValue({ id: "subscription-command", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.subscription.configure", state: "succeeded", hostname: publication.hostname, dnsProvider: "cloudflare", publicationId: publication.id, resultAvailable: false, createdAt: "2026-08-24T00:00:00Z", updatedAt: "2026-08-24T00:00:01Z" });
    const verify = vi.spyOn(api, "verifyPublication").mockResolvedValue({ ...publication, status: "ready", accessUrl: `https://${publication.hostname}/` });
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} />);

    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("公网订阅"))?.click();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(document.body.textContent).toContain("订阅已发布，入口尚未确认");
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
    data.publications = [{ id: "subscription-publication", serviceId: "subscription", kind: "cloudflare_tunnel", ingress: { owner: "tunnel_connector", entryNodeId: "agent" }, hostname: "subscription.example.test", dnsProvider: "cloudflare", tlsEnabled: true, desiredRevision: 2, appliedRevision: 2, status: "degraded", lastError: "TLS health check failed", createdAt: "2026-08-24T00:00:00Z", updatedAt: "2026-08-24T00:00:01Z" }];
    vi.spyOn(api, "latestApplicationCommand").mockResolvedValue({ id: "subscription-command", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.subscription.configure", state: "succeeded", hostname: "subscription.example.test", dnsProvider: "cloudflare", publicationId: "subscription-publication", resultAvailable: false, createdAt: "2026-08-24T00:00:00Z", updatedAt: "2026-08-24T00:00:01Z" });
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);

    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("公网订阅"))?.click();
      await Promise.resolve();
    });

    expect(document.body.textContent).toContain("订阅入口需要处理");
    expect(document.body.textContent).toContain("TLS health check failed");
    expect([...document.querySelectorAll("button")].some((button) => button.textContent?.includes("重试配置"))).toBe(true);
  });

  it("automatically installs later Vastora Proxy instances as Xray-only nodes", async () => {
    const data = realityDashboard();
    data.agents.push({ ...data.agents[0], id: "worker", name: "edge-worker", networkProfile: { serviceAddress: "100.64.0.20", headscaleAddress: "100.64.0.20", enabledKinds: ["headscale"], directPublic: false } });
    const create = vi.spyOn(api, "createDeployment").mockResolvedValue({ id: "worker-deployment", agentId: "worker", appKey: "vastora-official/3x-ui", appVersion: "3.7.0", state: "pending", operation: "install", deleteData: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" });
    const mutate = async (operation: () => Promise<unknown>) => { await operation(); };
    const container = render(<AppsView data={data} language="zh-CN" mutate={mutate} />);
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("应用商店"))?.click());
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.trim() === "安装")?.click());
    expect(document.body.textContent).toContain("将作为 Xray 节点");
    expect(document.body.textContent).toContain("只运行 Xray，不创建面板或独立订阅地址");
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("开始安装"))?.click();
      await Promise.resolve();
    });
    expect(create).toHaveBeenCalledWith("worker", "vastora-official/3x-ui", {}, "install", false, "worker", "", undefined, [], { packageRevision: 1, manifestSha256: "a".repeat(64) });
  });

		it("groups the global controller and cross-Site workers into compact rows without duplicate installations", () => {
    const data = realityDashboard();
    data.agents.push({ ...data.agents[0], id: "worker", name: "edge-worker" });
    data.applications.push({ ...data.applications[0], id: "three-x-ui-worker", nodeId: "worker", role: "worker", controllerApplicationId: "three-x-ui", nodeSyncStatus: "ready" });
    data.services.push(
      { id: "controller-inbound", applicationId: "three-x-ui", siteId: "site", name: "inbound-1", protocol: "tcp", containerPort: 30443, hostPort: 30443, endpoint: "10.0.0.10:30443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" },
      { id: "worker-inbound", applicationId: "three-x-ui-worker", siteId: "site", name: "inbound-2", protocol: "tcp", containerPort: 31443, hostPort: 31443, endpoint: "10.0.0.20:31443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" }
    );
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    expect(container.textContent).toContain("订阅主机");
    expect(container.textContent).toContain("2 个线路机 · 1 台订阅主机");
    expect(container.querySelector('[data-app-group]')?.tagName).toBe("SECTION");
    expect(container.querySelectorAll("[data-app-group]")).toHaveLength(1);
    expect(container.querySelectorAll("[data-application-id]")).toHaveLength(2);
    expect(container.querySelector("[data-application-id]")?.getAttribute("data-application-id")).toBe("three-x-ui");
    expect([...container.querySelectorAll("button")].filter((button) => button.textContent?.includes("客户端与订阅"))).toHaveLength(1);
    expect(container.textContent).not.toContain("VLESS 节点已配置");
    expect(container.textContent).not.toContain("修改配置");
    expect(container.textContent).not.toContain("卸载");
    expect(container.textContent).not.toContain("10.0.0.20:31443");
    expect(container.querySelector('[data-slot="subscription-controller"]')?.textContent).toContain("客户端与订阅");

    openAppDetails(container, "three-x-ui-worker");
    const details = document.querySelector('[data-slot="sheet-content"]');
    expect(details?.textContent).toContain("edge-worker");
	    expect(details?.textContent).toContain("客户端和订阅由全局订阅主机统一管理");
    expect(details?.textContent).not.toContain("管理客户端");
		expect(details?.textContent).not.toContain("管理账号");
	});

  it("keeps ingress failures visible independently from the running app and checks the correct entry", async () => {
    const data = realityDashboard();
    data.services = [{ id: "reality", applicationId: "three-x-ui", siteId: "site", name: "inbound-1", protocol: "tcp", containerPort: 443, hostPort: 30443, endpoint: "10.0.0.10:30443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    const publication = { id: "failed-entry", serviceId: "reality", kind: "public_shared_443" as const, ingress: { owner: "application_node" as const, entryNodeId: "agent" }, hostname: "node.example.com", dnsProvider: "cloudflare" as const, tlsEnabled: false, desiredRevision: 2, appliedRevision: 1, status: "pending" as const, lastError: "Listener unavailable", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" };
    data.publications = [publication];
    const check = vi.spyOn(api, "verifyPublication").mockResolvedValue({ ...publication, status: "ready", lastError: undefined });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} />);
    const row = container.querySelector('[data-application-id="three-x-ui"]')!;
    expect(row.textContent).toContain("运行中");
    expect(row.textContent).toContain("入口待处理");
    expect(container.textContent).toContain("1 个入口待处理");
    expect(container.textContent).not.toContain("node.example.com");
    await act(async () => {
      [...row.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === "检查")?.click();
      await Promise.resolve();
    });
    expect(check).toHaveBeenCalledWith("failed-entry");

    openAppDetails(container);
    expect(document.body.textContent).toContain("Listener unavailable");
    expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(1);
  });

  it("runs a managed REALITY security check from Center and labels same-host results", async () => {
    const data = realityDashboard();
    data.services = [{ id: "reality", applicationId: "three-x-ui", siteId: "site", name: "inbound-1", protocol: "tcp", containerPort: 443, hostPort: 30443, endpoint: "10.0.0.10:30443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    data.publications = [{
      id: "reality-entry", serviceId: "reality", kind: "public_shared_443", ingress: { owner: "application_node", entryNodeId: "agent" }, hostname: "node.example.com", sniHostname: "www.intel.com", dnsProvider: "cloudflare", tlsEnabled: false, desiredRevision: 2, appliedRevision: 2, status: "ready",
      securityCheck: { status: "safe", scope: "same_host", checkedAt: "2026-08-18T00:00:00Z", checks: [
        { kind: "expected_fallback", status: "passed", reason: "expected_fallback_verified" },
        { kind: "openai_sni", status: "passed", reason: "unauthorized_destination_rejected" },
        { kind: "cloudflare_sni", status: "passed", reason: "unauthorized_destination_rejected" },
        { kind: "random_sni", status: "passed", reason: "unauthorized_destination_rejected" },
        { kind: "no_sni", status: "passed", reason: "unauthorized_destination_rejected" },
      ] },
      createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z",
    }];
    const check = vi.spyOn(api, "checkRealitySecurity").mockResolvedValue(data.publications[0].securityCheck!);
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} />);

    expect(container.textContent).toContain("本机检查通过");
    expect(container.textContent).toContain("不代表外部网络");
    await act(async () => {
      [...container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("安全检查"))?.click();
      await Promise.resolve();
    });
    expect(check).toHaveBeenCalledWith("reality-entry");
  });

  it("uses the ready panel entry for the controller shortcut and hides unavailable links", () => {
    const data = realityDashboard();
    data.services = [{ id: "panel", applicationId: "three-x-ui", siteId: "site", name: "panel", protocol: "http", containerPort: 2053, hostPort: 2053, endpoint: "10.0.0.10:2053", source: "catalog", management: true, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    const publication = { id: "panel-entry", serviceId: "panel", kind: "cloudflare_tunnel" as const, ingress: { owner: "tunnel_connector" as const, entryNodeId: "agent" }, hostname: "panel.example.com", dnsProvider: "cloudflare" as const, tlsEnabled: true, desiredRevision: 2, appliedRevision: 2, status: "ready" as const, accessUrl: "https://panel.example.com/", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" };
    data.publications = [publication];
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    const panelLink = container.querySelector<HTMLAnchorElement>('[data-slot="subscription-controller"] a');
    expect(panelLink?.textContent).toContain("打开面板");
    expect(panelLink?.getAttribute("href")).toBe("https://panel.example.com/");
    expect(panelLink?.getAttribute("role")).not.toBe("button");

    const nextData = { ...data, publications: [{ ...publication, status: "failed" as const, lastError: "Origin unavailable" }] };
    rerender(<AppsView data={nextData} language="zh-CN" mutate={async () => undefined} />);
    expect(container.querySelector('[data-slot="subscription-controller"] a')).toBeNull();
    expect(container.querySelector('[data-slot="subscription-controller"]')?.textContent).toContain("入口待处理");
  });

  it("refreshes an open management sheet by application ID and closes it after uninstall", () => {
    const data = dashboard();
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    openAppDetails(container, "running");
    const nextData = { ...data, applications: [{ ...data.applications[0], installedVersion: "1.2.61", availableVersion: "1.2.61" }] };
    rerender(<AppsView data={nextData} language="zh-CN" mutate={async () => undefined} />);
    expect(document.querySelector('[data-slot="sheet-content"]')?.textContent).toContain("v1.2.61");
    expect(document.querySelector('[data-slot="sheet-content"]')?.textContent).not.toContain("v1.2.60");

    rerender(<AppsView data={{ ...nextData, applications: [] }} language="zh-CN" mutate={async () => undefined} />);
    expect(document.querySelector('[data-slot="sheet-content"]')).toBeNull();
    expect(container.textContent).toContain("还没有安装应用");
  });

	it("renames an existing REALITY node from Center", async () => {
		vi.useFakeTimers();
		const data = realityDashboard();
		data.services = [{ id: "reality-service", applicationId: "three-x-ui", siteId: "site", name: "inbound-9", displayName: "🇺🇸 美国Old name", regionCode: "US", protocol: "tcp", containerPort: 30443, hostPort: 30443, endpoint: "10.0.0.10:30443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" }];
		const pending: ApplicationCommand = { id: "rename-command", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.reality.rename", state: "pending", hostname: "", dnsProvider: "manual", action: "rename", regionCode: "US", displayName: "🇺🇸 美国｜Provider A", inboundId: 9, resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" };
		vi.spyOn(api, "regions").mockResolvedValue({ regions: [{ code: "US", nameZh: "美国", prefix: "🇺🇸 美国" }] });
		const rename = vi.spyOn(api, "renameRealityCommand").mockResolvedValue(pending);
		vi.spyOn(api, "nodeProtocols").mockResolvedValue({ vless: true, hy2: false, state: "succeeded" });
		mockCommandEvent({ ...pending, state: "succeeded", updatedAt: "2026-08-23T00:00:01Z" });
		const mutate = vi.fn(async (operation: () => Promise<unknown>) => { await operation(); });
		const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={mutate} />);
		expect(container.textContent).toContain("🇺🇸 美国Old name");
		act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("编辑节点"))?.click());
		const input = document.querySelector<HTMLInputElement>("#reality-rename-name")!;
		act(() => {
			Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(input, "Provider A");
			input.dispatchEvent(new Event("input", { bubbles: true }));
		});
		await act(async () => {
			[...document.querySelectorAll("button")].find((button) => button.textContent?.includes("保存名称"))?.click();
			await Promise.resolve();
		});
		expect(rename).toHaveBeenCalledWith("reality-service", "US", "Provider A");
		await act(async () => {
			vi.advanceTimersByTime(1300);
			await Promise.resolve();
			await Promise.resolve();
		});
		expect(document.body.textContent).toContain("现在显示为“🇺🇸 美国｜Provider A”");
		expect(mutate).toHaveBeenCalled();
	});

	it("offers a guided manual subscription-host migration", () => {
    const data = realityDashboard();
    data.agents.push({ ...data.agents[0], id: "worker", name: "edge-worker", connected: true });
    data.applications.push({ ...data.applications[0], id: "three-x-ui-worker", nodeId: "worker", role: "worker", controllerApplicationId: "three-x-ui", nodeSyncStatus: "ready" });
    data.threeXUIControllerMigrations.push({ id: "migration", kind: "replace", siteId: "site", sourceApplicationId: "three-x-ui", targetApplicationId: "three-x-ui-worker", backupRevision: 2, state: "backing_up", step: "backup", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:01Z" });
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("迁移订阅主机"))?.click());
    expect(document.body.textContent).toContain("正在安全迁移");
    expect(document.body.textContent).toContain("保存最新配置");
    expect(document.body.textContent).toContain("恢复到新主机");
    expect(document.body.textContent).toContain("替换为 Xray 节点");
    expect(document.body.textContent).toContain("同步节点拓扑");
  });

  it("manages 3x-ui clients and reveals links without opening the panel", async () => {
    const data = realityDashboard();
    const baseCommand: ApplicationCommand = { id: "client-command-list", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.clients.manage", state: "succeeded", hostname: "", dnsProvider: "manual", action: "list", clients: [{ email: "MacBook", enabled: true, totalBytes: 10 * 1024 ** 3, usedBytes: 1024, expiryTime: 0, resetDays: 0, limitIp: 2, inboundIds: [9], hasSubscription: true }], clientsObserved: true, inbounds: [{ id: 9, serviceId: "reality-service", name: "inbound-9", nodeName: "edge-worker", connectHostname: "reality.example.test", totalBytes: 200 * 1024 ** 3, usedBytes: 12 * 1024 ** 3, resetDay: 22, nextResetAt: "2026-09-22T00:00:00Z" }, { id: 10, serviceId: "reality-service-2", name: "inbound-10", nodeName: "oracle-worker", connectHostname: "reality.oracle.example.test", totalBytes: 0, usedBytes: 0, resetDay: 0 }], inboundsObserved: true, subscriptionAvailable: true, resultAvailable: false, createdAt: "2026-08-22T00:00:00Z", updatedAt: "2026-08-22T00:00:01Z" };
    const create = vi.spyOn(api, "createThreeXUIClientCommand").mockImplementation(async (input) => input.action.startsWith("reveal_") ? { ...baseCommand, id: `client-command-${input.action}`, action: input.action, resultAvailable: true } : baseCommand);
    const reveal = vi.spyOn(api, "revealApplicationCommand").mockImplementation(async (id) => ({ shareUri: id.includes("subscription") ? "https://subscription.example.test/sub/client-id" : "vless://one-time-client-link" }));
    const acknowledge = vi.spyOn(api, "acknowledgeApplicationCommand").mockResolvedValue({ acknowledged: true });
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("客户端与订阅"))?.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(create).toHaveBeenCalledWith({ applicationId: "three-x-ui", action: "list" });
    expect(document.body.textContent).toContain("MacBook");
    expect(document.body.textContent).toContain("已接入 1 个节点");
    expect(document.querySelector<HTMLDetailsElement>('details:has([aria-label="已接入节点"])')?.open).toBe(false);
    expect(document.querySelector('[aria-label="已接入节点"]')?.textContent).toContain("edge-worker");
    expect(document.body.textContent).toContain("已用流量");
    expect(document.body.textContent).toContain("管理 Vastora Proxy 客户端");
    act(() => [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("编辑") || button.querySelector(".sr-only")?.textContent === "编辑")?.click());
    expect(document.body.textContent).toContain("客户端额度（可选）");
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
    await act(async () => { document.querySelector<HTMLButtonElement>('button[aria-label="更多操作：MacBook"]')?.click(); });
    await act(async () => {
      [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((item) => item.textContent?.includes("复制 VLESS"))?.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(reveal).toHaveBeenCalledWith("client-command-reveal_link", expect.any(String));
    expect(writeText).toHaveBeenCalledWith("vless://one-time-client-link");
    expect(document.body.textContent).toContain("vless://one-time-client-link");
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("我已保存"))?.click();
      await Promise.resolve();
    });
    expect(acknowledge).toHaveBeenCalledWith("client-command-reveal_link", expect.any(String));
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
    await act(async () => { document.querySelector<HTMLButtonElement>('button[aria-label="更多操作：MacBook"]')?.click(); });
    await act(async () => { [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((item) => item.textContent?.includes("重置流量"))?.click(); });
    expect(document.body.textContent).toContain("所有 VLESS 节点上的合计用量清零");
    expect(create.mock.calls.filter(([input]) => input.action === "reset_traffic")).toHaveLength(resetCallsBeforeConfirmation);
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("确认重置"))?.click();
      await Promise.resolve();
    });
    expect(create).toHaveBeenCalledWith({ applicationId: "three-x-ui", action: "reset_traffic", email: "MacBook" });
    await act(async () => { document.querySelector<HTMLButtonElement>('button[aria-label="更多操作：MacBook"]')?.click(); });
    await act(async () => { [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((item) => item.textContent?.includes("删除客户端"))?.click(); });
    expect(document.body.textContent).toContain("删除“MacBook”？");
    expect(create.mock.calls.some(([input]) => input.action === "delete")).toBe(false);
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
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("客户端与订阅"))?.click();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(document.body.textContent).toContain("MacBook");
    expect(document.body.textContent).toContain("正在后台刷新");
    expect(document.body.textContent).toContain("2026年8月24日");
  });

	it("recovers an unacknowledged client link even after a newer list command", async () => {
		const data = realityDashboard();
		const listCommand: ApplicationCommand = { id: "newer-client-list", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.clients.manage", state: "succeeded", hostname: "", dnsProvider: "manual", action: "list", clients: [{ email: "MacBook", enabled: true, totalBytes: 0, usedBytes: 0, expiryTime: 0, resetDays: 0, limitIp: 0, inboundIds: [9], hasSubscription: true }], clientsObserved: true, inbounds: [{ id: 9, serviceId: "reality-service", name: "inbound-9", nodeName: "edge-worker", connectHostname: "reality.example.test" }], inboundsObserved: true, subscriptionAvailable: true, resultAvailable: false, createdAt: "2026-08-23T00:00:02Z", updatedAt: "2026-08-23T00:00:03Z" };
		const secretCommand: ApplicationCommand = { ...listCommand, id: "unacknowledged-client-link", action: "reveal_link", resultAvailable: true, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:01Z" };
		const operationKey = secretOperation(commandSecretScope("three-x-ui", secretCommand.id));
		vi.spyOn(api, "latestApplicationCommand").mockResolvedValue(listCommand);
		vi.spyOn(api, "createThreeXUIClientCommand").mockResolvedValue(listCommand);
		vi.spyOn(api, "applicationCommand").mockResolvedValue(secretCommand);
		const reveal = vi.spyOn(api, "revealApplicationCommand").mockResolvedValue({ shareUri: "vless://recovered-after-refresh" });
		const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);

		await act(async () => {
			[...container.querySelectorAll("button")].find((button) => button.textContent?.includes("客户端与订阅"))?.click();
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
		});

		expect(reveal).toHaveBeenCalledWith(secretCommand.id, operationKey);
		expect(document.body.textContent).toContain("vless://recovered-after-refresh");
	});

  it("offers browser-trusted HTTPS only when Cloudflare is connected", () => {
    const data = dashboard();
    data.sites[0].domainSuffix = "vastora.example.com";
    data.services = [{ id: "manager", applicationId: "running", siteId: "site", name: "manager", protocol: "http", containerPort: 8317, hostPort: 8317, endpoint: "192.168.1.2:8317", source: "catalog", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    let container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加入口"))?.click());
    expect(document.body.textContent).toContain("连接 Cloudflare 后可以开启");
    expect(document.querySelector<HTMLElement>('[role="switch"][aria-label="使用 HTTPS"]')?.getAttribute("aria-disabled")).toBe("true");

    resetRender();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, status: "configured" }];
    container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("添加入口"))?.click());
    expect(document.body.textContent).toContain("使用 Cloudflare DNS 验证申请可信证书");
    const tlsSwitch = document.querySelector<HTMLElement>('[role="switch"][aria-label="使用 HTTPS"]');
    expect(tlsSwitch?.getAttribute("aria-disabled")).not.toBe("true");
    expect(tlsSwitch?.getAttribute("aria-checked")).toBe("true");
  });

  it("shows the complete publication URL on its dedicated hostname", () => {
    const data = dashboard();
    data.services = [{ id: "manager", applicationId: "running", siteId: "site", name: "manager", protocol: "http", containerPort: 8317, hostPort: 8317, endpoint: "192.168.1.2:8317", source: "catalog", management: true, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    const accessUrl = "https://komari-agent-home-server.example.com/";
    data.publications = [{ id: "public-panel", serviceId: "manager", kind: "cloudflare_tunnel", ingress: { owner: "tunnel_connector", entryNodeId: "agent" }, hostname: "komari-agent-home-server.example.com", dnsProvider: "cloudflare", tlsEnabled: true, desiredRevision: 1, appliedRevision: 1, status: "ready", accessUrl, createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];

    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);

    const address = container.querySelector<HTMLElement>(`[title="${accessUrl}"]`);
    expect(address?.textContent).toBe(accessUrl);
  });

  it("upgrades an existing private HTTP access point from its HTTPS switch", async () => {
    const data = dashboard();
    data.integrations = [{ kind: "cloudflare", mode: "oauth", endpoint: "example.com", accountId: "account", zoneId: "zone", secretSet: true, status: "configured" }];
    data.services = [{ id: "manager", applicationId: "running", siteId: "site", name: "manager", protocol: "http", containerPort: 8317, hostPort: 8317, endpoint: "192.168.1.2:8317", source: "catalog", management: false, status: "ready", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    data.publications = [{ id: "private-panel", serviceId: "manager", kind: "headscale_gateway", ingress: { owner: "site_gateway", entryNodeId: "agent" }, hostname: "panel.home.example", dnsProvider: "headscale", tlsEnabled: false, desiredRevision: 1, appliedRevision: 1, status: "ready", accessUrl: "http://panel.home.example/", createdAt: "2026-08-18T00:00:00Z", updatedAt: "2026-08-18T00:00:00Z" }];
    const update = vi.spyOn(api, "updatePublicationTLS").mockResolvedValue({ ...data.publications[0], tlsEnabled: true });
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} />);

    expect(container.textContent).toContain("安全私网 · Site Gateway · HTTP");
    const tlsSwitch = container.querySelector<HTMLElement>('[role="switch"][aria-label="开启 HTTPS"]');
    expect(tlsSwitch?.getAttribute("aria-label")).toBe("开启 HTTPS");
    await act(async () => { tlsSwitch?.click(); await Promise.resolve(); });
    expect(update).toHaveBeenCalledWith("private-panel", true);
  });

  it.each([
    { nodeAsn: 64500, targetAsn: 64500, message: "同 ASN 仅作选站参考" },
    { nodeAsn: 64500, targetAsn: 64501, message: "节点与目标 ASN 不同，仅作选站提示" },
    { nodeAsn: 0, targetAsn: 0, message: "节点 未知 · 目标 未知" },
    { nodeAsn: 64500, targetAsn: 0, message: "节点 AS64500 · 目标 未知" },
  ])("shows advisory REALITY ASNs without blocking ready results: $nodeAsn/$targetAsn", async ({ nodeAsn, targetAsn, message }) => {
    const data = realityDashboard();
    vi.spyOn(api, "latestApplicationCommand").mockResolvedValue({ id: "asn-advisory", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.reality.create", state: "succeeded", hostname: "reality.example.com", dnsProvider: "manual", targetHost: "www.example.com", targetIp: "203.0.113.10", serverName: "www.example.com", nodeAsn, targetAsn, guardStatus: "ready", resultAvailable: false, createdAt: "2026-08-20T00:00:00Z", updatedAt: "2026-08-20T00:00:01Z" });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    await openRealityCreation(container);
    expect(document.body.textContent).toContain("回落目标限制已启用");
    expect(document.body.textContent).toContain(message);
    expect(document.body.textContent).toContain("未认证连接仍可能访问此回落网站并消耗流量");
    expect(document.body.textContent).not.toContain("AS0");
    expect(document.body.textContent).not.toContain("防盗保护已启用");
  });

	it("reveals a REALITY client link only after explicit confirmation", async () => {
    const data = realityDashboard();
	    vi.spyOn(api, "latestApplicationCommand").mockResolvedValue({ id: "application-command-1", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.reality.create", state: "succeeded", hostname: "reality.home-server.home.vastora.example.com", dnsProvider: "manual", targetHost: "www.example.com", targetIp: "203.0.113.10", serverName: "www.example.com", targetAsn: 64500, guardStatus: "ready", clientCreated: true, resultAvailable: true, createdAt: "2026-08-20T00:00:00Z", updatedAt: "2026-08-20T00:00:01Z" });
	    const reveal = vi.spyOn(api, "revealApplicationCommand").mockResolvedValue({ shareUri: "vless://one-time-client-link" });
	    const acknowledge = vi.spyOn(api, "acknowledgeApplicationCommand").mockResolvedValue({ acknowledged: true });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    await openRealityCreation(container);
    expect(reveal).not.toHaveBeenCalled();
    const revealButton = [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("显示客户端链接"));
    await act(async () => {
      revealButton?.click();
      await Promise.resolve();
    });
	const operationKey = reveal.mock.calls[0]?.[1];
	expect(operationKey).toEqual(expect.any(String));
	expect(reveal).toHaveBeenCalledWith("application-command-1", operationKey);
		expect(document.body.textContent).toContain("vless://one-time-client-link");
	await act(async () => {
		[...document.querySelectorAll("button")].find((button) => button.textContent?.includes("我已保存"))?.click();
		await Promise.resolve();
	});
	expect(acknowledge).toHaveBeenCalledWith("application-command-1", operationKey);
	expect(document.body.textContent).not.toContain("vless://one-time-client-link");
	});

	it("keeps the one-time REALITY link available when only public access fails", async () => {
		const data = realityDashboard();
		vi.spyOn(api, "latestApplicationCommand").mockResolvedValue({ id: "application-command-degraded", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.reality.create", state: "succeeded", hostname: "reality.home-server.home.vastora.example.com", dnsProvider: "cloudflare", displayName: "🇺🇸 美国Edge", targetHost: "www.example.com", targetIp: "203.0.113.10", serverName: "www.example.com", targetAsn: 64500, guardStatus: "ready", clientCreated: true, error: "center: create REALITY access entry: SNI conflict", resultAvailable: true, createdAt: "2026-08-20T00:00:00Z", updatedAt: "2026-08-20T00:00:01Z" });
		const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
		await openRealityCreation(container);
		expect(document.body.textContent).toContain("REALITY 已创建，公网入口待处理");
		expect(document.body.textContent).toContain("客户端凭据已安全保留");
		expect([...document.querySelectorAll("button")].some((button) => button.textContent?.includes("显示客户端链接"))).toBe(true);
	});

	it("returns to the REALITY form after a previous creation failed", async () => {
		const data = realityDashboard();
		vi.spyOn(api, "latestApplicationCommand").mockResolvedValue({ id: "failed-reality", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.reality.create", state: "failed", hostname: "reality.failed.example.test", dnsProvider: "manual", action: "create", error: "node plan rejected", resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:01Z" });
		const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
		await openRealityCreation(container);
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
		await openRealityCreation(container);
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
		await openRealityCreation(container);

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
		vi.spyOn(api, "agentRegionSuggestion").mockResolvedValue({ agentId: "agent", publicAddress: "203.0.113.10", regionCode: "US", prefix: "🇺🇸 美国", source: "configured_helper" });
		const pending: ApplicationCommand = { id: "create-reality", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.reality.create", state: "pending", hostname: "reality.home-server.home.vastora.example.com", dnsProvider: "manual", action: "create", regionCode: "US", displayName: "🇺🇸 美国｜Provider A", resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" };
		const verify = vi.spyOn(api, "verifyRealityTarget").mockResolvedValue({ id: "verify-reality", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.reality.verify", state: "succeeded", hostname: "", dnsProvider: "manual", targetHost: "www.example.com", targetIp: "203.0.113.20", serverName: "www.example.com", nodeAsn: 64500, targetAsn: 64501, tls13: true, x25519: true, h2: true, certificateValid: true, resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" });
		const create = vi.spyOn(api, "createRealityCommand").mockResolvedValue(pending);
		mockCommandEvent(pending);
		const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
		await openRealityCreation(container);
		const nodeName = document.querySelector<HTMLInputElement>("#reality-name")!;
		expect(nodeName.value).toBe("home-server");
		expect(document.querySelector<HTMLInputElement>("#reality-client-name")?.value).toBe("我的设备");
		act(() => {
			Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(nodeName, "Provider A");
			nodeName.dispatchEvent(new Event("input", { bubbles: true }));
			const targetHost = document.querySelector<HTMLInputElement>("#reality-target-host")!;
			Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(targetHost, "www.example.com");
			targetHost.dispatchEvent(new Event("input", { bubbles: true }));
			const serverName = document.querySelector<HTMLInputElement>("#reality-server-name")!;
			Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(serverName, "www.example.com");
			serverName.dispatchEvent(new Event("input", { bubbles: true }));
		});
		await act(async () => {
			[...document.querySelectorAll("button")].find((button) => button.textContent?.includes("检查此目标"))?.click();
			await Promise.resolve();
			await Promise.resolve();
		});
		await act(async () => {
			[...document.querySelectorAll("button")].find((button) => button.textContent?.includes("创建节点"))?.click();
			await Promise.resolve();
			await Promise.resolve();
		});
		expect(verify).toHaveBeenCalledWith("three-x-ui", "www.example.com", "www.example.com");
		expect(create).toHaveBeenCalledWith({ applicationId: "three-x-ui", verificationId: "verify-reality", targetIp: "203.0.113.20", regionCode: "US", name: "Provider A", clientName: "我的设备", hostname: "", dnsProvider: "manual", targetHost: "www.example.com", serverName: "www.example.com", inboundTotalBytes: 0, inboundResetDay: 1, clientTotalBytes: 0, clientResetDays: 0, clientExpiryTime: 0 });
		});

  it("keeps subscriber quotas on the controller even when a worker creates the first VLESS node", async () => {
    const data = realityDashboard();
    data.sites[0].gatewayNodes.push("worker");
    data.agents.push({ ...data.agents[0], id: "worker", name: "oracle-worker", networkProfile: { ...data.agents[0].networkProfile!, serviceAddress: "10.0.0.20", publicAddress: "203.0.113.20" } });
    data.applications.push({ ...data.applications[0], id: "three-x-ui-worker", nodeId: "worker", role: "worker", controllerApplicationId: "three-x-ui", nodeSyncStatus: "ready" });
    vi.spyOn(api, "latestApplicationCommand").mockRejectedValue(new APIError("not found", 404, "not_found"));
    vi.spyOn(api, "regions").mockResolvedValue({ regions: [{ code: "US", nameZh: "美国", prefix: "🇺🇸 美国" }] });
    vi.spyOn(api, "agentRegionSuggestion").mockResolvedValue({ agentId: "worker", publicAddress: "203.0.113.20", regionCode: "US", prefix: "🇺🇸 美国", source: "configured_helper" });
    const verify = vi.spyOn(api, "verifyRealityTarget").mockResolvedValue({ id: "verify-worker-reality", applicationId: "three-x-ui-worker", gatewayNodeId: "worker", kind: "3xui.reality.verify", state: "succeeded", hostname: "", dnsProvider: "manual", targetHost: "www.example.com", targetIp: "203.0.113.30", serverName: "www.example.com", nodeAsn: 64500, targetAsn: 64500, tls13: true, x25519: true, h2: true, certificateValid: true, resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" });
    const create = vi.spyOn(api, "createRealityCommand").mockResolvedValue({ id: "worker-reality", applicationId: "three-x-ui-worker", gatewayNodeId: "worker", kind: "3xui.reality.create", state: "succeeded", hostname: "reality.oracle-worker.home.vastora.example.com", dnsProvider: "manual", action: "create", clientCreated: false, resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:01Z" });
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    await openRealityCreation(container, "three-x-ui-worker");
    expect(document.body.textContent).toContain("VPS 月流量套餐");
    expect(document.body.textContent).toContain("订阅额度在主订阅机管理");
    expect(document.body.textContent).toContain("如果还没有用户");
    expect(document.querySelector("#reality-client-name")).toBeNull();
    expect(document.querySelector("#reality-subscription-quota")).toBeNull();
    act(() => {
      const targetHost = document.querySelector<HTMLInputElement>("#reality-target-host")!;
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(targetHost, "www.example.com");
      targetHost.dispatchEvent(new Event("input", { bubbles: true }));
      const serverName = document.querySelector<HTMLInputElement>("#reality-server-name")!;
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(serverName, "www.example.com");
      serverName.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("检查此目标"))?.click();
      await Promise.resolve();
      await Promise.resolve();
     });
     await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("创建节点"))?.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(verify).toHaveBeenCalledWith("three-x-ui-worker", "www.example.com", "www.example.com");
    expect(create).toHaveBeenCalledWith({ applicationId: "three-x-ui-worker", verificationId: "verify-worker-reality", targetIp: "203.0.113.30", regionCode: "US", name: "oracle-worker", hostname: "", dnsProvider: "manual", targetHost: "www.example.com", serverName: "www.example.com", inboundTotalBytes: 0, inboundResetDay: 1 });
    expect(document.body.textContent).not.toContain("客户端链接只显示一次");
  });

  it("still bootstraps the first controller client after a worker REALITY node exists", async () => {
    const data = realityDashboard();
    data.services = [{ id: "worker-reality", applicationId: "three-x-ui-worker", siteId: "site", name: "inbound-90", displayName: "🇺🇸 美国Worker", protocol: "tcp", containerPort: 30443, hostPort: 30443, endpoint: "10.0.0.20:30443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" }];
    vi.spyOn(api, "latestApplicationCommand").mockRejectedValue(new APIError("not found", 404, "not_found"));
    const container = render(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    await openRealityCreation(container);
    expect(document.querySelector<HTMLInputElement>("#reality-client-name")?.value).toBe("我的设备");
    expect(document.querySelector("#reality-subscription-quota")).not.toBeNull();
  });

  it("edits an independent VLESS node plan from its service card", async () => {
    const data = realityDashboard();
    const service = { id: "reality-service", applicationId: "three-x-ui", siteId: "site", name: "inbound-9", displayName: "🇺🇸 美国Provider A", protocol: "tcp" as const, containerPort: 30443, hostPort: 30443, endpoint: "10.0.0.10:30443", source: "observed" as const, appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" };
    data.services = [service];
    const current: ApplicationCommand = { id: "traffic-list", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.clients.manage", state: "succeeded", hostname: "", dnsProvider: "manual", action: "list_inbounds", clients: [], clientsObserved: false, inbounds: [{ id: 9, serviceId: "reality-service", name: "inbound-9", displayName: "🇺🇸 美国Provider A", totalBytes: 200 * 1024 ** 3, usedBytes: 12 * 1024 ** 3, resetDay: 22, nextResetAt: "2026-09-22T00:00:00Z", planStatus: "active" }], inboundsObserved: true, resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:01Z" };
    const updated = { ...current, id: "traffic-update", action: "update_inbound" as const, inbounds: [{ ...current.inbounds![0], totalBytes: 300 * 1024 ** 3, resetDay: 31 }] };
    const command = vi.spyOn(api, "createThreeXUIClientCommand").mockImplementation(async (input) => input.action === "update_inbound" ? updated : current);
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("节点套餐"))?.click();
      await Promise.resolve();
    });
    expect(command).toHaveBeenCalledWith({ applicationId: "three-x-ui", action: "list_inbounds" });
    expect(document.body.textContent).toContain("VLESS 节点套餐");
    expect(document.body.textContent).toContain("200.0 GB");
    act(() => [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("修改节点套餐"))?.click());
    const quota = document.querySelector<HTMLInputElement>("#inbound-plan-quota")!;
    const resetDay = document.querySelector<HTMLInputElement>("#inbound-plan-reset-day")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(quota, "300");
      quota.dispatchEvent(new Event("input", { bubbles: true }));
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(resetDay, "31");
      resetDay.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("取消"))?.click());
    act(() => [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("修改节点套餐"))?.click());
    const restoredQuota = document.querySelector<HTMLInputElement>("#inbound-plan-quota")!;
    const restoredResetDay = document.querySelector<HTMLInputElement>("#inbound-plan-reset-day")!;
    expect(restoredQuota.value).toBe("200");
    expect(restoredResetDay.value).toBe("22");
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(restoredQuota, "300");
      restoredQuota.dispatchEvent(new Event("input", { bubbles: true }));
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(restoredResetDay, "31");
      restoredResetDay.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("保存套餐"))?.click();
      await Promise.resolve();
    });
    expect(command).toHaveBeenCalledWith({ applicationId: "three-x-ui", action: "update_inbound", serviceId: "reality-service", inboundId: 9, inboundTotalBytes: 300 * 1024 ** 3, inboundResetDay: 31 });
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
    data.services = [{ id: "reality-service", applicationId: "three-x-ui", siteId: "site", name: "inbound-9", displayName: "🇺🇸 美国Provider A", protocol: "tcp", containerPort: 30443, hostPort: 30443, endpoint: "10.0.0.10:30443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" }];
    const cached: ApplicationCommand = { id: "cached-traffic", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.clients.manage", state: "succeeded", hostname: "", dnsProvider: "manual", action: "list_inbounds", clients: [], clientsObserved: false, inbounds: [{ id: 9, serviceId: "reality-service", name: "inbound-9", displayName: "🇺🇸 美国Provider A", totalBytes: 200 * 1024 ** 3, usedBytes: 12 * 1024 ** 3, resetDay: 22, nextResetAt: "2026-09-22T00:00:00Z" }], inboundsObserved: true, resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:01Z" };
    vi.spyOn(api, "latestApplicationCommand").mockResolvedValue(cached);
    vi.spyOn(api, "createThreeXUIClientCommand").mockResolvedValue({ ...cached, id: "refresh-traffic", state: "pending", inbounds: undefined, inboundsObserved: false });
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);

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
    data.services = [{ id: "reality-service", applicationId: "three-x-ui", siteId: "site", name: "inbound-9", displayName: "🇺🇸 美国Provider A", protocol: "tcp", containerPort: 30443, hostPort: 30443, endpoint: "10.0.0.10:30443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" }];
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    const details = [...container.querySelectorAll("details")].find((value) => value.querySelector("summary")?.textContent?.includes("技术信息"));
    expect(details?.open).toBe(false);
    expect(details?.textContent).toContain("tcp · 10.0.0.10:30443");
  });

  it("shows a recovery path when a monthly node reset fails", async () => {
    const data = realityDashboard();
    data.services = [{ id: "reality-service", applicationId: "three-x-ui", siteId: "site", name: "inbound-9", protocol: "tcp", containerPort: 30443, hostPort: 30443, endpoint: "10.0.0.10:30443", source: "observed", appProtocol: "vless/tcp/reality", management: false, status: "ready", createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:00Z" }];
    vi.spyOn(api, "createThreeXUIClientCommand").mockResolvedValue({ id: "traffic-list", applicationId: "three-x-ui", gatewayNodeId: "agent", kind: "3xui.clients.manage", state: "succeeded", hostname: "", dnsProvider: "manual", action: "list_inbounds", clients: [], clientsObserved: false, inbounds: [{ id: 9, serviceId: "reality-service", name: "inbound-9", totalBytes: 200 * 1024 ** 3, usedBytes: 200 * 1024 ** 3, resetDay: 23, nextResetAt: "2026-08-23T00:00:00Z", planStatus: "failed", planError: "Agent did not confirm the inbound reset" }], inboundsObserved: true, resultAvailable: false, createdAt: "2026-08-23T00:00:00Z", updatedAt: "2026-08-23T00:00:01Z" });
    const container = renderAppDetails(<AppsView data={data} language="zh-CN" mutate={async () => undefined} />);
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("节点套餐"))?.click();
      await Promise.resolve();
    });
    expect(document.body.textContent).toContain("节点月度重置失败");
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
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(location, "Edge Site");
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

});
