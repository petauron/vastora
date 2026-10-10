import { useEffect, useRef, useState, type ReactNode } from "react";
import { BellIcon, LanguagesIcon, SearchIcon, Settings2Icon } from "lucide-react";
import type { Language } from "@/translations";
import type { Screen } from "@/types";
import type { DesktopApplication } from "@/views/applicationLaunch";
import { copy } from "@/views/shared";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuGroup, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { ThemeToggle } from "@/components/theme";
import { AppIcon } from "./AppIcon";
import { AppLauncher } from "./AppLauncher";
import { systemApplications } from "./navigation";
import { DesktopWindow, type DesktopWindowHandle } from "./DesktopWindow";
import { windowID, type DesktopWindowEntry, type DesktopWindows } from "./windowState";

export function DesktopShell({ renderWindow, desktop, screen, language, loading, workspace, apps, windows, windowTitle, onFocusWindow, onDismissWindow, onNavigate, onOpenApp, onLanguage }: {
  renderWindow: (entry: DesktopWindowEntry) => ReactNode;
  desktop: ReactNode;
  screen: Screen;
  language: Language;
  loading: boolean;
  workspace: { key: string; name: string } | null;
  apps: DesktopApplication[];
  windows: DesktopWindows;
  windowTitle: (entry: DesktopWindowEntry) => string;
  onFocusWindow: (entry: DesktopWindowEntry) => void;
  onDismissWindow: (id: string, close: boolean) => void;
  onNavigate: (screen: Screen) => void;
  onOpenApp: (key: string) => void;
  onLanguage: (language: Language) => void;
}) {
  const [launcherOpen, setLauncherOpen] = useState(false);
  const [launcherMode, setLauncherMode] = useState<"all" | "search">("search");
  const showLauncher = (mode: "all" | "search") => { setLauncherMode(mode); setLauncherOpen(true); };
  const activeWindow = useRef<DesktopWindowHandle>(null);
  const [compact, setCompact] = useState(() => window.innerWidth <= 767);
  useEffect(() => { const resize = () => setCompact(window.innerWidth <= 767); window.addEventListener("resize", resize); return () => window.removeEventListener("resize", resize); }, []);
  const current = systemApplications.find((item) => item.id === screen)!;
  const title = workspace?.name ?? copy(language, current.zh, current.en);
  const activeID = screen === "home" ? null : windowID(screen, workspace?.key);
  const pinKeys: Screen[] = ["home", "apps", "nodes", "settings"];
  const pins = systemApplications.filter((app) => pinKeys.includes(app.id));
  const auxiliaryWindows = windows.entries.filter((entry) => entry.workspaceKey || !pinKeys.includes(entry.screen));
  const onNavigateWindow = (entry: DesktopWindowEntry) => entry.workspaceKey ? onOpenApp(entry.workspaceKey) : onNavigate(entry.screen);
  const minimize = () => { if (activeID) onDismissWindow(activeID, false); };
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") { event.preventDefault(); setLauncherMode("search"); setLauncherOpen((open) => !open); }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);
  return <div className="desktop-shell" data-desktop={screen === "home"}>
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
          <DropdownMenuItem disabled={screen === "home" || compact} onClick={() => activeWindow.current?.toggleMaximize()}>{copy(language, "最大化 / 还原", "Maximize / Restore")}</DropdownMenuItem>
        </DropdownMenuGroup>{windows.entries.length ? <><DropdownMenuSeparator /><DropdownMenuGroup>{windows.entries.map((entry) => <DropdownMenuItem key={entry.id} onClick={() => onNavigateWindow(entry)}>{windowTitle(entry)}{entry.minimized ? <span className="ml-auto text-xs text-muted-foreground">{copy(language, "已最小化", "Minimized")}</span> : null}</DropdownMenuItem>)}</DropdownMenuGroup></> : null}</DropdownMenuContent>
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
    <div className="desktop-surface" inert={compact && screen !== "home" || undefined}>{desktop}</div>
    <div className="desktop-windows">{windows.entries.map((entry, index) => <DesktopWindow key={entry.id} ref={entry.id === activeID ? activeWindow : undefined} entry={entry} title={windowTitle(entry)} active={entry.id === activeID} stackIndex={windows.stack.indexOf(entry.id)} launchIndex={index} language={language} loading={loading && entry.id === activeID} onFocus={onFocusWindow} onDismiss={onDismissWindow} showLauncher={showLauncher}>{renderWindow(entry)}</DesktopWindow>)}</div>
    <nav className="desktop-dock" aria-label={copy(language, "Dock 应用切换", "Dock applications")}>
      {pins.map((item) => <button aria-current={!workspace && screen === item.id ? "page" : undefined} aria-label={copy(language, item.zh, item.en)} className="desktop-dock-item" data-running={windows.entries.some((entry) => entry.id === item.id) || undefined} key={item.id} onClick={() => onNavigate(item.id)}><AppIcon appKey={item.id} /><span className="desktop-dock-label">{copy(language, item.zh, item.en)}</span></button>)}
      <button className="desktop-dock-item" aria-label={copy(language, "所有应用", "All applications")} onClick={() => showLauncher("all")}><span className="desktop-launchpad-icon" aria-hidden="true">{Array.from({ length: 9 }, (_, i) => <i key={i} />)}</span><span className="desktop-dock-label">{copy(language, "所有应用", "All applications")}</span></button>
      {auxiliaryWindows.length ? <span className="desktop-dock-divider" aria-hidden="true" /> : null}
      {auxiliaryWindows.map((entry) => <button key={entry.id} className="desktop-dock-item" data-running="true" aria-current={activeID === entry.id ? "page" : undefined} aria-label={windowTitle(entry)} onClick={() => onNavigateWindow(entry)}><AppIcon appKey={entry.workspaceKey ?? entry.screen} /><span className="desktop-dock-label">{windowTitle(entry)}</span></button>)}
    </nav>
    <AppLauncher key={launcherMode} mode={launcherMode} apps={apps} language={language} open={launcherOpen} onOpenChange={setLauncherOpen} onNavigate={onNavigate} onOpenApp={onOpenApp} />
  </div>;
}

function MenuClock({ language }: { language: Language }) {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => { const timer = window.setInterval(() => setNow(new Date()), 30_000); return () => window.clearInterval(timer); }, []);
  return <time className="desktop-clock" dateTime={now.toISOString()}>{new Intl.DateTimeFormat(language, { month: "short", day: "numeric", weekday: "short", hour: "2-digit", minute: "2-digit", hour12: false }).format(now)}</time>;
}
