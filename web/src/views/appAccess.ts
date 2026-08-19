import type { DashboardData } from "../types";
import type { AgentView, AppView, Deployment, PublicationKind, Service } from "../types";
import type { Language } from "../translations";
import { copy } from "./shared";

export function publicationOptions(data: DashboardData, service: Service | null, language: Language) {
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
    options.push({ kind: "lan_gateway", enabled: lan, reason: lan ? "HTTP · LAN Gateway" : copy(language, "没有可用的局域网 Gateway", "No LAN Gateway is available") });
    options.push({ kind: "headscale_gateway", enabled: headscale, reason: headscale ? "HTTP · Headscale Gateway" : copy(language, "没有可用的 Headscale Gateway", "No Headscale Gateway is available") });
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

export function gatewaysForKind(data: DashboardData, service: Service, kind: PublicationKind): AgentView[] {
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

export function latestOperations(deployments: Deployment[]) {
  const seen = new Set<string>();
  return deployments.filter((deployment) => {
    const key = `${deployment.agentId}\n${deployment.appKey}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

export function operationLabel(language: Language, operation: Deployment["operation"]) {
  const labels: Record<Deployment["operation"], [string, string]> = { install: ["安装", "Install"], upgrade: ["升级", "Upgrade"], uninstall: ["卸载", "Uninstall"] };
  return copy(language, ...labels[operation]);
}

export function installBlocker(data: DashboardData, appKey: string, language: Language) {
  if (data.agents.length === 0) return copy(language, "先添加一台节点，再安装应用。", "Add a node before installing apps.");
  if (!data.agents.some((agent) => agent.connected)) return copy(language, "没有在线节点。请检查 Agent 服务。", "No node is online. Check the Agent service.");
  if (!data.agents.some((agent) => agent.connected && agent.capabilities.docker)) return copy(language, "在线节点没有 Docker 应用能力。", "Online nodes do not provide Docker app capability.");
  if (!data.agents.some((agent) => agent.connected && agent.capabilities.docker && agent.networkProfile)) return copy(language, "请先在“网络”页面确认节点地址。", "Confirm a node address on the Network page first.");
  if (data.agents.every((agent) => !canInstall(agent) || data.applications.some((application) => application.nodeId === agent.id && application.appKey === appKey && isActiveApplication(application.status)))) return copy(language, "所有可用节点都已安装此应用。", "This app is already installed on every eligible node.");
  return copy(language, "当前没有符合条件的节点。", "No eligible node is currently available.");
}

export function localized(app: AppView, language: Language, field: "name" | "description") {
  return app.app[field][language] || app.app[field].en;
}

export function publicationKindLabel(language: Language, kind: PublicationKind) {
  const labels: Record<PublicationKind, [string, string]> = { lan_gateway: ["局域网", "Local network"], headscale_gateway: ["Headscale 私网", "Headscale private network"], public_direct: ["公网直连", "Direct public"], public_shared_443: ["共享 443", "Shared 443"], cloudflare_tunnel: ["Cloudflare Tunnel", "Cloudflare Tunnel"] };
  return copy(language, ...labels[kind]);
}
