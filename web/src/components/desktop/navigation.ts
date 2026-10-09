import type { Screen } from "@/types";

export const systemApplications: { id: Screen; zh: string; en: string }[] = [
  { id: "home", zh: "桌面", en: "Desktop" },
  { id: "apps", zh: "应用商店", en: "App Store" },
  { id: "nodes", zh: "主机管理", en: "Hosts" },
  { id: "settings", zh: "控制面板", en: "Control Panel" },
  { id: "network", zh: "网络", en: "Network" },
  { id: "overview", zh: "系统概览", en: "Overview" },
  { id: "activity", zh: "任务与活动", en: "Tasks & Activity" },
  { id: "assistant", zh: "助手", en: "Assistant" },
];

export function workspaceFromURL() {
  return window.location.pathname === "/apps" ? new URLSearchParams(window.location.search).get("open") : null;
}

export function workspacePath(appKey: string) {
  return `/apps?${new URLSearchParams({ open: appKey })}`;
}
