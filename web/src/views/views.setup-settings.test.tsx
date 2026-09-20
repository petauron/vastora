// @vitest-environment jsdom

import { act } from "react";
import { describe, expect, it, vi } from "vitest";
import { APIError, api } from "../api";
import { NetworkView } from "./NetworkView";
import { NodesView, agentInstallCommand, validCenterURL } from "./NodesView";
import { SettingsView } from "./SettingsView";
import { SetupWizard } from "./SetupWizard";
import { CloudflareOAuthConnect } from "./CloudflareOAuthConnect";
import { CenterUpdateCard } from "./CenterUpdateCard";
import { userError } from "./shared";

import { dashboard, rerender, resetRender, render } from "./views.test-support";

describe("network and app views", () => {
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
    const container = render(<SetupWizard builtinHeadscaleAvailable cloudflareConfigured cloudflareOAuthAvailable cloudflareZone="example.com" gatewayAddressCandidates={gatewayAddresses} language="zh-CN" observedPublicAddress="203.0.113.79" onComplete={async () => undefined} onLanguage={() => undefined} publicAddressCandidates={[]} publicAddressDetection="cloud_mapping_candidate" suggestedAgentConnectUrl="" suggestedGatewayAddress="10.0.0.157" />);
    const location = container.querySelector<HTMLInputElement>("#setup-location-name")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(location, "Cloud Node A");
      location.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续"))?.click());
    act(() => container.querySelector<HTMLInputElement>('input[value="headscale"]')?.click());

    expect(container.textContent).toContain("发现云公网地址 203.0.113.79");
    expect(container.textContent).toContain("本机 10.0.0.157");
    expect(container.textContent).toContain("这还不代表公网能够访问");
    expect(container.textContent).toContain("不需要先安装 Caddy");
    expect(container.querySelector<HTMLInputElement>("#setup-public-address")?.value).toBe("203.0.113.79");
    expect(container.querySelector<HTMLButtonElement>("#setup-gateway-address")?.textContent).toContain("10.0.0.157");
    expect(container.querySelector<HTMLButtonElement>("#setup-nat-confirmed")?.disabled).toBe(true);
  });

  it("verifies temporary public listeners before showing the setup review", async () => {
    let completeVerification: (() => void) | undefined;
    const verification = new Promise<{ status: "ready"; publicAddress: string; gatewayAddress: string; ports: number[] }>((resolve) => {
      completeVerification = () => resolve({ status: "ready", publicAddress: "203.0.113.79", gatewayAddress: "10.0.0.157", ports: [80, 443] });
    });
    const verify = vi.spyOn(api, "verifySetupPublicEntry").mockReturnValue(verification);
    const gatewayAddresses = [{ address: "10.0.0.157", interface: "enp0s6", kind: "lan" as const, observedAt: "2026-08-24T00:00:00Z" }];
    const container = render(<SetupWizard builtinHeadscaleAvailable cloudflareConfigured cloudflareOAuthAvailable cloudflareZone="example.com" gatewayAddressCandidates={gatewayAddresses} language="zh-CN" observedPublicAddress="203.0.113.79" onComplete={async () => undefined} onLanguage={() => undefined} publicAddressCandidates={[]} publicAddressDetection="cloud_mapping_candidate" suggestedAgentConnectUrl="" suggestedGatewayAddress="10.0.0.157" />);
    const location = container.querySelector<HTMLInputElement>("#setup-location-name")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(location, "Cloud Node A");
      location.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续"))?.click());
    act(() => container.querySelector<HTMLInputElement>('input[value="headscale"]')?.click());

    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续"))?.click();
      await Promise.resolve();
    });
    expect(verify).toHaveBeenCalledWith({ publicAddress: "203.0.113.79", gatewayAddress: "10.0.0.157", natConfirmed: false });
    expect(container.textContent).toContain("正在检测公网入口");
    expect(container.textContent).toContain("完成后会立即释放端口");

    await act(async () => {
      completeVerification?.();
      await verification;
    });
    expect(container.textContent).toContain("公网入口已验证");
    expect(container.textContent).toContain("80、443 已验证");
  });

  it("persists and submits an explicitly confirmed fixed endpoint during first setup", async () => {
    window.sessionStorage.setItem("vastora.initial-setup.v1", JSON.stringify({
      step: 2,
      name: "Cloud Node A",
      timezone: "Asia/Singapore",
      domainSuffix: "vastora.example.com",
      mode: "headscale",
      agentConnectUrl: "https://center.vastora.example.com",
      headscaleMode: "builtin",
      headscaleUrl: "https://headscale.vastora.example.com",
      publicAddress: "203.0.113.10",
      gatewayAddress: "10.0.0.157",
      natConfirmed: false
    }));
    const verify = vi.spyOn(api, "verifySetupPublicEntry").mockResolvedValue({ status: "ready", publicAddress: "203.0.113.10", gatewayAddress: "10.0.0.157", ports: [80, 443] });
    vi.spyOn(api, "configureSetupDNS").mockResolvedValue({ records: [] });
    const onComplete = vi.fn().mockResolvedValue(undefined);
    const publicAddresses = [{ address: "203.0.113.10", interface: "eth0", kind: "public" as const, observedAt: "2026-08-28T00:00:00Z" }];
    const gatewayAddresses = [{ address: "10.0.0.157", interface: "enp0s6", kind: "lan" as const, observedAt: "2026-08-28T00:00:00Z" }];
    const container = render(<SetupWizard builtinHeadscaleAvailable cloudflareConfigured cloudflareOAuthAvailable cloudflareZone="example.com" gatewayAddressCandidates={gatewayAddresses} language="zh-CN" observedPublicAddress="203.0.113.10" onComplete={onComplete} onLanguage={() => undefined} publicAddressCandidates={publicAddresses} publicAddressDetection="direct" suggestedAgentConnectUrl="" suggestedGatewayAddress="10.0.0.157" />);
    const advanced = [...container.querySelectorAll("details")].find((details) => details.textContent?.includes("高级设置"))!;
    act(() => { advanced.open = true; advanced.dispatchEvent(new Event("toggle", { bubbles: true })); });
    const fixedEndpoint = container.querySelector<HTMLButtonElement>("#setup-fixed-endpoint")!;
    expect(container.querySelector("#setup-fixed-endpoint-confirmed")).toBeNull();
    act(() => fixedEndpoint.click());
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续"))?.click());
    expect(container.textContent).toContain("请确认公网 IPv4 已固定");
    expect(verify).not.toHaveBeenCalled();
    act(() => container.querySelector<HTMLButtonElement>("#setup-fixed-endpoint-confirmed")?.click());
    expect(window.sessionStorage.getItem("vastora.initial-setup.v1")).toContain('"publishFixedEndpoint":true');
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续"))?.click();
      await Promise.resolve();
    });
    expect(container.textContent).toContain("203.0.113.10:41641/UDP");
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("完成并添加节点"))?.click();
      await Promise.resolve();
    });
    expect(onComplete).toHaveBeenCalledWith(expect.objectContaining({ tailscaleFixedEndpoint: { enabled: true, endpoint: "203.0.113.10:41641", localAddress: "10.0.0.157", confirmMapping: true } }));
  });

  it("offers a hardened direct Tunnel fallback during private-network setup", async () => {
    window.sessionStorage.setItem("vastora.initial-setup.v1", JSON.stringify({
      step: 2,
      name: "Cloud Node A",
      timezone: "Asia/Singapore",
      domainSuffix: "vastora.example.com",
      mode: "headscale",
      agentConnectUrl: "https://center.vastora.example.com",
      headscaleMode: "builtin",
      headscaleUrl: "https://headscale.vastora.example.com",
      publicAddress: "203.0.113.10",
      gatewayAddress: "203.0.113.10",
      remoteAccessEnabled: true
    }));
    vi.spyOn(api, "verifySetupPublicEntry").mockResolvedValue({ status: "ready", publicAddress: "203.0.113.10", gatewayAddress: "203.0.113.10", ports: [80, 443] });
    vi.spyOn(api, "configureSetupDNS").mockResolvedValue({ records: [] });
    const onComplete = vi.fn().mockResolvedValue(undefined);
    const addresses = [{ address: "203.0.113.10", interface: "eth0", kind: "public" as const, observedAt: "2026-08-29T00:00:00Z" }];
    const container = render(<SetupWizard builtinHeadscaleAvailable cloudflareConfigured cloudflareOAuthAvailable cloudflareTurnstileConfigured cloudflareZone="example.com" gatewayAddressCandidates={addresses} language="zh-CN" observedPublicAddress="203.0.113.10" onComplete={onComplete} onLanguage={() => undefined} publicAddressCandidates={addresses} publicAddressDetection="direct" suggestedAgentConnectUrl="" suggestedGatewayAddress="203.0.113.10" />);
    expect(container.textContent).toContain("Center 远程备用入口");
    expect(container.textContent).toContain("直连登录保护");
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续"))?.click();
      await Promise.resolve();
    });
    expect(container.textContent).toContain("Tunnel 直达登录 · Turnstile + 失败锁定");
    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("完成并添加节点"))?.click();
      await Promise.resolve();
    });
    expect(onComplete).toHaveBeenCalledWith(expect.objectContaining({ centerRemoteAccess: { enabled: true, protectionMode: "native" } }));
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
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(location, "Edge Site");
      location.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("继续"))?.click());
    act(() => container.querySelector<HTMLInputElement>('input[value="headscale"]')?.click());
    resetRender();

    container = render(<SetupWizard {...props} />);
    expect(container.textContent).toContain("你准备在哪里使用 Vastora");
    expect(container.querySelector<HTMLInputElement>('input[value="headscale"]')?.checked).toBe(true);
    expect(window.sessionStorage.getItem("vastora.initial-setup.v1")).not.toContain("apiKey");
  });

  it("upgrades legacy zone-level setup defaults", () => {
    window.sessionStorage.setItem("vastora.initial-setup.v1", JSON.stringify({
      step: 2,
      name: "Example Site",
      timezone: "Asia/Singapore",
      domainSuffix: "example.com",
      mode: "headscale",
      agentConnectUrl: "https://center.example.com",
      headscaleMode: "builtin",
      headscaleUrl: "https://headscale.example.com",
      publicAddress: "203.0.113.10"
    }));
    const addresses = [{ address: "203.0.113.10", interface: "eth0", kind: "public" as const, observedAt: "2026-08-19T00:00:00Z" }];
    const props = { builtinHeadscaleAvailable: true, cloudflareConfigured: false, cloudflareOAuthAvailable: true, gatewayAddressCandidates: addresses, language: "zh-CN" as const, observedPublicAddress: "203.0.113.10", onComplete: async () => undefined, onLanguage: () => undefined, publicAddressCandidates: addresses, publicAddressDetection: "direct" as const, suggestedAgentConnectUrl: "", suggestedGatewayAddress: "203.0.113.10" };
    const container = render(<SetupWizard {...props} />);

    expect(container.querySelector<HTMLInputElement>("#setup-center-url")?.value).toBe("https://center.vastora.example.com");
    expect(container.querySelector<HTMLInputElement>("#setup-headscale-url")?.value).toBe("https://headscale.vastora.example.com");
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("返回"))?.click());
    expect(container.querySelector<HTMLInputElement>("#setup-domain")?.value).toBe("vastora.example.com");
    expect(container.textContent).toContain("服务域名空间");
  });

  it("preserves custom setup hostnames after Cloudflare is connected", () => {
    window.sessionStorage.setItem("vastora.initial-setup.v1", JSON.stringify({
      step: 2,
      name: "Example Site",
      timezone: "Asia/Singapore",
      domainSuffix: "services.ops.example.net",
      mode: "headscale",
      agentConnectUrl: "https://control.ops.example.net",
      headscaleMode: "builtin",
      headscaleUrl: "https://mesh.ops.example.net",
      publicAddress: "203.0.113.10"
    }));
    const addresses = [{ address: "203.0.113.10", interface: "eth0", kind: "public" as const, observedAt: "2026-08-19T00:00:00Z" }];
    const container = render(<SetupWizard builtinHeadscaleAvailable cloudflareConfigured cloudflareOAuthAvailable cloudflareZone="example.com" gatewayAddressCandidates={addresses} language="zh-CN" observedPublicAddress="203.0.113.10" onComplete={async () => undefined} onLanguage={() => undefined} publicAddressCandidates={addresses} publicAddressDetection="direct" suggestedAgentConnectUrl="" suggestedGatewayAddress="203.0.113.10" />);

    expect(container.querySelector<HTMLInputElement>("#setup-center-url")?.value).toBe("https://control.ops.example.net");
    expect(container.querySelector<HTMLInputElement>("#setup-headscale-url")?.value).toBe("https://mesh.ops.example.net");
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("返回"))?.click());
    expect(container.querySelector<HTMLInputElement>("#setup-domain")?.value).toBe("services.ops.example.net");
  });

  it("explains a conflicting DNS record instead of reporting an invalid form", () => {
    const error = new APIError("center: DNS record center.vastora.example.com already exists with a different value", 400, "dns_record_conflict");
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
    act(() => fill("#setup-location-name", "Example Site"));
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

  it("keeps Cloudflare zone confirmation inside a narrow container", async () => {
    vi.useFakeTimers();
    const popupDocument = document.implementation.createHTMLDocument("Cloudflare");
    const popup = { close: vi.fn(), document: popupDocument, location: { replace: vi.fn() }, opener: window } as unknown as Window;
    vi.spyOn(window, "open").mockReturnValue(popup);
    vi.spyOn(api, "startCloudflareOAuth").mockResolvedValue({ sessionId: "session", authorizationUrl: "https://dash.cloudflare.test/oauth2/auth", expiresAt: new Date(Date.now() + 60_000).toISOString() });
    vi.spyOn(api, "pollCloudflareOAuth").mockResolvedValue({ status: "authorized", zones: [{ id: "zone", name: "example.com", accountId: "account", accountName: "A very long Cloudflare account name" }] });
    const container = render(<CloudflareOAuthConnect available connected language="zh-CN" onConnected={() => undefined} zoneName="example.com" />);

    await act(async () => {
      [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("重新连接"))?.click();
      await Promise.resolve();
      await vi.advanceTimersByTimeAsync(1500);
    });

    const section = container.querySelector<HTMLElement>('section[aria-label="连接 Cloudflare"]')!;
    const confirm = [...container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("使用这个域名"))!;
    expect(section.className).toContain("@container/cloudflare-oauth");
    expect(confirm.className).toContain("w-full");
    expect(confirm.className).toContain("@md/cloudflare-oauth:w-auto");
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

  it("applies an independently supplied private CA before the enrollment token is sent", () => {
    const certificate = "-----BEGIN CERTIFICATE-----\nprivate-ca\n-----END CERTIFICATE-----";
    const command = agentInstallCommand({
      centerURL: "https://center.example.com",
      enrollment: { token: "one-time-token", siteId: "site", installerUrl: "https://center.example.com", caCertificatePem: certificate, expiresAt: "2026-08-18T00:10:00Z" },
      installerAvailable: true
    });
    expect(command.indexOf("vastora-center-ca.pem")).toBeLessThan(command.indexOf("one-time-token"));
    expect(command).toContain("curl --cacert /tmp/vastora-center-ca.pem -fsSL");
    expect(command).toContain("'one-time-token' '/tmp/vastora-center-ca.pem' 1");
  });

  it("requires the disabled node name before deleting its Center record", async () => {
    const data = dashboard();
    data.agents[0].status = "disabled";
    data.agents[0].connected = false;
    const remove = vi.spyOn(api, "deleteAgent").mockResolvedValue({ deleted: true });
    const close = vi.fn();
    const container = render(<NodesView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} onNavigate={close} />);
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent === "删除")?.click());
    expect(document.body.textContent).toContain("服务器上的程序和数据不会被删除");
    const confirm = [...document.querySelectorAll("button")].filter((button) => button.textContent === "删除节点").at(-1);
    expect(confirm?.disabled).toBe(true);
    const input = document.querySelector<HTMLInputElement>("#disable-node-confirmation")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, data.agents[0].name);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => confirm?.click());
    expect(remove).toHaveBeenCalledWith(data.agents[0].id);
  });

  it("stops an offline node's access without requiring its apps or gateway to be removed", async () => {
    const data = dashboard();
    data.agents[0].name = "Edge Node B";
    data.agents[0].connected = false;
    const revoke = vi.spyOn(api, "revokeAgentCredential").mockResolvedValue({ revoked: true });
    const disable = vi.spyOn(api, "disableAgent");
    const remove = vi.spyOn(api, "deleteAgent");
    const deploy = vi.spyOn(api, "createDeployment");
    const container = render(<NodesView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} onNavigate={() => undefined} />);
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="管理 Edge Node B"]')?.click());
    act(() => [...document.querySelectorAll("button")].find((button) => button.textContent === "停止接入")?.click());
    const input = document.querySelector<HTMLInputElement>("#stop-node-access-name")!;
    const confirm = () => [...document.querySelectorAll("button")].find((button) => button.textContent === "停止接入" && button.type === "submit")!;
    expect(confirm().disabled).toBe(true);
    expect(document.body.textContent).toContain("无需节点在线");
    expect(document.body.textContent).toContain("不会卸载应用、删除记录或清理服务器数据");
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "Edge-Node-B");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(confirm().disabled).toBe(true);
    expect(input.getAttribute("aria-invalid")).toBe("true");
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, " Edge Node B ");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(confirm().disabled).toBe(false);
    await act(async () => confirm().click());
    expect(revoke).toHaveBeenCalledExactlyOnceWith(data.agents[0].id);
    expect(disable).not.toHaveBeenCalled();
    expect(remove).not.toHaveBeenCalled();
    expect(deploy).not.toHaveBeenCalled();
    expect(document.querySelector("#stop-node-access-name")).toBeNull();
  });

  it("does not offer offline access stop for a connected node", () => {
    const container = render(<NodesView data={dashboard()} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="管理 home-server"]')?.click());
    expect([...document.querySelectorAll("button")].some((button) => button.textContent === "停止接入")).toBe(false);
  });

  it("shows stopped access distinctly from offline and preserves reconnect", () => {
    const data = dashboard();
    data.agents[0].connected = false;
    data.agents[0].credentialRevoked = true;
    const container = render(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    expect(container.textContent).toContain("已停止接入");
    expect(container.textContent).not.toContain("离线");
    expect(container.querySelector('[aria-label="重新接入 home-server"]')).not.toBeNull();
  });

  it("keeps access-stop failures inline and does nothing on cancellation", async () => {
    const data = dashboard();
    data.agents[0].connected = false;
    const revoke = vi.spyOn(api, "revokeAgentCredential").mockRejectedValue(new Error("Failed to fetch"));
    const container = render(<NodesView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} onNavigate={() => undefined} />);
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="管理 home-server"]')?.click());
    act(() => [...document.querySelectorAll("button")].find((button) => button.textContent === "停止接入")?.click());
    act(() => [...document.querySelectorAll("button")].find((button) => button.textContent === "取消")?.click());
    expect(revoke).not.toHaveBeenCalled();
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="管理 home-server"]')?.click());
    act(() => [...document.querySelectorAll("button")].find((button) => button.textContent === "停止接入")?.click());
    const input = document.querySelector<HTMLInputElement>("#stop-node-access-name")!;
    expect(input.value).toBe("");
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, data.agents[0].name);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => [...document.querySelectorAll("button")].find((button) => button.textContent === "停止接入" && button.type === "submit")?.click());
    expect(input.value).toBe(data.agents[0].name);
    expect(document.body.textContent).toContain("无法连接 Center");
    expect(revoke).toHaveBeenCalledOnce();
  });

  it("blocks the stale confirmation if the offline node reconnects", () => {
    const data = dashboard();
    data.agents[0].connected = false;
    const mutate = async () => undefined;
    const container = render(<NodesView data={data} language="zh-CN" mutate={mutate} onNavigate={() => undefined} />);
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="管理 home-server"]')?.click());
    act(() => [...document.querySelectorAll("button")].find((button) => button.textContent === "停止接入")?.click());
    const input = document.querySelector<HTMLInputElement>("#stop-node-access-name")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, data.agents[0].name);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const next = { ...data, agents: [{ ...data.agents[0], connected: true }] };
    rerender(<NodesView data={next} language="zh-CN" mutate={mutate} onNavigate={() => undefined} />);
    expect(document.body.textContent).toContain("节点状态已变化");
    expect([...document.querySelectorAll("button")].find((button) => button.textContent === "停止接入" && button.type === "submit")?.disabled).toBe(true);
  });

  it("queues supported Agent updates through Center and keeps purpose changes explicit", async () => {
    const data = dashboard();
    data.agents[0].version = "old";
    const update = vi.spyOn(api, "startAgentUpdate").mockResolvedValue({ id: "agent-update-1", targetVersion: "test", state: "pending", updatedAt: "2026-08-18T00:00:00Z" });
    const container = render(<NodesView data={data} language="zh-CN" mutate={async (operation) => { await operation(); }} onNavigate={() => undefined} />);
    const manage = container.querySelector<HTMLButtonElement>('[aria-label="管理 home-server"]');
    act(() => manage?.click());
    expect(document.body.textContent).toContain("节点用途");
    const updateButton = [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("通过 Center 更新"));
    await act(async () => updateButton?.click());
    expect(update).toHaveBeenCalledWith("agent");
    expect(document.body.textContent).not.toContain("agent update");
    const tunnel = document.querySelector<HTMLElement>("#manage-node-tunnel");
    act(() => tunnel?.click());
    const generate = [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("生成修改命令"));
    act(() => generate?.click());
    expect(document.body.textContent).toContain("agent configure");
    expect(document.body.textContent).toContain("--capabilities 'docker,gateway'");
  });

  it("shows a recovering Agent update error while keeping competing updates disabled", () => {
    const data = dashboard();
    data.agents[0].version = "old";
    data.agents[0].update = { id: "agent-update-1", targetVersion: "test", state: "installing", lastError: "Recovery required: candidate startup failed", updatedAt: "2026-08-18T00:00:00Z" };
    const container = render(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    const manage = container.querySelector<HTMLButtonElement>('[aria-label="管理 home-server"]');
    act(() => manage?.click());
    expect(document.body.textContent).toContain("Recovery required: candidate startup failed");
    const updateButton = [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("正在更新"));
    expect(updateButton?.disabled).toBe(true);
    expect(document.body.textContent).not.toContain("重试更新");
  });

  it("shows one manual bootstrap update for legacy Agents", () => {
    const data = dashboard();
    data.agents[0].version = "old";
    data.agents[0].remoteUpdateSupported = false;
    const container = render(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    const manage = container.querySelector<HTMLButtonElement>('[aria-label="管理 home-server"]');
    act(() => manage?.click());
    expect(document.body.textContent).toContain("需要一次手动更新");
    expect(document.body.textContent).toContain("sudo /usr/local/bin/vastora agent update --data-dir /var/lib/vastora/agent");
    expect(document.body.textContent).not.toContain("--center-url");
    expect(document.body.textContent).not.toContain("通过 Center 更新");
  });

  it("generates a one-time reconnect command for an offline Agent", async () => {
    const data = dashboard();
    data.agents[0].connected = false;
    const reconnect = vi.spyOn(api, "createAgentReconnectEnrollment").mockResolvedValue({
      token: "replacement-token",
      siteId: "site",
      centerUrl: "https://center.example.com",
      installerUrl: "https://center.example.com",
      expiresAt: "2026-09-03T12:10:00Z"
    });
    const container = render(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    const reconnectButton = container.querySelector<HTMLButtonElement>('[aria-label="重新接入 home-server"]');
    await act(async () => {
      reconnectButton?.click();
      await Promise.resolve();
    });
    expect(reconnect).toHaveBeenCalledWith("agent");
    expect(document.body.textContent).toContain("保留原节点，替换身份");
    expect(document.body.textContent).toContain("节点 ID、名称、位置、用途、应用关系和已确认网络保持不变");
    expect(document.body.textContent).toContain("replacement-token");
    expect(document.body.textContent).toContain("正在等待原节点重新上线");
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

  it("clears backup secrets after every close path and successful download", async () => {
    vi.spyOn(api, "downloadBackup").mockResolvedValue(undefined);
    render(<SettingsView data={dashboard()} language="zh-CN" mutate={async () => undefined} onCenterUpdateStatus={() => undefined} onLogout={async () => undefined} onRefresh={async () => undefined} />);
    const clickButton = (label: string) => act(() => [...document.querySelectorAll("button")].find((button) => button.textContent?.includes(label))?.click());
    const enter = (selector: string, value: string) => act(() => {
      const input = document.querySelector<HTMLInputElement | HTMLTextAreaElement>(selector);
      if (!input) throw new Error(`missing input ${selector}`);
      const prototype = input instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
      Object.getOwnPropertyDescriptor(prototype, "value")?.set?.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const openAndFill = () => {
      clickButton("下载加密备份");
      enter("#backup-password", "secret-backup-password");
      enter("#backup-confirmation", "secret-backup-password");
    };
    const expectReopenedEmpty = () => {
      clickButton("下载加密备份");
      expect(document.querySelector<HTMLInputElement>("#backup-password")?.value).toBe("");
      expect(document.querySelector<HTMLInputElement>("#backup-confirmation")?.value).toBe("");
      clickButton("取消");
    };
    const dismissOutside = () => act(() => {
      const overlay = document.querySelector<HTMLElement>('[data-slot="sheet-overlay"]');
      for (const type of ["pointerdown", "mousedown", "pointerup", "mouseup", "click"]) overlay?.dispatchEvent(new MouseEvent(type, { bubbles: true }));
    });

    openAndFill(); clickButton("取消"); expectReopenedEmpty();
    openAndFill(); act(() => document.querySelector<HTMLButtonElement>('[data-slot="sheet-close"]')?.click()); expectReopenedEmpty();
    openAndFill(); act(() => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))); expectReopenedEmpty();
    openAndFill(); dismissOutside(); expectReopenedEmpty();
    openAndFill();
    await act(async () => {
      document.querySelector<HTMLFormElement>("#backup-password")?.closest("form")?.requestSubmit();
      await Promise.resolve();
    });
    expect(api.downloadBackup).toHaveBeenCalledWith("secret-backup-password");
    expectReopenedEmpty();
  });

  it("retains failed catalog input only while open and clears it on every close path", async () => {
    const mutate = vi.fn().mockRejectedValue(new Error("catalog rejected"));
    const container = render(<SettingsView data={dashboard()} language="zh-CN" mutate={mutate} onCenterUpdateStatus={() => undefined} onLogout={async () => undefined} onRefresh={async () => undefined} />);
    const clickButton = (label: string) => act(() => [...document.querySelectorAll("button")].find((button) => button.textContent?.includes(label))?.click());
    const enter = (selector: string, value: string) => act(() => {
      const input = document.querySelector<HTMLInputElement | HTMLTextAreaElement>(selector);
      if (!input) throw new Error(`missing input ${selector}`);
      const prototype = input instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
      Object.getOwnPropertyDescriptor(prototype, "value")?.set?.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const openAndFill = () => {
      clickButton("添加目录");
      enter("#source-token", "secret-bearer-token");
      enter("#source-ca", "secret-custom-ca");
    };
    const expectReopenedEmpty = () => {
      clickButton("添加目录");
      expect(document.querySelector<HTMLInputElement>("#source-token")?.value).toBe("");
      expect(document.querySelector<HTMLTextAreaElement>("#source-ca")?.value).toBe("");
      clickButton("取消");
    };
    const dismissOutside = () => act(() => {
      const overlay = document.querySelector<HTMLElement>('[data-slot="sheet-overlay"]');
      for (const type of ["pointerdown", "mousedown", "pointerup", "mouseup", "click"]) overlay?.dispatchEvent(new MouseEvent(type, { bubbles: true }));
    });
    act(() => [...container.querySelectorAll("summary")].find((summary) => summary.textContent?.includes("应用目录"))?.click());
    openAndFill(); clickButton("取消"); expectReopenedEmpty();
    openAndFill(); act(() => document.querySelector<HTMLButtonElement>('[data-slot="sheet-close"]')?.click()); expectReopenedEmpty();
    openAndFill(); act(() => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))); expectReopenedEmpty();
    openAndFill(); dismissOutside(); expectReopenedEmpty();

    openAndFill();
    enter("#source-id", "private-source");
    enter("#source-name", "Private source");
    enter("#source-url", "https://private.example/catalog");
    enter("#source-key", "public-key");
    await act(async () => {
      document.querySelector<HTMLFormElement>("#source-id")?.closest("form")?.requestSubmit();
      await Promise.resolve();
    });
    expect(document.querySelector<HTMLInputElement>("#source-token")?.value).toBe("secret-bearer-token");
    expect(document.querySelector<HTMLTextAreaElement>("#source-ca")?.value).toBe("secret-custom-ca");
    expect(document.body.textContent).toContain("操作未完成");
    clickButton("取消");
    expectReopenedEmpty();
  });

  it("unmounts catalog secrets when a successful submit closes the sheet programmatically", async () => {
    const mutate = vi.fn().mockResolvedValue(undefined);
    const container = render(<SettingsView data={dashboard()} language="zh-CN" mutate={mutate} onCenterUpdateStatus={() => undefined} onLogout={async () => undefined} onRefresh={async () => undefined} />);
    const clickButton = (label: string) => act(() => [...document.querySelectorAll("button")].find((button) => button.textContent?.includes(label))?.click());
    const enter = (selector: string, value: string) => act(() => {
      const input = document.querySelector<HTMLInputElement | HTMLTextAreaElement>(selector);
      if (!input) throw new Error(`missing input ${selector}`);
      const prototype = input instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
      Object.getOwnPropertyDescriptor(prototype, "value")?.set?.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => [...container.querySelectorAll("summary")].find((summary) => summary.textContent?.includes("应用目录"))?.click());
    clickButton("添加目录");
    enter("#source-id", "private-source"); enter("#source-name", "Private source"); enter("#source-url", "https://private.example/catalog"); enter("#source-key", "public-key"); enter("#source-token", "secret-bearer-token"); enter("#source-ca", "secret-custom-ca");
    await act(async () => {
      document.querySelector<HTMLFormElement>("#source-id")?.closest("form")?.requestSubmit();
      await Promise.resolve();
    });
    expect(document.querySelector("#source-token")).toBeNull();
    clickButton("添加目录");
    expect(document.querySelector<HTMLInputElement>("#source-token")?.value).toBe("");
    expect(document.querySelector<HTMLTextAreaElement>("#source-ca")?.value).toBe("");
  });

  it("distinguishes verified, cached, failed, pending, and disabled catalog states in Chinese", () => {
    const data = dashboard();
    data.sources = [
      { id: "healthy", displayName: "Healthy source", url: "https://healthy.example/catalog", publicKey: "key", customCASet: false, bearerTokenSet: false, enabled: true, status: "healthy", refreshIntervalSeconds: 3600, fetchedAt: "2026-08-30T00:00:00Z", checkedAt: "2026-08-30T00:00:00Z" },
      { id: "stale", displayName: "Stale source", url: "https://stale.example/catalog", publicKey: "key", customCASet: false, bearerTokenSet: true, enabled: true, status: "stale", refreshIntervalSeconds: 3600, fetchedAt: "2026-08-29T00:00:00Z", checkedAt: "2026-08-30T00:00:00Z", lastError: "temporary failure" },
      { id: "failed", displayName: "Failed source", url: "https://failed.example/catalog", publicKey: "key", customCASet: false, bearerTokenSet: false, enabled: true, status: "failed", refreshIntervalSeconds: 3600, checkedAt: "2026-08-30T00:00:00Z", lastError: "signature failure" },
      { id: "pending", displayName: "Pending source", url: "https://pending.example/catalog", publicKey: "key", customCASet: false, bearerTokenSet: false, enabled: true, status: "pending", refreshIntervalSeconds: 3600 },
      { id: "disabled", displayName: "Disabled source", url: "https://disabled.example/catalog", publicKey: "key", customCASet: false, bearerTokenSet: false, enabled: false, status: "disabled", refreshIntervalSeconds: 3600 }
    ];
    const container = render(<SettingsView data={data} language="zh-CN" mutate={async () => undefined} onCenterUpdateStatus={() => undefined} onLogout={async () => undefined} onRefresh={async () => undefined} />);
    const catalogs = [...container.querySelectorAll("summary")].find((summary) => summary.textContent?.includes("应用目录"));
    act(() => catalogs?.click());
    for (const expected of ["健康", "使用缓存", "失败", "等待中", "未启用", "正在继续使用最后一次验证通过的缓存", "目录暂不可用于安装"]) {
      expect(container.textContent).toContain(expected);
    }
  });

  it("shows official catalog revision and refresh requirement without hiding existing apps", () => {
    const data = dashboard();
    data.sources = [{ id: "vastora-official", displayName: "Vastora Official", url: "https://downloads.petauron.com/vastora/catalog/", publicKey: "", customCASet: false, bearerTokenSet: false, enabled: true, status: "expired", refreshIntervalSeconds: 3600, catalogRevision: 42, expiresAt: "2026-09-11T00:00:00Z" }];
    const container = render(<SettingsView data={data} language="zh-CN" mutate={async () => undefined} onCenterUpdateStatus={() => undefined} onLogout={async () => undefined} onRefresh={async () => undefined} />);
    act(() => [...container.querySelectorAll("summary")].find(summary => summary.textContent?.includes("应用目录"))?.click());
    for (const text of ["Vastora 官方目录", "目录修订", "42", "有效期至", "需刷新", "已安装应用不受影响"]) expect(container.textContent).toContain(text);
    expect(container.querySelector('[aria-label="目录身份"]')?.textContent).toBe("官方目录");
  });

  it("labels a private catalog as third-party even when its display name copies the official catalog", () => {
    const data = dashboard();
    data.sources = [{ id: "community", displayName: "Vastora 官方目录", url: "https://private.example/catalog", publicKey: "key", customCASet: false, bearerTokenSet: false, enabled: true, status: "healthy", refreshIntervalSeconds: 3600 }];
    const container = render(<SettingsView data={data} language="zh-CN" mutate={async () => undefined} onCenterUpdateStatus={() => undefined} onLogout={async () => undefined} onRefresh={async () => undefined} />);
    act(() => [...container.querySelectorAll("summary")].find(summary => summary.textContent?.includes("应用目录"))?.click());
    expect(container.querySelector('[aria-label="目录身份"]')?.textContent).toBe("第三方目录");
  });

  it("edits, disables, and confirms deletion of a private catalog in English", async () => {
    const data = dashboard();
    data.sources = [{ id: "private-source", displayName: "Private source", url: "https://private.example/catalog", publicKey: "public-key", customCASet: true, bearerTokenSet: true, enabled: true, status: "healthy", refreshIntervalSeconds: 3600, fetchedAt: "2026-08-30T00:00:00Z", checkedAt: "2026-08-30T00:00:00Z" }];
    const update = vi.spyOn(api, "updateSource").mockResolvedValue({ id: "private-source" });
    const remove = vi.spyOn(api, "deleteSource").mockResolvedValue(undefined);
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const mutate = async (operation: () => Promise<unknown>) => { await operation(); };
    const container = render(<SettingsView data={data} language="en" mutate={mutate} onCenterUpdateStatus={() => undefined} onLogout={async () => undefined} onRefresh={async () => undefined} />);
    act(() => [...container.querySelectorAll("summary")].find((summary) => summary.textContent?.includes("app catalogs"))?.click());
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("Edit"))?.click());
    expect(document.body.textContent).toContain("Blank credential fields preserve the stored values");
    expect(document.querySelector<HTMLInputElement>("#source-token-private-source")?.placeholder).toContain("keep the stored token");
    await act(async () => {
      document.querySelector<HTMLFormElement>("#source-name-private-source")?.closest("form")?.requestSubmit();
      await Promise.resolve();
    });
    expect(update).toHaveBeenCalledWith("private-source", expect.objectContaining({ displayName: "Private source" }));
    expect(update.mock.calls[0]?.[1]).not.toHaveProperty("bearerToken");
    await act(async () => { await [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("Disable"))?.click(); });
    expect(update).toHaveBeenCalledWith("private-source", { enabled: false });
    await act(async () => { await [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("Delete"))?.click(); });
    expect(window.confirm).toHaveBeenCalledOnce();
    expect(remove).toHaveBeenCalledWith("private-source");
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

  it("keeps domain-switch blockers inside the switch workflow", async () => {
    const data = dashboard();
    data.systemDomain.activePublications = 1;
    const listZones = vi.spyOn(api, "cloudflareZones");
    const container = render(<SettingsView data={data} language="zh-CN" mutate={async () => undefined} onCenterUpdateStatus={() => undefined} onLogout={async () => undefined} onRefresh={async () => undefined} />);
    expect(container.textContent).not.toContain("请先停止 1 个访问入口");
    const changeDomain = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("切换域名"));
    expect(changeDomain?.disabled).toBe(false);
    await act(async () => { changeDomain?.click(); });
    expect(document.body.textContent).toContain("请先停止 1 个访问入口，再回来切换域名");
    expect(document.body.textContent).toContain("管理访问入口");
    expect(listZones).not.toHaveBeenCalled();
  });

  it("shows a confirmed Center update instead of exposing Docker access", () => {
    const data = dashboard();
    data.centerUpdate = { currentVersion: "0.1.0-alpha.47", latestVersion: "0.1.0-alpha.48", updateAvailable: true, releaseCheckAvailable: true, automatic: true, state: "idle", checkedAt: "2026-08-25T00:00:00Z" };
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

  it("refreshes and reloads after Center succeeds without waiting for every Agent", async () => {
    const status = { ...dashboard().centerUpdate, latestVersion: "0.1.0-alpha.51", updateAvailable: true, state: "applying" as const };
    const onRefresh = vi.fn().mockResolvedValue(undefined);
    const onReload = vi.fn();
    const onStatusChange = vi.fn();
    const completed = {
      ...status,
      currentVersion: "0.1.0-alpha.51",
      updateAvailable: false,
      state: "succeeded" as const,
      agentRollout: { targetVersion: "0.1.0-alpha.51", total: 3, updated: 1, updating: 1, pending: 1, failed: 0, offline: 0, manual: 0, blocked: 0 },
    };
    vi.spyOn(api, "centerUpdate").mockResolvedValue(completed);
    render(<CenterUpdateCard language="zh-CN" onRefresh={onRefresh} onReload={onReload} onStatusChange={onStatusChange} status={status} />);
    await act(async () => { await Promise.resolve(); });
    expect(onRefresh).toHaveBeenCalledOnce();
    expect(onReload).toHaveBeenCalledOnce();
    expect(onStatusChange).toHaveBeenCalledWith(completed);
  });

  it("keeps polling pending Agents after the updated Center page loads", async () => {
    const status = {
      ...dashboard().centerUpdate,
      currentVersion: "0.1.0-alpha.51",
      latestVersion: "0.1.0-alpha.51",
      updateAvailable: false,
      state: "succeeded" as const,
      agentRollout: { targetVersion: "0.1.0-alpha.51", total: 3, updated: 1, updating: 0, pending: 2, failed: 0, offline: 0, manual: 0, blocked: 0 },
    };
    const onRefresh = vi.fn().mockResolvedValue(undefined);
    const onReload = vi.fn();
    const onStatusChange = vi.fn();
    const pending = {
      ...status,
      currentVersion: "0.1.0-alpha.51",
      updateAvailable: false,
      state: "succeeded" as const,
      agentRollout: { targetVersion: "0.1.0-alpha.51", total: 3, updated: 1, updating: 0, pending: 2, failed: 0, offline: 0, manual: 0, blocked: 0 },
    };
    vi.spyOn(api, "centerUpdate").mockResolvedValue(pending);
    render(<CenterUpdateCard language="zh-CN" onRefresh={onRefresh} onReload={onReload} onStatusChange={onStatusChange} status={status} />);
    await act(async () => { await Promise.resolve(); });
    expect(onStatusChange).toHaveBeenCalledWith(pending);
    expect(onRefresh).not.toHaveBeenCalled();
    expect(onReload).not.toHaveBeenCalled();
  });

  it("does not keep the update busy when an Agent requires follow-up", async () => {
    const status = {
      ...dashboard().centerUpdate,
      currentVersion: "0.1.0-alpha.51",
      latestVersion: "0.1.0-alpha.51",
      updateAvailable: false,
      state: "succeeded" as const,
      agentRollout: { targetVersion: "0.1.0-alpha.51", total: 3, updated: 2, updating: 0, pending: 0, failed: 1, offline: 0, manual: 0, blocked: 0 },
    };
    const onRefresh = vi.fn().mockResolvedValue(undefined);
    const onReload = vi.fn();
    const onStatusChange = vi.fn();
    const failed = {
      ...status,
      currentVersion: "0.1.0-alpha.51",
      updateAvailable: false,
      state: "succeeded" as const,
      agentRollout: { targetVersion: "0.1.0-alpha.51", total: 3, updated: 2, updating: 0, pending: 0, failed: 1, offline: 0, manual: 0, blocked: 0 },
    };
    const check = vi.spyOn(api, "centerUpdate").mockResolvedValue(failed);
    render(<CenterUpdateCard language="zh-CN" onRefresh={onRefresh} onReload={onReload} onStatusChange={onStatusChange} status={status} />);
    await act(async () => { await Promise.resolve(); });
    expect(check).not.toHaveBeenCalled();
    expect(onStatusChange).not.toHaveBeenCalled();
    expect(onRefresh).not.toHaveBeenCalled();
    expect(onReload).not.toHaveBeenCalled();
  });

  it("distinguishes unresolved task blockers from legacy Agents", () => {
    const status = {
      ...dashboard().centerUpdate,
      currentVersion: "0.1.0-alpha.159",
      latestVersion: "0.1.0-alpha.159",
      updateAvailable: false,
      state: "succeeded" as const,
      agentRollout: { targetVersion: "0.1.0-alpha.159", total: 17, updated: 6, updating: 0, pending: 0, failed: 0, offline: 0, manual: 0, blocked: 11 },
    };
    const container = render(<CenterUpdateCard language="zh-CN" onRefresh={async () => undefined} onStatusChange={() => undefined} status={status} />);
    expect(container.textContent).toContain("11 个存在待处理任务，已暂停更新");
    expect(container.textContent).not.toContain("旧版本需要手动更新");
  });

  it("shows the current verified update phase and progress", () => {
    const status = { ...dashboard().centerUpdate, latestVersion: "0.1.0-alpha.68", updateAvailable: true, state: "applying" as const, targetVersion: "0.1.0-alpha.68", phase: "pulling" as const, progress: 50 };
    vi.spyOn(api, "centerUpdate").mockImplementation(() => new Promise(() => undefined));
    const container = render(<CenterUpdateCard language="zh-CN" onRefresh={async () => undefined} onStatusChange={() => undefined} status={status} />);
    const progress = container.querySelector('[role="progressbar"]');
    expect(progress?.getAttribute("aria-valuenow")).toBe("50");
    expect(progress?.getAttribute("data-indeterminate")).toBeNull();
    expect(progress?.getAttribute("aria-labelledby")).not.toBeNull();
    expect(container.textContent).toContain("正在下载 Center 镜像");
    expect(container.textContent).toContain("50%");
  });

  it("shows remote Agent rollout as independent background work", () => {
    const status = {
      ...dashboard().centerUpdate,
      currentVersion: "0.1.0-alpha.99",
      latestVersion: "0.1.0-alpha.99",
      updateAvailable: false,
      state: "succeeded" as const,
      targetVersion: "0.1.0-alpha.99",
      agentRollout: { targetVersion: "0.1.0-alpha.99", total: 4, updated: 2, updating: 1, pending: 1, failed: 0, offline: 0, manual: 0, blocked: 0 },
    };
    vi.spyOn(api, "centerUpdate").mockImplementation(() => new Promise(() => undefined));
    const container = render(<CenterUpdateCard language="zh-CN" onRefresh={async () => undefined} onStatusChange={() => undefined} status={status} />);
    expect(container.textContent).toContain("Agent 同步进度");
    expect(container.textContent).not.toContain("并发");
    expect(container.textContent).not.toContain("远端");
    expect(container.textContent).toContain("2/4 个 Agent 已是当前版本");
    expect(container.textContent).toContain("Agent 后台升级");
    expect(container.textContent).toContain("1 个正在更新；1 个等待领取更新任务");
    expect(container.textContent).not.toContain("Center 会短暂重启");
    expect(container.querySelector('[role="progressbar"]')?.getAttribute("aria-valuenow")).toBe("50");
  });

  it("reports Center completion while Agents wait for independent tasks", () => {
    const status = {
      ...dashboard().centerUpdate,
      currentVersion: "0.1.0-alpha.99",
      latestVersion: "0.1.0-alpha.99",
      updateAvailable: false,
      state: "succeeded" as const,
      targetVersion: "0.1.0-alpha.99",
      agentRollout: { targetVersion: "0.1.0-alpha.99", total: 16, updated: 1, updating: 0, pending: 15, failed: 0, offline: 0, manual: 0, blocked: 0 },
    };
    const container = render(<CenterUpdateCard language="zh-CN" onRefresh={async () => undefined} onStatusChange={() => undefined} status={status} />);
    expect(container.textContent).toContain("Center 更新完成");
    expect(container.textContent).toContain("15 个等待领取更新任务");
    expect(container.textContent).not.toContain("等待发布条件");
    expect(container.querySelector('[role="progressbar"]')?.getAttribute("aria-valuenow")).toBe("6");
  });

  it("shows a complete Agent rollout progress bar after the update", () => {
    const status = {
      ...dashboard().centerUpdate,
      currentVersion: "0.1.0-alpha.151",
      latestVersion: "0.1.0-alpha.151",
      updateAvailable: false,
      state: "succeeded" as const,
      agentRollout: { targetVersion: "0.1.0-alpha.151", total: 17, updated: 17, updating: 0, pending: 0, failed: 0, offline: 0, manual: 0, blocked: 0 },
    };
    const container = render(<CenterUpdateCard language="zh-CN" onRefresh={async () => undefined} onStatusChange={() => undefined} status={status} />);
    const progress = container.querySelector('[role="progressbar"]');
    expect(progress?.getAttribute("aria-valuenow")).toBe("100");
    expect(progress?.getAttribute("aria-valuetext")).toBe("17/17 个 Agent 已是当前版本");
    expect(container.textContent).toContain("Agent 同步进度");
  });

  it("bypasses the official release cache when update checking is requested", async () => {
    const status = { ...dashboard().centerUpdate, latestVersion: "0.1.0-alpha.59", updateAvailable: true };
    const refreshed = { ...status, latestVersion: "0.1.0-alpha.60" };
    const check = vi.spyOn(api, "centerUpdate").mockResolvedValue(refreshed);
    const onStatusChange = vi.fn();
    const container = render(<CenterUpdateCard language="zh-CN" onRefresh={async () => undefined} onStatusChange={onStatusChange} status={status} />);
    const refresh = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("检查更新"));
    await act(async () => { refresh?.click(); await Promise.resolve(); });
    expect(check).toHaveBeenCalledWith(true);
    expect(onStatusChange).toHaveBeenCalledWith(refreshed);
  });
});
