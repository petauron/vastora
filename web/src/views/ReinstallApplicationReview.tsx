import type { AgentReinstallPlan } from "../types";
import type { Language } from "../translations";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { ReinstallDNSReview } from "./ReinstallDNSReview";
import { ReinstallEntryCheck } from "./ReinstallEntryCheck";
import { ReinstallLandingReview } from "./ReinstallLandingReview";
import { copy } from "./shared";

export function ReinstallApplicationReview({ plan, busy, language, onPrepare, onLanding, onRestoreRuntime, onRestoreListener, onVerifyEntry, checkingEntry, onActivateAccess, onDNS, workingDNS }: { onLanding: (applicationId: string, authorize: boolean) => Promise<void>; plan: AgentReinstallPlan; busy: boolean; language: Language; onPrepare: (applicationId: string) => Promise<void>; onRestoreRuntime: (applicationId: string) => Promise<void>; onRestoreListener: (applicationId: string) => Promise<void>; onVerifyEntry: (applicationId: string) => Promise<void>; checkingEntry: string | null; onActivateAccess: (applicationId: string) => Promise<void>; onDNS: (applicationId: string, action: "migrate" | "inspect", expectedAttempt: number) => Promise<void>; workingDNS: string | null }) {
  const apps = plan.applications.filter((app) => app.appKey === "vastora-official/meridian" && app.recovery === "rebuild_configuration");
  if (!apps.length) return null;
  const ready = plan.networkReview?.approvalCurrent && !plan.executions.length && !plan.unclaimedLocalWork.length;
  return <section className="flex flex-col gap-3 rounded-xl border p-4" aria-label={copy(language, "准备应用", "Prepare applications")}>
    <p className="text-sm">{copy(language, "恢复应用", "Restore applications")}</p>
    <p className="text-xs text-muted-foreground">{copy(language, "先准备原版本镜像，接替落地授权，再恢复运行配置和入口。保留原账号和密钥，完成后仍需验证客户端访问。", "Prepare the saved image, replace landing authorization, then restore runtime and entry. Original credentials are retained; client access still needs verification.")}</p>
    {apps.map((app) => {
      const state = app.preparation?.state;
      const runtime = app.preparation?.runtime;
      const landing = app.preparation?.landing;
      const listener = app.preparation?.listener;
      const access = app.preparation?.access;
      const canActivate = runtime?.state === "succeeded" && (!app.sharedEntry && !listener || listener?.state === "succeeded");
      const pending = state === "pending" || state === "running";
      return <div key={app.applicationId} className="flex flex-col gap-2 text-sm"><div className="flex items-center justify-between gap-3">
        <span>{app.name} <span className="text-xs text-muted-foreground">{app.version}</span></span>
        {state ? <span className="flex items-center gap-2 text-xs text-muted-foreground" role="status">{pending ? <Spinner /> : null}{state === "succeeded" ? runtime ? copy(language, "镜像已准备", "Image prepared") : copy(language, "镜像已准备 · 运行配置待恢复", "Image prepared · runtime restoration pending") : pending ? copy(language, "正在准备", "Preparing") : state === "needs_review" ? copy(language, "结果未确认 · 需核对", "Outcome uncertain · inspect before continuing") : copy(language, "准备失败 · 需核对结果", "Preparation failed · inspect the outcome")}</span> : <Button variant="outline" disabled={busy || !ready} onClick={() => void onPrepare(app.applicationId)}>{copy(language, "准备原版本", "Prepare saved version")}</Button>}
      </div>
        {state === "succeeded" && landing ? <ReinstallLandingReview value={landing} language={language} disabled={busy || !ready || !!runtime} onAction={(authorize) => onLanding(app.applicationId, authorize)} /> : null}
        {state === "succeeded" ? runtime ? <p className="flex items-center gap-2 text-xs text-muted-foreground" role="status">{runtime.state === "pending" || runtime.state === "running" ? <Spinner /> : null}{runtime.state === "succeeded" ? copy(language, "本机配置已恢复 · 入口与落地待验证", "Native runtime restored · access and landing verification pending") : runtime.state === "pending" || runtime.state === "running" ? copy(language, "正在恢复运行配置", "Restoring runtime configuration") : copy(language, "运行配置结果需核对", "Inspect runtime restoration outcome")}</p> : <Button className="self-end" variant="outline" disabled={busy || !ready || !!landing && landing.state !== "authorized"} onClick={() => void onRestoreRuntime(app.applicationId)}>{copy(language, "恢复运行配置", "Restore runtime configuration")}</Button> : null}
        {runtime?.state === "succeeded" && (app.sharedEntry || listener) ? listener ? <p className="flex items-center gap-2 text-xs text-muted-foreground" role="status">{listener.state === "pending" || listener.state === "running" ? <Spinner /> : null}{listener.state === "succeeded" ? app.preparation?.entryCheck?.current && app.preparation.entryCheck.state === "passed" ? copy(language, "入口已应用", "Entry applied") : copy(language, "入口已应用 · 公网访问待验证", "Entry applied · public access verification pending") : listener.state === "pending" || listener.state === "running" ? copy(language, "正在恢复入口", "Restoring entry") : copy(language, "入口恢复结果需核对", "Inspect entry restoration outcome")}</p> : plan.networkReview?.approval?.profile.directPublic ? <Button className="self-end" variant="outline" disabled={busy || !ready} onClick={() => void onRestoreListener(app.applicationId)}>{copy(language, "恢复入口", "Restore entry")}</Button> : <p className="text-xs text-muted-foreground">{copy(language, "恢复共享入口前，需确认新机器的公网入口地址。", "Approve the replacement public ingress before restoring its shared entry.")}</p> : null}
        {canActivate ? access ? <p role="status" className="text-xs text-muted-foreground">{access.state === "applied" ? copy(language, "恢复地址已启用 · DNS、落地与客户端访问待验证", "Recovery addresses activated · DNS, landing and client access verification pending") : copy(language, "恢复地址已变化 · 需核对，未重新应用", "Recovery addresses changed · inspect before reapplying")}</p> : <Button className="self-end" variant="outline" disabled={busy || !ready} onClick={() => void onActivateAccess(app.applicationId)}>{copy(language, "启用恢复地址", "Activate recovery addresses")}</Button> : null}
        {access?.state === "applied" && app.preparation?.dns ? <ReinstallDNSReview dns={app.preparation.dns} busy={busy} working={workingDNS === app.applicationId} ready={Boolean(ready)} language={language} onAction={(action, attempt) => onDNS(app.applicationId, action, attempt)} /> : null}
        {listener?.state === "succeeded" && runtime?.state === "succeeded" && access?.state === "applied" ? <ReinstallEntryCheck check={app.preparation?.entryCheck} busy={busy} checking={checkingEntry === app.applicationId} ready={!!ready} language={language} onVerify={() => onVerifyEntry(app.applicationId)} /> : null}
      </div>;
    })}
    {!ready && apps.some((app) => !app.preparation || app.preparation.state === "succeeded" && !app.preparation.runtime) ? <p className="text-xs text-muted-foreground">{copy(language, "先核对旧任务并确认新机器地址。", "Review previous work and approve the replacement address first.")}</p> : null}
  </section>;
}
