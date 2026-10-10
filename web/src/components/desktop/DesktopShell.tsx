import { useEffect, useState, type ReactNode } from "react";
import { ArrowUpRightIcon, BellIcon, Grid2X2Icon, LanguagesIcon, Maximize2Icon, Minimize2Icon, MinusIcon, SearchIcon, Settings2Icon, XIcon } from "lucide-react";
import type { Language } from "@/translations";
import type { Screen } from "@/types";
import type { DesktopApplication } from "@/views/applicationLaunch";
import { copy } from "@/views/shared";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuGroup, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { ThemeToggle } from "@/components/theme";
import { Spinner } from "@/components/ui/spinner";
import { AppIcon } from "./AppIcon";
import { AppLauncher } from "./AppLauncher";
import { systemApplications } from "./navigation";
import { resizeEdges, useDesktopWindow } from "./useDesktopWindow";

export function DesktopShell({ children, screen, language, loading, workspace, apps, openApps, onNavigate, onOpenApp, onCloseWorkspace, onLanguage }: {
  children: ReactNode;
  screen: Screen;
  language: Language;
  loading: boolean;
  workspace: { key: string; name: string } | null;
  apps: DesktopApplication[];
  openApps: DesktopApplication[];
  onNavigate: (screen: Screen) => void;
  onOpenApp: (key: string) => void;
  onCloseWorkspace: () => void;
  onLanguage: (language: Language) => void;
}) {
  const [launcherOpen, setLauncherOpen] = useState(false);
  const [launcherMode, setLauncherMode] = useState<"all" | "search">("search");
  const showLauncher = (mode: "all" | "search") => { setLauncherMode(mode); setLauncherOpen(true); };
  const [minimizedScreen, setMinimizedScreen] = useState<Screen | null>(null);
  const current = systemApplications.find((item) => item.id === screen)!;
  const title = workspace?.name ?? copy(language, current.zh, current.en);
  const currentKey = workspace?.key ?? screen;
  const desktopWindow = useDesktopWindow(currentKey);
  const pinKeys: Screen[] = ["home", "apps", "nodes", "settings"];
  const pins = systemApplications.filter((app) => pinKeys.includes(app.id));
  const auxiliaryScreen = !workspace && !pinKeys.includes(screen) ? screen : minimizedScreen;
  const auxiliaryApp = auxiliaryScreen && !pinKeys.includes(auxiliaryScreen) ? systemApplications.find((app) => app.id === auxiliaryScreen) : undefined;
  const minimize = () => { if (!workspace) setMinimizedScreen(screen); onNavigate("home"); };
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") { event.preventDefault(); setLauncherMode("search"); setLauncherOpen((open) => !open); }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);
  return <div className="desktop-shell" data-desktop={screen === "home"} data-expanded={desktopWindow.maximized && screen !== "home"}>
    <header className="desktop-system-bar">
      <DropdownMenu>
        <DropdownMenuTrigger className="desktop-menu-brand" aria-label={copy(language, "Vastora 菜单", "Vastora menu")}><span className="desktop-brand-mark" aria-hidden="true"><i /><i /><i /><i /></span><span>Vastora</span></DropdownMenuTrigger>
        <DropdownMenuContent className="w-56">
          <DropdownMenuGroup><DropdownMenuItem onClick={() => onNavigate("overview")}>{copy(language, "系统概览", "System overview")}</DropdownMenuItem><DropdownMenuItem onClick={() => onNavigate("settings")}>{copy(language, "控制面板…", "Control Panel…")}</DropdownMenuItem></DropdownMenuGroup>
          <DropdownMenuSeparator />
          <DropdownMenuGroup><DropdownMenuItem onClick={() => onNavigate("home")}>{copy(language, "显示桌面", "Show desktop")}</DropdownMenuItem><DropdownMenuItem onClick={() => showLauncher("search")}>{copy(language, "搜索应用…", "Search applications…")}<kbd className="ml-auto text-xs text-muted-foreground">⌘ K</kbd></DropdownMenuItem></DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
      <span className="desktop-active-app">{title}</span>
      <DropdownMenu>
        <DropdownMenuTrigger className="desktop-menu-trigger">{copy(language, "前往", "Go")}</DropdownMenuTrigger>
        <DropdownMenuContent className="w-56"><DropdownMenuGroup>{systemApplications.map((item) => <DropdownMenuItem key={item.id} onClick={() => onNavigate(item.id)}><AppIcon appKey={item.id} className="size-6" />{copy(language, item.zh, item.en)}</DropdownMenuItem>)}</DropdownMenuGroup></DropdownMenuContent>
      </DropdownMenu>
      <DropdownMenu>
        <DropdownMenuTrigger className="desktop-menu-trigger desktop-window-menu">{copy(language, "窗口", "Window")}</DropdownMenuTrigger>
        <DropdownMenuContent className="w-56"><DropdownMenuGroup>
          <DropdownMenuItem disabled={screen === "home"} onClick={minimize}>{copy(language, "最小化", "Minimize")}</DropdownMenuItem>
          <DropdownMenuItem disabled={screen === "home" || desktopWindow.compact} onClick={desktopWindow.toggleMaximize}>{desktopWindow.maximized ? copy(language, "还原窗口", "Restore window") : copy(language, "最大化窗口", "Maximize window")}</DropdownMenuItem>
        </DropdownMenuGroup>{openApps.length ? <><DropdownMenuSeparator /><DropdownMenuGroup>{openApps.map((app) => <DropdownMenuItem key={app.key} onClick={() => onOpenApp(app.key)}>{app.name}</DropdownMenuItem>)}</DropdownMenuGroup></> : null}</DropdownMenuContent>
      </DropdownMenu>
      <div className="flex-1" />
      <Button aria-label={copy(language, "搜索应用（⌘ K / Ctrl K）", "Search apps (⌘ K / Ctrl K)")} onClick={() => showLauncher("search")} variant="ghost" size="icon"><SearchIcon /></Button>
      <DropdownMenu>
        <DropdownMenuTrigger render={<Button variant="ghost" size="icon" />} aria-label={copy(language, "快捷设置", "Quick settings")}><Settings2Icon /></DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-60 p-3">
          <div className="mb-2 flex items-center justify-between border-b pb-2 text-sm"><span>{copy(language, "外观", "Appearance")}</span><ThemeToggle language={language} /></div>
          <DropdownMenuGroup>
          <DropdownMenuItem onClick={() => onLanguage(language === "zh-CN" ? "en" : "zh-CN")}><LanguagesIcon />{copy(language, "English", "简体中文")}</DropdownMenuItem>
          <DropdownMenuItem onClick={() => onNavigate("activity")}><BellIcon />{copy(language, "任务与活动", "Tasks & Activity")}</DropdownMenuItem>
          <DropdownMenuItem onClick={() => onNavigate("settings")}><Settings2Icon />{copy(language, "控制面板…", "Control Panel…")}</DropdownMenuItem>
        </DropdownMenuGroup></DropdownMenuContent>
      </DropdownMenu>
      <MenuClock language={language} />
    </header>
    <div className={screen === "home" ? "desktop-surface" : "desktop-window"} style={screen === "home" ? undefined : desktopWindow.style} data-interacting={desktopWindow.interacting || undefined}>
      {screen !== "home" ? <div className="desktop-window-bar" {...desktopWindow.titleBarProps} tabIndex={desktopWindow.compact ? undefined : 0} role="group" aria-label={copy(language, `${title}窗口标题栏`, `${title} window title bar`)} aria-describedby="desktop-window-help">
        <div className="desktop-traffic-lights">
          <button className="desktop-traffic-light" data-kind="close" aria-label={copy(language, "关闭窗口", "Close window")} title={copy(language, "关闭窗口", "Close window")} onClick={() => { setMinimizedScreen(null); onCloseWorkspace(); }}><XIcon /></button>
          <button className="desktop-traffic-light" data-kind="minimize" aria-label={copy(language, "最小化窗口", "Minimize window")} title={copy(language, "最小化窗口", "Minimize window")} onClick={minimize}><MinusIcon /></button>
          <button className="desktop-traffic-light" data-kind="zoom" disabled={desktopWindow.compact} aria-label={desktopWindow.maximized ? copy(language, "还原窗口", "Restore window") : copy(language, "最大化窗口", "Maximize window")} title={desktopWindow.maximized ? copy(language, "还原窗口", "Restore window") : copy(language, "最大化窗口", "Maximize window")} aria-pressed={desktopWindow.maximized} onClick={desktopWindow.toggleMaximize}>{desktopWindow.maximized ? <Minimize2Icon /> : <Maximize2Icon />}</button>
        </div>
        <div className="desktop-window-title"><span>{title}</span></div>
        <div className="desktop-window-tools">{loading ? <Spinner aria-label={copy(language, "正在更新", "Updating")} /> : null}<Button aria-label={copy(language, "所有应用", "All applications")} onClick={() => showLauncher("all")} size="icon" variant="ghost"><Grid2X2Icon /></Button></div>
      </div> : null}
      {children}
      {screen !== "home" ? <>
        <p id="desktop-window-help" className="sr-only">{copy(language, "拖动标题栏移动，双击最大化或还原；拖动边缘调整大小。聚焦标题栏后，Alt 加方向键移动，Alt 加 Shift 加方向键调整大小，Esc 取消拖动。", "Drag the title bar to move; double-click to maximize or restore. Drag an edge to resize. With the title bar focused, Alt + arrows moves, Alt + Shift + arrows resizes, and Escape cancels dragging.")}</p>
        {!desktopWindow.compact && !desktopWindow.maximized ? resizeEdges.map((edge) => <div key={edge} className="desktop-window-resize" data-edge={edge} aria-hidden="true" {...desktopWindow.resizeProps(edge)} />) : null}
      </> : null}
    </div>
    <nav className="desktop-dock" aria-label={copy(language, "Dock 应用切换", "Dock applications")}>
      {pins.map((item) => <button aria-current={!workspace && screen === item.id ? "page" : undefined} aria-label={copy(language, item.zh, item.en)} className="desktop-dock-item" key={item.id} onClick={() => onNavigate(item.id)}><AppIcon appKey={item.id} /><span className="desktop-dock-label">{copy(language, item.zh, item.en)}</span></button>)}
      <button className="desktop-dock-item" aria-label={copy(language, "所有应用", "All applications")} onClick={() => showLauncher("all")}><span className="desktop-launchpad-icon" aria-hidden="true">{Array.from({ length: 9 }, (_, i) => <i key={i} />)}</span><span className="desktop-dock-label">{copy(language, "所有应用", "All applications")}</span></button>
      {openApps.length || auxiliaryApp ? <span className="desktop-dock-divider" aria-hidden="true" /> : null}
      {openApps.map((app) => {
        const content = <><AppIcon appKey={app.key} /><span className="desktop-dock-label">{app.name}{app.url ? <ArrowUpRightIcon aria-hidden="true" className="ml-1 inline size-3" /> : null}</span></>;
        return app.url ? <a className="desktop-dock-item" key={app.key} href={app.url} target="_blank" rel="noreferrer" aria-label={copy(language, `在新标签页打开 ${app.name}`, `Open ${app.name} in a new tab`)}>{content}</a> : <button aria-current={workspace?.key === app.key ? "page" : undefined} aria-label={app.name} className="desktop-dock-item" key={app.key} onClick={() => onOpenApp(app.key)}>{content}</button>;
      })}
      {auxiliaryApp ? <button className="desktop-dock-item" aria-current={!workspace && screen === auxiliaryApp.id ? "page" : undefined} aria-label={copy(language, auxiliaryApp.zh, auxiliaryApp.en)} onClick={() => onNavigate(auxiliaryApp.id)}><AppIcon appKey={auxiliaryApp.id} /><span className="desktop-dock-label">{copy(language, auxiliaryApp.zh, auxiliaryApp.en)}</span></button> : null}
    </nav>
    <AppLauncher key={launcherMode} mode={launcherMode} apps={apps} language={language} open={launcherOpen} onOpenChange={setLauncherOpen} onNavigate={onNavigate} onOpenApp={onOpenApp} />
  </div>;
}

function MenuClock({ language }: { language: Language }) {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => { const timer = window.setInterval(() => setNow(new Date()), 30_000); return () => window.clearInterval(timer); }, []);
  return <time className="desktop-clock" dateTime={now.toISOString()}>{new Intl.DateTimeFormat(language, { month: "short", day: "numeric", weekday: "short", hour: "2-digit", minute: "2-digit", hour12: false }).format(now)}</time>;
}
