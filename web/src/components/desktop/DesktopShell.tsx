import { useEffect, useState, type ReactNode } from "react";
import { BellIcon, Grid2X2Icon, LanguagesIcon, Maximize2Icon, MinusIcon, SearchIcon, Settings2Icon, WifiIcon, WifiOffIcon, XIcon } from "lucide-react";
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

export function DesktopShell({ children, screen, language, connected, loading, workspace, apps, recentApps, onNavigate, onOpenApp, onCloseWorkspace, onLanguage }: {
  children: ReactNode;
  screen: Screen;
  language: Language;
  connected: boolean;
  loading: boolean;
  workspace: { key: string; name: string } | null;
  apps: DesktopApplication[];
  recentApps: DesktopApplication[];
  onNavigate: (screen: Screen) => void;
  onOpenApp: (key: string) => void;
  onCloseWorkspace: () => void;
  onLanguage: (language: Language) => void;
}) {
  const [launcherOpen, setLauncherOpen] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [minimizedScreen, setMinimizedScreen] = useState<Screen | null>(null);
  const current = systemApplications.find((item) => item.id === screen)!;
  const title = workspace?.name ?? copy(language, current.zh, current.en);
  const currentKey = workspace?.key ?? screen;
  const pinKeys: Screen[] = ["home", "apps", "nodes", "settings"];
  const pins = systemApplications.filter((app) => pinKeys.includes(app.id));
  const auxiliaryScreen = !workspace && !pinKeys.includes(screen) ? screen : minimizedScreen;
  const auxiliaryApp = auxiliaryScreen && !pinKeys.includes(auxiliaryScreen) ? systemApplications.find((app) => app.id === auxiliaryScreen) : undefined;
  const minimize = () => { if (!workspace) setMinimizedScreen(screen); onNavigate("home"); };
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") { event.preventDefault(); setLauncherOpen((open) => !open); }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);
  return <div className="desktop-shell" data-expanded={expanded && screen !== "home"}>
    <header className="desktop-system-bar">
      <DropdownMenu>
        <DropdownMenuTrigger className="desktop-menu-brand" aria-label={copy(language, "Vastora 菜单", "Vastora menu")}><span className="desktop-brand-mark" aria-hidden="true"><i /><i /><i /><i /></span><span>Vastora</span></DropdownMenuTrigger>
        <DropdownMenuContent className="w-56">
          <DropdownMenuGroup><DropdownMenuItem onClick={() => onNavigate("overview")}>{copy(language, "系统概览", "System overview")}</DropdownMenuItem><DropdownMenuItem onClick={() => onNavigate("settings")}>{copy(language, "控制面板…", "Control Panel…")}</DropdownMenuItem></DropdownMenuGroup>
          <DropdownMenuSeparator />
          <DropdownMenuGroup><DropdownMenuItem onClick={() => onNavigate("home")}>{copy(language, "显示桌面", "Show desktop")}</DropdownMenuItem><DropdownMenuItem onClick={() => setLauncherOpen(true)}>{copy(language, "搜索应用…", "Search applications…")}<kbd className="ml-auto text-xs text-muted-foreground">⌘ K</kbd></DropdownMenuItem></DropdownMenuGroup>
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
          <DropdownMenuItem disabled={screen === "home"} onClick={() => setExpanded((value) => !value)}>{expanded ? copy(language, "还原窗口", "Restore window") : copy(language, "放大窗口", "Zoom window")}</DropdownMenuItem>
        </DropdownMenuGroup>{recentApps.length ? <><DropdownMenuSeparator /><DropdownMenuGroup>{recentApps.map((app) => <DropdownMenuItem key={app.key} onClick={() => onOpenApp(app.key)}>{app.name}</DropdownMenuItem>)}</DropdownMenuGroup></> : null}</DropdownMenuContent>
      </DropdownMenu>
      <div className="flex-1" />
      <span className="desktop-connection" role="status" title={connected ? copy(language, "Center 已连接", "Center connected") : copy(language, "正在重新连接 Center", "Reconnecting to Center")}>{connected ? <WifiIcon /> : <WifiOffIcon />}<span className="sr-only">{connected ? copy(language, "已连接", "Connected") : copy(language, "重新连接中", "Reconnecting")}</span></span>
      <Button aria-label={copy(language, "搜索应用（⌘ K / Ctrl K）", "Search apps (⌘ K / Ctrl K)")} onClick={() => setLauncherOpen(true)} variant="ghost" size="icon"><SearchIcon /></Button>
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
    <div className={screen === "home" ? "desktop-surface" : "desktop-window"}>
      {screen !== "home" ? <div className="desktop-window-bar">
        <div className="desktop-traffic-lights">
          <button className="desktop-traffic-light" data-kind="close" aria-label={copy(language, "关闭窗口", "Close window")} title={copy(language, "关闭窗口", "Close window")} onClick={() => { setMinimizedScreen(null); onCloseWorkspace(); }}><XIcon /></button>
          <button className="desktop-traffic-light" data-kind="minimize" aria-label={copy(language, "最小化窗口", "Minimize window")} title={copy(language, "最小化窗口", "Minimize window")} onClick={minimize}><MinusIcon /></button>
          <button className="desktop-traffic-light" data-kind="zoom" aria-label={expanded ? copy(language, "还原窗口", "Restore window") : copy(language, "放大窗口", "Zoom window")} title={expanded ? copy(language, "还原窗口", "Restore window") : copy(language, "放大窗口", "Zoom window")} aria-pressed={expanded} onClick={() => setExpanded((value) => !value)}><Maximize2Icon /></button>
        </div>
        <div className="desktop-window-title"><AppIcon appKey={currentKey} className="size-5" /><span>{title}</span></div>
        <div className="desktop-window-tools">{loading ? <Spinner aria-label={copy(language, "正在更新", "Updating")} /> : null}<Button aria-label={copy(language, "搜索并切换应用", "Find and switch apps")} onClick={() => setLauncherOpen(true)} size="icon" variant="ghost"><Grid2X2Icon /></Button></div>
      </div> : null}
      {children}
    </div>
    <nav className="desktop-dock" aria-label={copy(language, "Dock 应用切换", "Dock applications")}>
      {pins.map((item) => <button aria-current={!workspace && screen === item.id ? "page" : undefined} aria-label={copy(language, item.zh, item.en)} className="desktop-dock-item" key={item.id} onClick={() => onNavigate(item.id)}><AppIcon appKey={item.id} /><span className="desktop-dock-label">{copy(language, item.zh, item.en)}</span></button>)}
      <button className="desktop-dock-item" aria-label={copy(language, "所有应用", "All applications")} onClick={() => setLauncherOpen(true)}><span className="desktop-launchpad-icon" aria-hidden="true">{Array.from({ length: 9 }, (_, i) => <i key={i} />)}</span><span className="desktop-dock-label">{copy(language, "所有应用", "All applications")}</span></button>
      {recentApps.length || auxiliaryApp ? <span className="desktop-dock-divider" aria-hidden="true" /> : null}
      {recentApps.map((app) => <button aria-current={workspace?.key === app.key ? "page" : undefined} aria-label={app.name} className="desktop-dock-item" data-open="true" key={app.key} onClick={() => onOpenApp(app.key)}><AppIcon appKey={app.key} /><span className="desktop-dock-label">{app.name}</span></button>)}
      {auxiliaryApp ? <button className="desktop-dock-item" data-open="true" aria-current={!workspace && screen === auxiliaryApp.id ? "page" : undefined} aria-label={copy(language, auxiliaryApp.zh, auxiliaryApp.en)} onClick={() => onNavigate(auxiliaryApp.id)}><AppIcon appKey={auxiliaryApp.id} /><span className="desktop-dock-label">{copy(language, auxiliaryApp.zh, auxiliaryApp.en)}</span></button> : null}
    </nav>
    <AppLauncher apps={apps} language={language} open={launcherOpen} onOpenChange={setLauncherOpen} onNavigate={onNavigate} onOpenApp={onOpenApp} />
  </div>;
}

function MenuClock({ language }: { language: Language }) {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => { const timer = window.setInterval(() => setNow(new Date()), 30_000); return () => window.clearInterval(timer); }, []);
  return <time className="desktop-clock" dateTime={now.toISOString()}>{new Intl.DateTimeFormat(language, { month: "short", day: "numeric", weekday: "short", hour: "2-digit", minute: "2-digit", hour12: false }).format(now)}</time>;
}
