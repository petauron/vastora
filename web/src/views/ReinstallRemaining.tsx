import type { AgentReinstallPlan } from "../types";
import type { Language } from "../translations";
import { copy } from "./shared";

const labels: Record<string, [string, string]> = {
  replacement_identity: ["核对新机器身份", "Verify replacement identity"],
  previous_identity_isolation: ["撤销旧机器私网身份", "Withdraw previous private identity"],
  previous_work: ["处置历史任务及远端副作用", "Resolve previous tasks and remote effects"],
  network_review: ["确认新网络地址及依赖", "Approve replacement network and dependencies"],
  data_restore: ["需要从有效备份恢复数据，当前流程不支持自动恢复", "Restore a valid data backup; automatic recovery is not supported"],
  application_review: ["此应用需要单独核对恢复方式", "Review recovery for this application"],
  application_prepare: ["准备原目标版本的应用", "Prepare the saved application version"],
  landing_authorization: ["恢复落地源身份授权", "Restore landing source authorization"],
  application_runtime: ["恢复并核对代理运行时", "Restore and verify the proxy runtime"],
  entry_restore: ["恢复访问入口", "Restore the access listener"],
  access_activate: ["应用已核对的访问地址", "Activate reviewed access addresses"],
  entry_dns: ["核对入口 DNS", "Verify entry DNS"],
  entry_verify: ["检查入口解析和 TLS", "Check entry resolution and TLS"],
  monitor_restore: ["恢复原监控关联", "Restore the original monitoring association"],
  monitor_reporting: ["获取恢复后的新鲜监控上报", "Verify fresh reporting after restoration"],
  client_acceptance: ["待验收原生线路及已配置落地线路的真实客户端请求", "Verify authenticated client requests through native and configured landing routes"],
  completion_review: ["由服务端核对全部证据后完成恢复", "Complete recovery after the server verifies all required evidence"],
};

export function ReinstallRemaining({ plan, language }: { plan: AgentReinstallPlan; language: Language }) {
  return <section className="flex flex-col gap-2 rounded-xl border p-4" aria-label={copy(language, "恢复尚缺", "Recovery remaining")}>
    <h3 className="text-sm font-medium">{copy(language, "恢复尚缺", "Recovery remaining")}</h3>
    <ul className="list-disc space-y-2 pl-4 text-xs text-muted-foreground">
      {plan.remaining?.map((item) => {
        const label = labels[item.code];
        const app = plan.applications.find((value) => value.applicationId === item.applicationId);
        return <li key={`${item.code}:${item.applicationId ?? ""}`}>
          {item.applicationId ? `${app?.name || copy(language, "应用", "Application")}：` : ""}
          {label ? copy(language, ...label) : copy(language, "需要刷新并核对恢复状态", "Refresh and review recovery status")}
        </li>;
      })}
    </ul>
    <p className="text-xs text-muted-foreground">{copy(language, "业务验证尚未完成，任务限制仍保留；入口连通不等于业务验收通过。", "Business verification is not complete; task restrictions remain. Entry reachability does not establish business acceptance.")}</p>
  </section>;
}
