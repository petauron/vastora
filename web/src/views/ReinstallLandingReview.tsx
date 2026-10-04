import type { AgentReinstallLandingSource } from "../types";
import type { Language } from "../translations";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { copy } from "./shared";

export function ReinstallLandingReview({ value, language, disabled, onAction }: { value: AgentReinstallLandingSource; language: Language; disabled: boolean; onAction: (authorize: boolean) => Promise<void> }) {
  const waiting = value.state === "withdrawing" || value.state === "authorizing";
  const label = value.state === "pending" ? copy(language, "旧落地授权待撤销", "Previous landing access needs withdrawal")
    : value.state === "withdrawing" ? copy(language, "等待落地确认撤销", "Waiting for landing withdrawal receipts")
    : value.state === "withdrawn" ? copy(language, "旧授权已撤销 · 待授权新身份", "Previous access withdrawn · replacement authorization pending")
    : value.state === "authorizing" ? copy(language, "等待落地确认新授权", "Waiting for replacement authorization receipts")
    : value.state === "authorized" ? copy(language, "新身份授权已应用 · 线路访问待验证", "Replacement authorization applied · route verification pending")
    : copy(language, "落地授权结果需核对", "Inspect landing authorization evidence");
  return <div className="flex flex-col gap-2 rounded-lg border p-3" aria-label={copy(language, "落地身份接替", "Landing identity replacement")}>
    <p role="status" className="flex items-center gap-2 text-xs text-muted-foreground">{waiting ? <Spinner /> : null}{label}</p>
    {value.identity.previousAddress && value.identity.currentAddress ? <p className="break-all text-xs text-muted-foreground">{value.identity.previousAddress} → {value.identity.currentAddress} · {copy(language, `${value.landings.length} 个落地`, `${value.landings.length} landings`)}</p> : null}
    {value.state === "pending" || value.state === "withdrawn" ? <Button className="self-end" variant="outline" disabled={disabled} onClick={() => void onAction(value.state === "withdrawn")}>{value.state === "pending" ? copy(language, "撤销旧落地授权", "Withdraw previous landing access") : copy(language, "授权新机器身份", "Authorize replacement identity")}</Button> : null}
  </div>;
}
