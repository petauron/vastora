import type { AgentReinstallPlan } from "../types";
import type { Language } from "../translations";
import { copy } from "./shared";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";

export function ReinstallMonitorReview({ plan, busy, language, onInspect, onRotate, onRestore, onReporting }: { plan: AgentReinstallPlan; busy: boolean; language: Language; onInspect: (applicationId: string) => Promise<void>; onRotate: (applicationId: string) => Promise<void>; onRestore: (applicationId: string) => Promise<void>; onReporting: (applicationId: string) => Promise<void> }) {
  return <section aria-label={copy(language, "监控身份", "Monitoring identity")} className="flex flex-col gap-3 rounded-xl border p-4">
    <h3 className="text-sm font-medium">{copy(language, "监控身份", "Monitoring identity")}</h3>
    {plan.monitoring.map((monitor) => {
      const state = monitor.inspection?.state ?? monitor.state;
      const pending = state === "pending" || state === "running";
      const localService = monitor.serviceAgentId === plan.agentId;
      const canInspect = !monitor.rotation && monitor.state === "inspection_required" && !localService && !pending && state !== "verified";
      const name = plan.applications.find((app) => app.applicationId === monitor.applicationId)?.name ?? "Pulse";
      return <div key={monitor.applicationId} className="flex flex-col gap-2">
        <p className="text-sm" role="status" aria-atomic="true">{name} · {monitorState(state, language)}</p>
        {localService ? <p className="text-xs text-muted-foreground">{copy(language, "先恢复本机的 Pulse 服务和数据，再核验原监控身份。", "Restore this host's Pulse service and data before inspecting the original monitoring identity.")}</p> : null}
        {state === "verified" && !monitor.rotation ? <p className="text-xs text-muted-foreground">{copy(language, "凭据恢复和数据上报仍待完成。", "Credential restoration and metric reporting are still pending.")}</p> : null}
        {monitor.rotation ? <p className="text-xs text-muted-foreground" role="status">{monitor.restoration && monitor.rotation.state === "rotated" ? copy(language, "原节点凭据已轮换", "Original node credential rotated") : rotationState(monitor.rotation.state, language)}</p> : state === "verified" && !localService ? <>
          <p className="text-xs text-muted-foreground">{copy(language, "下一步轮换原节点凭据，旧凭据立即失效，监控历史保留。", "Next, rotate the original node credential. The old credential stops working; monitoring history is retained.")}</p>
          <Button className="self-start" variant="outline" disabled={busy || plan.recovery?.privateIsolation === "pending"} onClick={() => void onRotate(monitor.applicationId)}>{copy(language, "轮换原监控凭据", "Rotate original monitoring credential")}</Button>
        </> : null}
        {monitor.restoration ? <p className="text-xs text-muted-foreground" role="status">{restoreState(monitor.restoration.state, language)}</p> : monitor.rotation?.state === "rotated" ? <Button className="self-start" variant="outline" disabled={busy || !plan.networkReview?.approvalCurrent} onClick={() => void onRestore(monitor.applicationId)}>{copy(language, "恢复原监控采集端", "Restore original monitoring collector")}</Button> : null}
        {monitor.restoration?.state === "succeeded" ? <>
          {monitor.reporting ? <p className="text-xs text-muted-foreground" role="status">{reportingState(monitor.reporting.state, language)}</p> : null}
          {monitor.reporting?.state !== "verified" ? <Button className="self-start" variant="outline" disabled={busy || !plan.networkReview?.approvalCurrent || monitor.reporting?.state === "pending" || monitor.reporting?.state === "running"} onClick={() => void onReporting(monitor.applicationId)}>{copy(language, "验证原节点上报", "Verify original node reporting")}</Button> : null}
        </> : null}
        {canInspect || pending ? <Button className="self-start" variant="outline" disabled={busy || pending || plan.recovery?.privateIsolation === "pending"} onClick={() => void onInspect(monitor.applicationId)}>
          {pending ? <Spinner data-icon="inline-start" /> : null}
          {pending ? copy(language, "等待核验结果", "Awaiting inspection result") : copy(language, "核验原监控身份", "Inspect original monitoring identity")}
        </Button> : null}
      </div>;
    })}
  </section>;
}

function monitorState(state: string, language: Language) {
  switch (state) {
    case "inspection_required": return copy(language, "待核验", "Inspection needed");
    case "pending": return copy(language, "等待原监控服务", "Waiting for original monitoring service");
    case "running": return copy(language, "正在核验", "Inspecting identity");
    case "verified": return copy(language, "原监控身份已确认", "Original monitoring identity verified");
    case "needs_review": return copy(language, "原身份缺失、停用或不唯一，需人工核对", "Original identity missing, inactive or ambiguous; review required");
    case "failed": return copy(language, "核验失败，请检查原监控服务后重试", "Inspection failed; check the original monitoring service before retrying");
    case "stale": return copy(language, "原核验依据已变化，需重新核对", "Inspection evidence changed; review again");
    case "configuration_invalid": return copy(language, "原监控配置需要核对", "Review original monitoring configuration");
    case "service_missing": return copy(language, "未找到原监控服务", "Original monitoring service missing");
    default: return copy(language, "原登记记录不完整，需人工核对", "Original registration evidence incomplete; review required");
  }
}

function rotationState(state: string, language: Language) {
  switch (state) {
    case "pending": return copy(language, "等待轮换监控凭据", "Waiting to rotate monitoring credential");
    case "running": return copy(language, "正在轮换监控凭据", "Rotating monitoring credential");
    case "rotated": return copy(language, "原节点凭据已轮换 · 采集端恢复与上报待验证", "Original node credential rotated · collector restoration and reporting pending");
    default: return copy(language, "凭据轮换结果需核对，请勿重复操作", "Review the credential rotation outcome before any further action");
  }
}

function restoreState(state: string, language: Language) {
  switch (state) {
    case "pending": return copy(language, "等待恢复采集端", "Waiting to restore collector");
    case "running": return copy(language, "正在导入原身份并启动采集端", "Importing original identity and starting collector");
    case "succeeded": return copy(language, "采集端已启动", "Collector started");
    default: return copy(language, "采集端恢复结果需核对", "Collector restoration outcome needs review");
  }
}

function reportingState(state: string, language: Language) {
  switch (state) {
    case "pending": case "running": return copy(language, "正在验证原节点上报", "Verifying original node reports");
    case "verified": return copy(language, "原节点已恢复上报", "Original node reporting restored");
    case "not_reporting": return copy(language, "尚未收到轮换后的新数据，请检查采集端后重试", "No fresh report after rotation; check the collector and retry");
    case "stale": return copy(language, "上报验证已过期，请重新验证", "Reporting verification expired; verify again");
    default: return copy(language, "上报结果需核对", "Reporting result needs review");
  }
}
