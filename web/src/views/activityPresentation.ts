import type { Action } from "../types";
import type { Language } from "../translations";
import { copy, taskError } from "./shared";

export function groupActions(actions: Action[]) {
  const grouped = new Map<string, Action[]>();
  for (const action of actions) {
    const group = grouped.get(action.taskId);
    if (group) group.push(action);
    else grouped.set(action.taskId, [action]);
  }
  return [...grouped.entries()].map(([taskId, values]) => ({ taskId, actions: [...values].sort((left, right) => Date.parse(right.createdAt) - Date.parse(left.createdAt)) }));
}

export function visibleActionMessage(language: Language, action: Action) {
  const localized = actionMessage(language, action.message);
  if (!action.message || localized !== action.message || /^(install|upgrade|configure|uninstall) /.test(action.message)) return localized;
  return action.event === "failed" ? taskError(language, action.message) : actionKind(language, action.kind);
}

export function actionKind(language: Language, kind: string) {
  const labels: Record<string, [string, string]> = {
    "application.command": ["应用操作", "App operation"],
    "landing.server.apply": ["配置落地服务", "Configure exit server"],
    "landing.proxy.apply": ["连接线路与落地", "Connect entry and exit"],
    "agent.update": ["更新节点管理程序", "Update node software"],
    "agent.decommission": ["移除节点", "Remove node"],
    "node.listener.apply": ["配置节点入口", "Configure node access"],
    "node.ip-quality": ["检测 IP 质量", "Check IP quality"],
    "application.apply": ["应用变更", "Application change"],
    "gateway.component.apply": ["准备服务入口", "Prepare service access"],
    "gateway.routes.apply": ["更新访问方式", "Update service access"],
    "tunnel.state.apply": ["更新 Cloudflare 连接", "Update Cloudflare connection"]
  };
  return labels[kind] ? copy(language, ...labels[kind]) : copy(language, "系统操作", "System operation");
}

export function actionMessage(language: Language, message?: string) {
  if (!message) return "";
  const operation = /^(install|upgrade|configure|uninstall) (.+)$/.exec(message);
  if (operation) {
    const labels: Record<string, [string, string]> = { install: ["安装", "Install"], upgrade: ["升级", "Upgrade"], configure: ["修改配置", "Configure"], uninstall: ["卸载", "Uninstall"] };
    return copy(language, ...labels[operation[1]]);
  }
  if (message === "task lease expired; queued for retry") return copy(language, "当时节点未及时响应。请在任务处理中查看当前结果。", "The node did not respond in time. Check Tasks for the current outcome.");
  if (message === "gateway health check failed; queued for reconcile") return copy(language, "当时服务入口检查失败。请查看应用的当前访问状态。", "The access check failed at the time. Check the app's current access status.");
  return message;
}
