import { useId, useMemo } from "react";
import { ArrowUpRightIcon, ServerIcon } from "lucide-react";
import type { AppData, Screen } from "@/types";
import type { Language } from "@/translations";
import { AppIcon } from "@/components/desktop/AppIcon";
import { useDesktopOrder } from "@/components/desktop/useDesktopOrder";
import { useDesktopSelection } from "@/components/desktop/useDesktopSelection";
import { copy } from "./shared";
import { desktopApplications } from "./applicationLaunch";

type Shortcut = { key: string; name: string; icon: string; screen?: Screen; url?: string };

export function DesktopView({ data, language, onNavigate, onOpenApp }: { data: AppData; language: Language; onNavigate: (screen: Screen) => void; onOpenApp: (key: string) => void }) {
  const selection = useDesktopSelection();
  const selectedDescription = useId();
  const selectionInstructions = useId();
  const apps = useMemo(() => desktopApplications(data, language), [data, language]);
  const shortcuts: Shortcut[] = [
    { key: "system:apps", name: copy(language, "应用商店", "App Store"), icon: "apps", screen: "apps" },
    { key: "system:nodes", name: copy(language, "主机管理", "Hosts"), icon: "nodes", screen: "nodes" },
    ...apps.map((app) => ({ key: `app:${app.key}`, name: app.name, icon: app.key, url: app.url })),
  ];
  const order = useDesktopOrder(shortcuts.map((item) => item.key), selection.beginItemDrag);
  const itemProps = (key: string) => ({ "data-desktop-item": key, "data-selected": selection.selected.has(key), "aria-describedby": selection.selected.has(key) ? `${selectionInstructions} ${selectedDescription}` : selectionInstructions, ...order.itemProps(key) });
  const agents = data.agents.filter((agent) => agent.status === "active");
  const online = agents.filter((agent) => agent.connected && !agent.credentialRevoked).length;
  const failed = data.applications.filter((app) => ["failed", "degraded"].includes(app.status)).length;
  return <section className="desktop-home" ref={selection.surfaceRef} tabIndex={-1} aria-label={copy(language, "桌面", "Desktop")} {...selection.surfaceProps}>
    <h1 className="sr-only">{copy(language, "Vastora 桌面", "Vastora desktop")}</h1>
    <p className="sr-only" id={selectionInstructions}>{copy(language, "拖动图标到桌面空白处自由摆放并自动对齐，已有图标自动让位，多选后可一起移动；Alt 加方向键移动到相邻格子。拖动空白处框选；Shift、⌘ 或 Ctrl 加单击可多选，空格切换选中，Esc 取消。单击选中，双击或 Enter 打开应用；触屏点按打开。", "Drag icons anywhere on the desktop to snap to the grid, with a live placement preview and automatic displacement. Alt plus an arrow key moves an icon to the adjacent grid cell. Drag the background to select. Shift, Command or Control-click to toggle selection, Space to select, Escape to clear. Click to select, double-click or Enter to open; tap to open on touchscreens.")}</p>
    <span className="sr-only" id={selectedDescription}>{copy(language, "已选中", "Selected")}</span>
    <span className="sr-only" role="status">{selection.selected.size ? copy(language, `已选择 ${selection.selected.size} 个应用`, `${selection.selected.size} applications selected`) : ""}</span>
    <span className="sr-only" role="status" key={order.announcement?.revision}>{order.announcement ? order.announcement.saved ? copy(language, "桌面布局已保存", "Desktop layout saved") : copy(language, "布局已调整，但当前浏览器无法保存", "Layout updated, but this browser could not save it") : ""}</span>
    <aside className="desktop-widget" data-desktop-no-select aria-label={copy(language, "系统状态", "System status")}>
      <div className="desktop-widget-heading"><span className="flex items-center gap-2"><ServerIcon className="size-4" />Vastora</span><button aria-label={copy(language, "查看系统概览", "View system overview")} onClick={() => onNavigate("overview")}><ArrowUpRightIcon className="size-4" /></button></div>
      <dl className="desktop-widget-stats"><div><dt>{copy(language, "主机在线", "Hosts online")}</dt><dd>{online}<small> / {agents.length}</small></dd></div><div><dt>{copy(language, "运行中的安装", "Running installations")}</dt><dd>{data.applications.filter((app) => app.status === "running").length}</dd></div></dl>
      {online < agents.length ? <button className="desktop-widget-notice" onClick={() => onNavigate("nodes")}>{copy(language, `${agents.length - online} 台主机未连接`, `${agents.length - online} hosts disconnected`)}<ArrowUpRightIcon /></button> : null}
      {failed ? <button className="desktop-widget-notice" onClick={() => onNavigate("apps")}>{copy(language, `${failed} 项应用安装需要检查`, `${failed} installations need attention`)}<ArrowUpRightIcon /></button> : null}
    </aside>
    <nav ref={order.gridRef} style={order.gridStyle} data-ready={order.ready} className="desktop-shortcuts" data-reordering={order.dragging.length > 0} aria-label={copy(language, "桌面应用", "Desktop applications")}>
      {order.orderedKeys.map((key) => {
        const shortcut = shortcuts.find((item) => item.key === key)!;
        const content = <><span className="desktop-shortcut-art"><AppIcon appKey={shortcut.icon} />{shortcut.url ? <span className="desktop-shortcut-link" aria-hidden="true"><ArrowUpRightIcon /></span> : null}</span><span className="desktop-shortcut-label">{shortcut.name}</span></>;
        return shortcut.url ? <a className="desktop-shortcut" {...itemProps(key)} key={key} href={shortcut.url} target="_blank" rel="noreferrer" aria-label={copy(language, `在新标签页打开 ${shortcut.name}`, `Open ${shortcut.name} in a new tab`)}>{content}</a> : <button className="desktop-shortcut" {...itemProps(key)} key={key} onClick={() => shortcut.screen ? onNavigate(shortcut.screen) : onOpenApp(shortcut.icon)}>{content}</button>;
      })}
      {!apps.length ? <p className="desktop-empty-hint">{copy(language, "从应用商店添加你的第一个应用", "Add your first application from the App Store")}</p> : null}
    </nav>
    {order.position ? <div className="desktop-drag-preview" aria-hidden="true" style={{ left: order.position.x, top: order.position.y }}><AppIcon appKey={shortcuts.find((item) => item.key === order.position?.key)?.icon ?? "apps"} />{order.dragging.length > 1 ? <span className="desktop-drag-count">{order.dragging.length}</span> : null}</div> : null}
    {selection.rectangle ? <div className="desktop-selection-box" aria-hidden="true" style={{ left: selection.rectangle.x, top: selection.rectangle.y, width: selection.rectangle.width, height: selection.rectangle.height }} /> : null}
  </section>;
}
