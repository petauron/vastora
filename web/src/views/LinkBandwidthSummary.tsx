import type { NodeDiagnosticCheck } from "../node-diagnostics-types";
import type { Language } from "../translations";
import { copy } from "./shared";

export function LinkBandwidthSummary({ check, language, unavailable, showUnit = true }: { check?: NodeDiagnosticCheck; language: Language; unavailable?: boolean; showUnit?: boolean }) {
  const active = check?.state === "pending" || check?.state === "running";
  const failed = check?.state === "failed" || Boolean(check?.error);
  const valid = !unavailable && !active && !failed && check?.state === "succeeded" && check.link;
  const label = unavailable ? copy(language, "读取失败", "Unavailable") : active ? copy(language, "测速中…", "Testing…") : failed ? copy(language, "测速失败", "Test failed") : copy(language, "未测速", "Not tested");
  const stamp = check?.checkedAt || check?.updatedAt;
  const description = valid ? copy(language, `入口→落地 ${valid.uploadMbps.toFixed(1)} Mbps；落地→入口 ${valid.downloadMbps.toFixed(1)} Mbps`, `Entry → landing ${valid.uploadMbps.toFixed(1)} Mbps; landing → entry ${valid.downloadMbps.toFixed(1)} Mbps`) : label;
  const title = `${description}${stamp ? ` · ${new Date(stamp).toLocaleString(language)}` : ""}`;
  return <span className={`shrink-0 whitespace-nowrap text-right ${valid ? "text-foreground" : failed || unavailable ? "text-destructive" : "text-muted-foreground"}`} title={title} aria-label={title}>
    {valid ? <>↑{valid.uploadMbps.toFixed(1)} ↓{valid.downloadMbps.toFixed(1)} {showUnit ? <span className="text-muted-foreground">Mbps</span> : null}</> : label}
  </span>;
}
