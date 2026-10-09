import { useMemo } from "react";
import { ArrowUpRightIcon, ServerIcon } from "lucide-react";
import type { AppData, Screen } from "@/types";
import type { Language } from "@/translations";
import { AppIcon } from "@/components/desktop/AppIcon";
import { copy } from "./shared";
import { desktopApplications } from "./applicationLaunch";

export function DesktopView({ data, language, onNavigate, onOpenApp }: { data: AppData; language: Language; onNavigate: (screen: Screen) => void; onOpenApp: (key: string) => void }) {
  const apps = useMemo(() => desktopApplications(data, language), [data, language]);
  const agents = data.agents.filter((agent) => agent.status === "active");
  const online = agents.filter((agent) => agent.connected && !agent.credentialRevoked).length;
  const failed = data.applications.filter((app) => ["failed", "degraded"].includes(app.status)).length;
  return <section className="desktop-home">
    <h1 className="sr-only">{copy(language, "Vastora 桌面", "Vastora desktop")}</h1>
    <aside className="desktop-widget" aria-label={copy(language, "系统状态", "System status")}>
      <div className="desktop-widget-heading"><span className="flex items-center gap-2"><ServerIcon className="size-4" />Vastora</span><button aria-label={copy(language, "查看系统概览", "View system overview")} onClick={() => onNavigate("overview")}><ArrowUpRightIcon className="size-4" /></button></div>
      <dl className="desktop-widget-stats"><div><dt>{copy(language, "主机在线", "Hosts online")}</dt><dd>{online}<small> / {agents.length}</small></dd></div><div><dt>{copy(language, "运行中的安装", "Running installations")}</dt><dd>{data.applications.filter((app) => app.status === "running").length}</dd></div></dl>
      {online < agents.length ? <button className="desktop-widget-notice" onClick={() => onNavigate("nodes")}>{copy(language, `${agents.length - online} 台主机未连接`, `${agents.length - online} hosts disconnected`)}<ArrowUpRightIcon /></button> : null}
      {failed ? <button className="desktop-widget-notice" onClick={() => onNavigate("apps")}>{copy(language, `${failed} 项应用安装需要检查`, `${failed} installations need attention`)}<ArrowUpRightIcon /></button> : null}
    </aside>
    <nav className="desktop-shortcuts" aria-label={copy(language, "桌面应用", "Desktop applications")}>
      <button className="desktop-shortcut" onClick={() => onNavigate("apps")}><AppIcon appKey="apps" /><span>{copy(language, "应用商店", "App Store")}</span></button>
      <button className="desktop-shortcut" onClick={() => onNavigate("nodes")}><AppIcon appKey="nodes" /><span>{copy(language, "主机管理", "Hosts")}</span></button>
      {apps.map((app) => {
        const content = <><AppIcon appKey={app.key} /><span>{app.name}{app.url ? <ArrowUpRightIcon aria-hidden="true" className="ml-0.5 inline size-3" /> : null}</span></>;
        return app.url ? <a className="desktop-shortcut" key={app.key} href={app.url} target="_blank" rel="noreferrer" aria-label={copy(language, `在新标签页打开 ${app.name}`, `Open ${app.name} in a new tab`)}>{content}</a> : <button className="desktop-shortcut" key={app.key} onClick={() => onOpenApp(app.key)}>{content}</button>;
      })}
      {!apps.length ? <p className="desktop-empty-hint">{copy(language, "从应用商店添加你的第一个应用", "Add your first application from the App Store")}</p> : null}
    </nav>
  </section>;
}
