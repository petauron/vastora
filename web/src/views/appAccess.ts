import type { AgentView, AppView, AppData, Deployment, PublicationKind, Service } from "../types";
import type { Language } from "../translations";
import { copy } from "./shared";

export function publicationOptions(data: AppData, service: Service | null, language: Language) {
  if (!service) return [];
  const web = service.protocol === "http" || service.protocol === "https";
  const cloudflare = data.integrations.some((value) => value.kind === "cloudflare" && value.status === "configured");
  const available = (kind: PublicationKind) => gatewaysForKind(data, service, kind).length > 0;
  const options: Array<{ kind: PublicationKind; enabled: boolean; reason: string }> = [];
  if (web) {
    const lan = available("lan_gateway");
    const headscale = available("headscale_gateway");
    const direct = available("public_direct");
    const tunnel = available("cloudflare_tunnel") && cloudflare;
    options.push({ kind: "lan_gateway", enabled: lan, reason: lan ? cloudflare ? "HTTPS · LAN Gateway" : copy(language, "HTTP · 连接 Cloudflare 后可启用 HTTPS", "HTTP · connect Cloudflare to enable HTTPS") : copy(language, "没有可用的局域网 Gateway", "No LAN Gateway is available") });
    options.push({ kind: "headscale_gateway", enabled: headscale, reason: headscale ? cloudflare ? "HTTPS · Headscale Gateway" : copy(language, "HTTP · 连接 Cloudflare 后可启用 HTTPS", "HTTP · connect Cloudflare to enable HTTPS") : copy(language, "没有可用的 Headscale Gateway", "No Headscale Gateway is available") });
    options.push({ kind: "public_direct", enabled: direct, reason: direct ? "HTTPS · Public Gateway" : copy(language, "没有已批准的公网 Gateway", "No approved public Gateway is available") });
    options.push({ kind: "cloudflare_tunnel", enabled: tunnel, reason: tunnel ? "HTTPS · Cloudflare Tunnel" : copy(language, "请先连接 Cloudflare 并启用 Tunnel 节点", "Connect Cloudflare and enable a Tunnel node first") });
  } else {
    const direct = available("public_direct");
    const shared443 = available("public_shared_443");
    options.push({ kind: "public_direct", enabled: direct, reason: direct ? copy(language, "原始端口 · 仅应用节点", "Direct raw port · app node only") : copy(language, "应用节点没有已批准的公网地址", "The app node has no approved public address") });
    if (service.protocol === "tcp") options.push({ kind: "public_shared_443", enabled: shared443, reason: shared443 ? copy(language, "与 Web 共用公网 443 · 自动启用 HAProxy", "Share public 443 with Web · enables HAProxy automatically") : copy(language, "没有可用的公网 Gateway", "No public Gateway is available") });
  }
  return options;
}

export type PublicationIntent = "private" | "public_web" | "protocol";

export function publicationIntentOptions(data: AppData, service: Service | null, language: Language) {
  if (!service) return [];
  const web = service.protocol === "http" || service.protocol === "https";
  if (!web) {
    const kind = preferredKind(data, service, ["public_direct", "public_shared_443"]);
    return [{
      intent: "protocol" as const,
      kind,
      enabled: Boolean(kind),
      title: copy(language, "开放代理协议端口", "Expose a proxy protocol port"),
      description: kind
        ? copy(language, "供 VLESS 等客户端直接连接；端口仍由应用管理。", "For VLESS and similar clients. The app continues to manage the port.")
        : copy(language, "节点需要已确认的公网地址。", "The node needs a confirmed public address.")
    }];
  }

  const privateKind = preferredKind(data, service, ["headscale_gateway", "lan_gateway"]);
  const publicKind = preferredKind(data, service, ["cloudflare_tunnel", "public_direct"]);
  return [
    {
      intent: "private" as const,
      kind: privateKind,
      enabled: Boolean(privateKind),
      title: copy(language, "仅我的设备访问", "Only my devices"),
      description: privateKind
        ? copy(language, "使用局域网或安全私网，不暴露到互联网。", "Uses your local or secure private network and stays off the public internet.")
        : copy(language, "请先确认节点的局域网或安全私网。", "Confirm a local or secure private network first.")
    },
    {
      intent: "public_web" as const,
      kind: publicKind,
      enabled: Boolean(publicKind),
      title: copy(language, "通过互联网打开网页", "Open the website from the internet"),
      description: publicKind
        ? copy(language, "自动选择安全的 HTTPS 入口。", "Automatically selects a secure HTTPS access method.")
        : copy(language, "请先连接 Cloudflare，或确认公网入口节点。", "Connect Cloudflare or confirm a public entry node first.")
    }
  ];
}

export function publicationKindsForIntent(service: Service, intent: PublicationIntent): PublicationKind[] {
  if (intent === "private") return ["headscale_gateway", "lan_gateway"];
  if (intent === "public_web") return ["cloudflare_tunnel", "public_direct"];
  return service.protocol === "tcp" ? ["public_direct", "public_shared_443"] : ["public_direct"];
}

function preferredKind(data: AppData, service: Service, kinds: PublicationKind[]) {
  const cloudflareReady = data.integrations.some((value) => value.kind === "cloudflare" && value.status === "configured");
  return kinds.find((kind) => {
    if (kind === "cloudflare_tunnel" && !cloudflareReady) return false;
    return gatewaysForKind(data, service, kind).length > 0;
  });
}

export function gatewaysForKind(data: AppData, service: Service, kind: PublicationKind): AgentView[] {
  const app = data.applications.find((value) => value.id === service.applicationId);
  const site = data.sites.find((value) => value.id === service.siteId);
  return data.agents.filter((agent) => {
    if (!agent.connected || agent.siteId !== service.siteId || !agent.networkProfile) return false;
    if (kind === "cloudflare_tunnel") return agent.capabilities.tunnel;
    if (kind === "public_direct" && service.protocol !== "http" && service.protocol !== "https") return agent.id === app?.nodeId && agent.networkProfile.directPublic && agent.networkProfile.enabledKinds.includes("public");
    if (!agent.capabilities.gateway || !site?.gatewayNodes.includes(agent.id)) return false;
    if (kind === "lan_gateway") return agent.networkProfile.enabledKinds.includes("lan");
    if (kind === "headscale_gateway") return agent.networkProfile.enabledKinds.includes("headscale");
    return agent.networkProfile.directPublic && agent.networkProfile.enabledKinds.includes("public");
  });
}

export function canInstall(agent: AgentView) {
  return agent.connected && agent.capabilities.docker && Boolean(agent.networkProfile);
}

export function isActiveApplication(status: string) {
  return ["pending", "deploying", "running"].includes(status);
}

export function isInstalledApplication(application: AppData["applications"][number]) {
  return Boolean(application.installedVersion);
}

export function latestOperations(deployments: Deployment[]) {
  const seen = new Set<string>();
  return deployments.filter((deployment) => {
    const key = `${deployment.agentId}\n${deployment.appKey}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

export function defaultPublicationHostname(data: AppData, service: Service) {
  const application = data.applications.find((value) => value.id === service.applicationId);
  const site = data.sites.find((value) => value.id === service.siteId);
  if (!site?.domainSuffix) return "";
  const appLabel = dnsLabel(application?.appKey.split("/").at(-1) || application?.name || "app");
  const serviceLabel = dnsLabel(service.name) || "service";
  const siteLabel = dnsLabel(site.code) || dnsLabel(site.name) || "site";
  return `${dnsLabel(`${serviceLabel}-${appLabel}`)}.${siteLabel}.${site.domainSuffix}`.toLowerCase();
}

export function defaultRealityHostname(data: AppData, application: AppData["applications"][number]) {
  const site = data.sites.find((value) => value.id === application.siteId);
  const agent = data.agents.find((value) => value.id === application.nodeId);
  if (!site?.domainSuffix) return "";
  return `reality.${dnsLabel(agent?.name || "node")}.${dnsLabel(site.code) || "site"}.${site.domainSuffix}`.toLowerCase();
}

function dnsLabel(value: string) {
  return value.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
}

export function operationLabel(language: Language, operation: Deployment["operation"]) {
  const labels: Record<Deployment["operation"], [string, string]> = { install: ["安装", "Install"], upgrade: ["升级", "Upgrade"], configure: ["修改配置", "Configure"], uninstall: ["卸载", "Uninstall"] };
  return copy(language, ...labels[operation]);
}

export function installBlocker(data: AppData, appKey: string, language: Language) {
  if (data.agents.length === 0) return copy(language, "先添加一台节点，再安装应用。", "Add a node before installing apps.");
  if (!data.agents.some((agent) => agent.connected)) return copy(language, "没有在线节点。请检查 Agent 服务。", "No node is online. Check the Agent service.");
  if (!data.agents.some((agent) => agent.connected && agent.capabilities.docker)) return copy(language, "在线节点没有 Docker 应用能力。", "Online nodes do not provide Docker app capability.");
  if (!data.agents.some((agent) => agent.connected && agent.capabilities.docker && agent.networkProfile)) return copy(language, "请先在“网络”页面确认节点地址。", "Confirm a node address on the Network page first.");
  if (data.agents.every((agent) => !canInstall(agent) || data.applications.some((application) => application.nodeId === agent.id && application.appKey === appKey && (isInstalledApplication(application) || isActiveApplication(application.status))))) return copy(language, "所有可用节点都已安装或正在安装此应用。", "This app is already installed or being installed on every eligible node.");
  return copy(language, "当前没有符合条件的节点。", "No eligible node is currently available.");
}

export function localized(app: AppView, language: Language, field: "name" | "description") {
  return app.app[field][language] || app.app[field].en;
}

export function publicationKindLabel(language: Language, kind: PublicationKind) {
  const labels: Record<PublicationKind, [string, string]> = { lan_gateway: ["局域网访问", "Local network"], headscale_gateway: ["安全私网", "Secure private network"], public_direct: ["公网直连", "Direct public"], public_shared_443: ["共享 443（公网）", "Shared 443 (public)"], cloudflare_tunnel: ["Cloudflare 安全通道", "Cloudflare secure tunnel"] };
  return copy(language, ...labels[kind]);
}
