import { NetworkIcon } from "lucide-react";
import type { AgentView } from "../types";
import type { Language } from "../translations";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { copy } from "./shared";

export function RuntimeRecoveryAlert({ agent, language, onApplications }: { agent: AgentView; language: Language; onApplications: () => void }) {
  if (!agent.connected || !agent.runtimeRecovery) return null;
  const applications = agent.runtimeRecoveryApplications ?? [];
  const applicationBlocked = agent.runtimeRecovery === "application" && applications.length > 0;
  return <Alert variant={applicationBlocked ? "destructive" : "default"}>
    <NetworkIcon aria-hidden="true" />
    <AlertTitle>{applicationBlocked ? copy(language, "应用恢复受阻", "Application recovery blocked") : copy(language, "服务正在恢复", "Restoring services")}</AlertTitle>
    <AlertDescription>
      <p>{recoveryDescription(language, agent.runtimeRecovery)}</p>
      {applicationBlocked ? <>
        <ul className="list-disc pl-4">{applications.map((application) => <li className="break-words" key={application.appKey}><span className="font-mono">{application.appKey}</span>{" · "}{recoveryReason(language, application.reason)}</li>)}</ul>
        <p>{copy(language, "代理配置与线路恢复请在 Glivela 中处理。所有权或本地状态无法确认时仍需人工处理。", "Manage proxy configuration and route recovery in Glivela. Unproven ownership or local state still requires operator intervention.")}</p>
        <Button onClick={onApplications} size="sm" type="button" variant="outline">{copy(language, "查看应用", "View apps")}</Button>
      </> : null}
    </AlertDescription>
  </Alert>;
}

function recoveryDescription(language: Language, stage: NonNullable<AgentView["runtimeRecovery"]>) {
  switch (stage) {
    case "reconciliation": return copy(language, "正在恢复上次未完成的操作，完成后会继续启动服务。", "Recovering the interrupted operation before starting services.");
    case "landing": return copy(language, "落地配置尚未恢复。请检查落地状态或提交停用操作；无法确认安全状态时需要人工处理。", "The landing configuration has not recovered. Inspect it or submit a disable operation; an unproven safe state requires operator intervention.");
    case "application": return copy(language, "节点管理连接正常，但应用恢复尚未完成。无法确认运行配置与 Agent 状态时，必须由操作员显式恢复。", "The management connection is available, but application recovery is incomplete. An operator must explicitly recover an unproven runtime and Agent state.");
    case "gateway": return copy(language, "访问入口尚未恢复，系统会自动重试。持续失败时需人工核对网关状态。", "Service access has not recovered yet. Persistent failures require operator reconciliation of gateway state.");
    case "listener": return copy(language, "代理入口尚未恢复，系统会自动重试。恢复成功前不接受新路由。", "Proxy access has not recovered yet. New routes wait until recovery succeeds.");
    default: return copy(language, "节点已连接，正在恢复服务。", "The node is connected and its services are being restored.");
  }
}

function recoveryReason(language: Language, reason: NonNullable<AgentView["runtimeRecoveryApplications"]>[number]["reason"]) {
  switch (reason) {
    case "state_incomplete": return copy(language, "本地安装状态不完整", "Local installation state is incomplete");
    case "image_unavailable": return copy(language, "离线恢复所需镜像不可用", "The image required for offline recovery is unavailable");
    case "health_check_failed": return copy(language, "应用健康检查未通过", "Application health check failed");
    default: return copy(language, "应用恢复失败", "Application restore failed");
  }
}
