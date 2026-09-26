import type { IPQualityCheck } from "../ip-quality-types";
import type { Language } from "../translations";
import { copy } from "./shared";

// Reports contain terminal ESC sequences that must be removed before display.
// eslint-disable-next-line no-control-regex
const ansiEscapePattern = /\u001b\[[0-?]*[ -/]*[@-~]/g;
const serializedAnsiEscapePattern = /\\?x1b\[[0-?]*[ -/]*[@-~]/gi;

export function cleanIPQualityValue(value?: string) {
  return (value ?? "").replace(ansiEscapePattern, "").replace(serializedAnsiEscapePattern, "").replace(/\s+/g, " ").trim();
}

type ClassificationTone = "good" | "warning" | "bad" | "neutral";
export const unlockServices = ["ChatGPT", "Netflix", "DisneyPlus", "Youtube", "AmazonPrimeVideo", "TikTok", "Reddit"] as const;

export function unlockServiceLabel(name: string, compact = false) {
  const labels: Record<string, [string, string]> = {
    ChatGPT: ["GPT", "ChatGPT"], Netflix: ["NF", "Netflix"], DisneyPlus: ["D+", "Disney+"],
    Youtube: ["YT", "YouTube"], AmazonPrimeVideo: ["Prime", "Prime Video"], TikTok: ["TikTok", "TikTok"], Reddit: ["Reddit", "Reddit"],
  };
  return labels[name]?.[compact ? 0 : 1] ?? name;
}
const classificationLabels: Record<string, [string, string, ClassificationTone]> = {
  business: ["商业", "Business", "warning"], commercial: ["商业", "Business", "warning"],
  isp: ["家宽", "ISP", "good"], "line isp": ["家宽", "Line ISP", "good"],
  hosting: ["机房", "Hosting", "bad"],
  education: ["教育", "Education", "warning"], government: ["政府", "Government", "warning"],
  banking: ["银行", "Banking", "warning"], organization: ["组织", "Organization", "warning"],
  military: ["军队", "Military", "warning"], library: ["图书馆", "Library", "warning"],
  cdn: ["CDN", "CDN", "bad"], mobile: ["手机", "Mobile ISP", "good"], "mobile isp": ["手机", "Mobile ISP", "good"],
  spider: ["蜘蛛", "Web Spider", "bad"], "web spider": ["蜘蛛", "Web Spider", "bad"],
  reserved: ["保留", "Reserved", "warning"], other: ["其他", "Other", "warning"],
};

export function ipClassification(language: Language, raw: string): { label: string; tone: ClassificationTone } {
  const value = cleanIPQualityValue(raw);
  const known = classificationLabels[value.toLowerCase()];
  return known ? { label: copy(language, known[0], known[1]), tone: known[2] } : { label: value || "—", tone: "neutral" };
}

export function checkPending(check?: IPQualityCheck) {
  return check?.state === "pending" || check?.state === "running";
}

export function unlockLabel(language: Language, status?: string) {
  const value = cleanIPQualityValue(status);
  switch (value.toLowerCase()) {
    case "yes": return copy(language, "解锁", "Unlocked");
    case "no": case "block": return copy(language, "未解锁", "Blocked");
    case "org": case "originals only": case "nf.only": return copy(language, "仅自制", "Originals only");
    case "apponly": return copy(language, "仅 APP", "App only");
    case "webonly": return copy(language, "仅网页", "Web only");
    case "china": return copy(language, "中国", "China");
    case "noprem.": return copy(language, "禁会员", "No Premium");
    case "pending": return copy(language, "待支持", "Pending support");
    case "idc": return copy(language, "机房", "IDC");
    case "failed": case "fail": case "error": return copy(language, "检测失败", "Check failed");
    case "": case "null": return copy(language, "未知", "Unknown");
    default: return value;
  }
}

export function unlockTypeLabel(language: Language, type?: string) {
  const value = cleanIPQualityValue(type);
  if (!value || value.toLowerCase() === "null") return "";
  return value.toLowerCase() === "native" ? copy(language, "原生", "Native") : value;
}

export function ipQualityError(language: Language, code: string) {
  const labels: Record<string, [string, string]> = {
    ip_quality_node_unavailable: ["节点已停用或不可用。", "The node is disabled or unavailable."],
    ip_quality_node_offline: ["节点离线，上线后才能检测。", "The node must be online to run a check."],
    ip_quality_agent_upgrade_required: ["需要升级 Agent，并启用 Docker。", "Upgrade the Agent and enable Docker."],
    ip_quality_target_required: ["IP 质量仅适用于 VLESS 节点和落地机。", "IP quality is available only for VLESS nodes and landing servers."],
    ip_quality_tasks_paused: ["任务领取已暂停，请先在活动中恢复。", "Task claims are paused. Resume them in Activity."],
    ip_quality_node_busy: ["节点已有执行中或待处理任务，请先在活动中查看。", "The node has an active or unresolved task. Check Activity first."],
    ip_quality_address_unavailable: ["节点尚未上报有效的公网出口。", "The node has not reported a valid public egress address."],
    docker_unavailable: ["无法启动检测容器，请检查节点 Docker。", "Could not start the check container. Check Docker on the node."],
    download_failed: ["检测镜像下载失败。", "Could not download the check image."],
    detection_failed: ["检测未完成，可能是检测服务无法访问。", "The check did not finish; a detection service may be unreachable."],
    timeout: ["检测超时，未生成新结果。", "The check timed out without a new result."],
    ip_changed: ["实际出口与节点上报的 IP 不一致，未保存结果。", "The observed exit differs from the node's IP. No result was saved."],
    invalid_report: ["检测服务未返回可识别的结果。", "The check did not return a valid report."],
    interrupted: ["执行被中断或结果未确认，请在活动中核实。", "Execution was interrupted or unconfirmed. Review Activity."],
    abandoned: ["检测任务已放弃。", "The check was abandoned."],
  };
  return labels[code] ? copy(language, ...labels[code]) : copy(language, "暂时无法读取检测状态，请刷新；不要重复提交。", "Could not read the check state. Refresh before submitting again.");
}

export function ipQualitySummary(language: Language, check?: IPQualityCheck) {
  if (checkPending(check)) return check?.state === "pending" ? copy(language, "IP 质量 · 等待检测", "IP quality · Queued") : copy(language, "IP 质量 · 检测中", "IP quality · Checking");
  if (check?.error) return copy(language, "IP 质量 · 检测未完成", "IP quality · Check incomplete");
  if (check?.stale) return copy(language, "IP 质量 · 需重新检测", "IP quality · Stale result");
  if (check?.assessment?.status === "expired" || check?.assessment?.status === "ip_changed") return copy(language, "IP 质量 · 需重新检测", "IP quality · Stale result");
  if (!check?.report) return copy(language, "IP 质量 · 未检测", "IP quality · Not checked");
  const services = unlockServices.map((name) => {
    const service = check.report!.services.find((item) => item.name === name);
    return `${unlockServiceLabel(name)} ${unlockLabel(language, service?.status)}`;
  });
  return services.join(" · ");
}
