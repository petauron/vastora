import type { AgentReinstallEntryCheck } from "../types";
import type { Language } from "../translations";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { copy, formatDate } from "./shared";

export function ReinstallEntryCheck({ check, busy, checking, ready, language, onVerify }: { check?: AgentReinstallEntryCheck; busy: boolean; checking: boolean; ready: boolean; language: Language; onVerify: () => Promise<void> }) {
  return <div className="flex flex-col gap-2">
    <div className="flex flex-wrap items-center justify-between gap-2">
      <p className="text-xs text-muted-foreground" role="status">
        {checking ? copy(language, "正在验证 DNS 与 TLS 入口", "Checking DNS and TLS entry") : check ? !check.current ? copy(language, "检查结果已失效 · 请重新验证", "Check is stale · verify again") : check.state === "passed" ? copy(language, "DNS 与 TLS 已通过 · 真实客户端访问待验证", "DNS and TLS passed · authenticated client verification pending") : copy(language, "公网入口未通过 · 修正后重新验证", "Public entry check incomplete · correct it and verify again") : copy(language, "检查新公网地址的 DNS 与 TLS 入口。", "Check DNS and TLS on the replacement public address.")}
      </p>
      <Button variant="outline" disabled={busy || !ready} onClick={() => void onVerify()}>
        {checking ? <Spinner data-icon="inline-start" aria-hidden="true" /> : null}
        {copy(language, check ? "重新验证入口" : "验证公网入口", check ? "Recheck entry" : "Check public entry")}
      </Button>
    </div>
    {check ? <details className="text-xs text-muted-foreground">
      <summary className="cursor-pointer">{copy(language, "检查详情", "Check details")} · {formatDate(language, check.checkedAt)}</summary>
      <ul className="mt-2 flex flex-col gap-2">
        {check.entries.map((entry) => <li key={entry.publicationId} className="break-words">
          {entry.hostname} → {entry.publicAddress} · {entry.state === "passed" ? copy(language, "DNS / TLS 通过", "DNS / TLS passed") : entry.state === "dns_pending" ? copy(language, "DNS 尚未全部指向新公网地址", "DNS does not exclusively resolve to the replacement address") : entry.state === "tls_pending" ? copy(language, "TLS 1.3 校验未通过", "TLS 1.3 check did not pass") : copy(language, "前项未通过，尚未检查", "Not checked after an earlier failure")}
        </li>)}
      </ul>
    </details> : null}
  </div>;
}
