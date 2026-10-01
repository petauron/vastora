import type { AgentReinstallPlan } from "../types";
import type { Language } from "../translations";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { copy } from "./shared";

export function ReinstallApplicationReview({ plan, busy, language, onPrepare }: { plan: AgentReinstallPlan; busy: boolean; language: Language; onPrepare: (applicationId: string) => Promise<void> }) {
  const apps = plan.applications.filter((app) => app.appKey === "vastora-official/meridian" && app.recovery === "rebuild_configuration");
  if (!apps.length) return null;
  const ready = plan.networkReview?.approvalCurrent && !plan.executions.length && !plan.unclaimedLocalWork.length;
  return <section className="flex flex-col gap-3 rounded-xl border p-4" aria-label={copy(language, "准备应用", "Prepare applications")}>
    <p className="text-sm">{copy(language, "准备原版本应用", "Prepare saved application version")}</p>
    <p className="text-xs text-muted-foreground">{copy(language, "先准备原版本 Meridian 镜像，随后恢复运行配置并验证入口。此步骤不会恢复业务。", "Prepare the saved Meridian image before restoring runtime configuration and verifying access. This step does not restore business service.")}</p>
    {apps.map((app) => {
      const state = app.preparation?.state;
      const pending = state === "pending" || state === "running";
      return <div key={app.applicationId} className="flex items-center justify-between gap-3 text-sm">
        <span>{app.name} <span className="text-xs text-muted-foreground">{app.version}</span></span>
        {state ? <span className="flex items-center gap-2 text-xs text-muted-foreground" role="status">{pending ? <Spinner /> : null}{state === "succeeded" ? copy(language, "镜像已准备 · 运行配置待恢复", "Image prepared · runtime restoration pending") : pending ? copy(language, "正在准备", "Preparing") : state === "needs_review" ? copy(language, "结果未确认 · 需核对", "Outcome uncertain · inspect before continuing") : copy(language, "准备失败 · 需核对结果", "Preparation failed · inspect the outcome")}</span> : <Button variant="outline" disabled={busy || !ready} onClick={() => void onPrepare(app.applicationId)}>{copy(language, "准备原版本", "Prepare saved version")}</Button>}
      </div>;
    })}
    {!ready && apps.some((app) => !app.preparation) ? <p className="text-xs text-muted-foreground">{copy(language, "先核对旧任务并确认新机器地址。", "Review previous work and approve the replacement address first.")}</p> : null}
  </section>;
}
