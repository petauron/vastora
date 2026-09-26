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

export function LinkBandwidthSummary({ check, language, unavailable, showUnit = true }: { check?: NodeDiagnosticCheck; language: Language; unavailable?: boolean; showUnit?: boolean }) {
  const active = check?.state === "pending" || check?.state === "running";
  const failed = check?.state === "failed" || Boolean(check?.error);
  const valid = !unavailable && !active && !failed && check?.state === "succeeded" && check.link;
  const label = unavailable ? copy(language, "读取失败", "Unavailable") : active ? copy(language, "测速中…", "Testing…") : failed ? copy(language, "测速失败", "Test failed") : copy(language, "未测速", "Not tested");
  const stamp = check?.checkedAt || check?.updatedAt;
  const description = valid ? copy(language, `入口→落地 ${valid.uploadMbps.toFixed(1)} Mbps；落地→入口 ${valid.downloadMbps.toFixed(1)} Mbps`, `Entry → landing ${valid.uploadMbps.toFixed(1)} Mbps; landing → entry ${valid.downloadMbps.toFixed(1)} Mbps`) : label;
  const title = `${description}${stamp ? ` · ${new Date(stamp).toLocaleString(language)}` : ""}`;
  return <span className={`shrink-0 whitespace-nowrap text-right ${valid ? "text-foreground" : failed || unavailable ? "text-destructive" : active ? "quality-grade-good" : "text-muted-foreground"}`} title={title} aria-label={title}>
    {valid ? <><span className={bandwidthColor(valid.uploadMbps)}>↑{valid.uploadMbps.toFixed(1)}</span>{" "}<span className={bandwidthColor(valid.downloadMbps)}>↓{valid.downloadMbps.toFixed(1)}</span> {showUnit ? <span className="text-muted-foreground">Mbps</span> : null}</> : label}
  </span>;
}
