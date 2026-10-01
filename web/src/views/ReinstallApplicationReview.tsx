import type { AgentReinstallPlan } from "../types";
import type { Language } from "../translations";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { copy } from "./shared";

export function ReinstallApplicationReview({ plan, busy, language, onPrepare, onRestoreRuntime }: { plan: AgentReinstallPlan; busy: boolean; language: Language; onPrepare: (applicationId: string) => Promise<void>; onRestoreRuntime: (applicationId: string) => Promise<void> }) {
  const apps = plan.applications.filter((app) => app.appKey === "vastora-official/meridian" && app.recovery === "rebuild_configuration");
  if (!apps.length) return null;
  const ready = plan.networkReview?.approvalCurrent && !plan.executions.length && !plan.unclaimedLocalWork.length;
  return <section className="flex flex-col gap-3 rounded-xl border p-4" aria-label={copy(language, "准备应用", "Prepare applications")}>
    <p className="text-sm">{copy(language, "恢复应用", "Restore applications")}</p>
    <p className="text-xs text-muted-foreground">{copy(language, "先准备原版本 Meridian 镜像，随后恢复运行配置并验证入口。保留原账号和密钥，落地线路待单独核对。", "Prepare the saved Meridian image before restoring runtime configuration and verifying access. Original credentials are retained; landing routes require separate review.")}</p>
    {apps.map((app) => {
      const state = app.preparation?.state;
      const runtime = app.preparation?.runtime;
      const pending = state === "pending" || state === "running";
      return <div key={app.applicationId} className="flex flex-col gap-2 text-sm"><div className="flex items-center justify-between gap-3">
        <span>{app.name} <span className="text-xs text-muted-foreground">{app.version}</span></span>
        {state ? <span className="flex items-center gap-2 text-xs text-muted-foreground" role="status">{pending ? <Spinner /> : null}{state === "succeeded" ? runtime ? copy(language, "镜像已准备", "Image prepared") : copy(language, "镜像已准备 · 运行配置待恢复", "Image prepared · runtime restoration pending") : pending ? copy(language, "正在准备", "Preparing") : state === "needs_review" ? copy(language, "结果未确认 · 需核对", "Outcome uncertain · inspect before continuing") : copy(language, "准备失败 · 需核对结果", "Preparation failed · inspect the outcome")}</span> : <Button variant="outline" disabled={busy || !ready} onClick={() => void onPrepare(app.applicationId)}>{copy(language, "准备原版本", "Prepare saved version")}</Button>}
      </div>
        {state === "succeeded" ? runtime ? <p className="flex items-center gap-2 text-xs text-muted-foreground" role="status">{runtime.state === "pending" || runtime.state === "running" ? <Spinner /> : null}{runtime.state === "succeeded" ? copy(language, "本机配置已恢复 · 入口与落地待验证", "Native runtime restored · access and landing verification pending") : runtime.state === "pending" || runtime.state === "running" ? copy(language, "正在恢复运行配置", "Restoring runtime configuration") : copy(language, "运行配置结果需核对", "Inspect runtime restoration outcome")}</p> : <Button className="self-end" variant="outline" disabled={busy || !ready} onClick={() => void onRestoreRuntime(app.applicationId)}>{copy(language, "恢复运行配置", "Restore runtime configuration")}</Button> : null}
      </div>;
    })}
    {!ready && apps.some((app) => !app.preparation || app.preparation.state === "succeeded" && !app.preparation.runtime) ? <p className="text-xs text-muted-foreground">{copy(language, "先核对旧任务并确认新机器地址。", "Review previous work and approve the replacement address first.")}</p> : null}
  </section>;
}
