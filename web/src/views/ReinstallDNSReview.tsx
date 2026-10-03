import type { AgentReinstallDNS } from "../types";
import type { Language } from "../translations";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { copy } from "./shared";

export function ReinstallDNSReview({ dns, busy, working, ready, language, onAction }: { dns: AgentReinstallDNS; busy: boolean; working: boolean; ready: boolean; language: Language; onAction: (action: "migrate" | "inspect", attempt: number) => Promise<void> }) {
  const managed = dns.entries.some((entry) => entry.provider === "cloudflare");
  const blocked = dns.entries.some((entry) => entry.state === "blocked");
  const disabled = busy || !ready || !dns.current || blocked;
  const succeeded = dns.current && dns.state === "succeeded";
  return <div className="flex flex-col gap-2">
    <div className="flex flex-wrap items-center justify-between gap-2">
      <p className="flex items-center gap-2 text-xs text-muted-foreground" role="status">
        {working ? <Spinner /> : null}
        {working ? copy(language, "正在处理 DNS", "Processing DNS") : !dns.current ? copy(language, "DNS 恢复配置已变化 · 需重新核对", "DNS recovery configuration changed · review required") : blocked ? copy(language, "DNS 归属缺失或冲突 · 需核对", "DNS ownership missing or conflicting · review required") : succeeded ? copy(language, "受管 DNS 已更新 · 公网访问待验证", "Managed DNS updated · public access verification pending") : dns.id ? copy(language, "DNS 迁移结果需核对", "Inspect DNS migration outcome") : managed ? copy(language, "将已托管的 DNS 记录迁移到恢复地址。", "Move existing managed DNS records to the recovery address.") : copy(language, "按下方地址更新 DNS 后，验证公网入口。", "Update DNS to the address below, then verify the public entry.")}
      </p>
      {managed && !succeeded ? <div className="flex flex-wrap gap-2">
        {dns.id ? <Button variant="outline" disabled={disabled} onClick={() => void onAction("inspect", dns.attempt)}>{copy(language, "核对 DNS 结果", "Inspect DNS result")}</Button> : <Button variant="outline" disabled={disabled} onClick={() => void onAction("migrate", 0)}>{copy(language, "迁移 DNS", "Migrate DNS")}</Button>}
        {dns.canContinue ? <Button variant="outline" disabled={disabled} onClick={() => void onAction("migrate", dns.attempt)}>{copy(language, "继续 DNS 迁移", "Continue DNS migration")}</Button> : null}
      </div> : null}
    </div>
    <details className="text-xs text-muted-foreground">
      <summary className="cursor-pointer">{copy(language, "DNS 记录", "DNS records")}</summary>
      <ul className="mt-2 flex flex-col gap-1">{dns.entries.map((entry) => <li key={entry.publicationId} className="break-words">{entry.hostname} · A · {entry.address} · {entry.provider === "manual" ? copy(language, "手动配置", "Manual") : entry.state === "applied" ? copy(language, "已确认", "Confirmed") : entry.state === "pending" ? copy(language, "待迁移", "Pending") : entry.state === "conflict" ? copy(language, "记录不符", "Record conflict") : copy(language, "待核对", "Review required")}</li>)}</ul>
    </details>
  </div>;
}
