import type { NodeDiagnosticCheck } from "../node-diagnostics-types";
import type { Language } from "../translations";
import { copy } from "./shared";

export const bandwidthBands = [
  { minimum: 500, label: "≥500", className: "quality-grade-excellent" },
  { minimum: 100, label: "100–<500", className: "quality-grade-good" },
  { minimum: 10, label: "10–<100", className: "quality-grade-fair" },
  { minimum: 0, label: "<10", className: "quality-grade-poor" },
] as const;

function bandwidthColor(mbps: number) {
  return bandwidthBands.find((band) => mbps >= band.minimum)?.className ?? "text-muted-foreground";
}

export function linkBandwidthStatus(check?: NodeDiagnosticCheck, now = Date.now()) {
  if (check?.state === "pending" || check?.state === "running") return "active";
  if (check?.error === "transport_not_direct") return "non_direct";
  if (check?.error === "peer_identity_changed") return "identity_changed";
  if (check?.error === "expired") return "expired";
  if (check?.error === "transport_unverified") return "unverified";
  if (check?.state === "failed" || check?.error) return "failed";
  if (check?.state !== "succeeded" || !check.link) return "missing";
  if (check.link.transportState !== "direct") return "unverified";
  const checked = Date.parse(check.checkedAt ?? "");
  if (!Number.isFinite(checked) || checked > now || now - checked > 24 * 60 * 60 * 1000) return "expired";
  return "succeeded";
}

export function linkBandwidthStatusLabel(status: ReturnType<typeof linkBandwidthStatus>, language: Language) {
  switch (status) {
    case "active": return copy(language, "测速中…", "Testing…");
    case "non_direct": return copy(language, "私网未直连", "Private link is not direct");
    case "identity_changed": return copy(language, "节点已变化，需重测", "Node changed; retest required");
    case "unverified": return copy(language, "旧结果需重测", "Retest required");
    case "expired": return copy(language, "结果已过期", "Result expired");
    case "failed": return copy(language, "测速失败", "Test failed");
    case "missing": return copy(language, "未测速", "Not tested");
    default: return "";
  }
}

export function LinkBandwidthSummary({ check, language, unavailable, showUnit = true }: { check?: NodeDiagnosticCheck; language: Language; unavailable?: boolean; showUnit?: boolean }) {
  const status = linkBandwidthStatus(check);
  const active = status === "active";
  const failed = ["failed", "non_direct", "identity_changed"].includes(status);
  const valid = !unavailable && status === "succeeded" && check?.link;
  const label = unavailable ? copy(language, "读取失败", "Unavailable") : linkBandwidthStatusLabel(status, language);
  const stamp = check?.checkedAt || check?.updatedAt;
  const description = valid ? copy(language, `入口→落地 ${valid.uploadMbps.toFixed(1)} Mbps；落地→入口 ${valid.downloadMbps.toFixed(1)} Mbps`, `Entry → landing ${valid.uploadMbps.toFixed(1)} Mbps; landing → entry ${valid.downloadMbps.toFixed(1)} Mbps`) : label;
  const title = `${description}${stamp ? ` · ${new Date(stamp).toLocaleString(language)}` : ""}`;
  return <span className={`shrink-0 whitespace-nowrap text-right ${valid ? "text-foreground" : failed || unavailable ? "text-destructive" : active ? "quality-grade-good" : "text-muted-foreground"}`} title={title} aria-label={title}>
    {valid ? <><span className={bandwidthColor(valid.uploadMbps)}>↑{valid.uploadMbps.toFixed(1)}</span>{" "}<span className={bandwidthColor(valid.downloadMbps)}>↓{valid.downloadMbps.toFixed(1)}</span> {showUnit ? <span className="text-muted-foreground">Mbps</span> : null}</> : label}
  </span>;
}
