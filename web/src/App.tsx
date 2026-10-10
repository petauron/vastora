import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import { CircleAlertIcon, CircleCheckIcon, LogOutIcon, RefreshCwIcon } from "lucide-react";
import { AuthShell } from "@/components/auth/AuthShell";
import { CredentialPage } from "./views/CredentialPage";
import { APIError, api } from "./api";
import { emptyAppData, loadScreenData, pathForScreen, screenFromPath } from "./app-data";
import type { AppData, CenterUpdateStatus, Screen, SetupStatus } from "./types";
import type { Language } from "./translations";
import { PageHeading, copy, userError } from "./views/shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { dismissWindow, emptyWindows, frontWindow, openWindow, windowID, type DesktopWindowEntry } from "@/components/desktop/windowState";
import { DesktopShell } from "@/components/desktop/DesktopShell";
import { DesktopView } from "./views/DesktopView";
import { systemApplications, workspaceFromURL, workspacePath } from "@/components/desktop/navigation";
import { localized } from "./views/appAccess";
import { desktopApplications } from "./views/applicationLaunch";
import { Spinner } from "@/components/ui/spinner";
import { TooltipProvider } from "@/components/ui/tooltip";

export type { AppData, Screen } from "./types";
type Phase = "loading" | "setup-admin" | "setup-wizard" | "login" | "ready" | "unavailable";
export type Mutate = (operation: () => Promise<unknown>, success?: string, options?: { reportError?: boolean }) => Promise<void>;

const preferredLanguage = (): Language => {
  const saved = window.localStorage.getItem("vastora.language");
  if (saved === "en" || saved === "zh-CN") return saved;
  return navigator.language.toLowerCase().startsWith("zh") ? "zh-CN" : "en";
};

const ActivityView = lazy(() => import("./views/ActivityView").then((module) => ({ default: module.ActivityView })));
const AssistantView = lazy(() => import("./views/AssistantView").then((module) => ({ default: module.AssistantView })));
const AppsView = lazy(() => import("./views/AppsView").then((module) => ({ default: module.AppsView })));
const HomeView = lazy(() => import("./views/HomeView").then((module) => ({ default: module.HomeView })));
const NetworkView = lazy(() => import("./views/NetworkView").then((module) => ({ default: module.NetworkView })));
const NodesView = lazy(() => import("./views/NodesView").then((module) => ({ default: module.NodesView })));
const SettingsView = lazy(() => import("./views/SettingsView").then((module) => ({ default: module.SettingsView })));
const SetupWizard = lazy(() => import("./views/SetupWizard").then((module) => ({ default: module.SetupWizard })));

export function App() {
  const [language, setLanguageState] = useState<Language>(preferredLanguage);
  const [phase, setPhase] = useState<Phase>("loading");
  const [screen, setScreen] = useState<Screen>(screenFromPath);
  const [workspaceKey, setWorkspaceKey] = useState<string | null>(workspaceFromURL);
  const [windows, setWindows] = useState(() => openWindow(emptyWindows(), screenFromPath(), workspaceFromURL()));
  const [data, setData] = useState<AppData | null>(null);
  const [loadedScreens, setLoadedScreens] = useState<Set<Screen>>(() => new Set());
  const [loadingScreen, setLoadingScreen] = useState<Screen | null>(null);
  const [setupStatus, setSetupStatus] = useState<SetupStatus | null>(null);
  const [addFirstNode, setAddFirstNode] = useState(false);
  const [notice, setNotice] = useState<{ message: string; detail?: string; error?: boolean } | null>(null);
  const [connection, setConnection] = useState<"connected" | "reconnecting">("connected");
  const [connectionError, setConnectionError] = useState<unknown>(null);
  const [lastSync, setLastSync] = useState<Date | null>(null);
  const mainRef = useRef<HTMLDivElement>(null);
  const focusAfterNavigation = useRef(false);
  const activeScreen = useRef(screen);
  const initializationController = useRef<AbortController | null>(null);
  const screenLoadGeneration = useRef(0);
  const screenLoadController = useRef<AbortController | null>(null);

  useEffect(() => {
    if (!notice || notice.error || phase !== "ready") return;
    const timer = window.setTimeout(() => setNotice(null), 5000);
    return () => window.clearTimeout(timer);
  }, [notice, phase]);

  const setLanguage = (next: Language) => {
    window.localStorage.setItem("vastora.language", next);
    setLanguageState(next);
  };

  const loadScreen = useCallback(async (target: Screen) => {
    const generation = screenLoadGeneration.current + 1;
    screenLoadGeneration.current = generation;
    screenLoadController.current?.abort();
    const controller = new AbortController();
    screenLoadController.current = controller;
    setLoadingScreen(target);
    try {
      const patch = await loadScreenData(target, controller.signal);
      if (screenLoadGeneration.current !== generation || activeScreen.current !== target) return false;
      setData((current) => ({ ...(current ?? emptyAppData(patch.status)), ...patch }));
      setLoadedScreens((current) => {
        if (current.has(target)) return current;
        return new Set(current).add(target);
      });
      setConnection("connected");
      setConnectionError(null);
      setLastSync(new Date());
      return true;
    } catch (error) {
      if (controller.signal.aborted || screenLoadGeneration.current !== generation || activeScreen.current !== target) return false;
      if (!(error instanceof APIError && error.status === 401)) {
        setConnection("reconnecting");
        setConnectionError(error);
      }
      throw error;
    } finally {
      if (screenLoadGeneration.current === generation) {
        if (screenLoadController.current === controller) screenLoadController.current = null;
        setLoadingScreen(null);
      }
    }
  }, []);

  const handleLoadError = useCallback((error: unknown) => {
    if (error instanceof APIError && error.status === 401) {
      setPhase("login");
    }
  }, []);

  const selectRoute = useCallback((target: Screen, replace = false, appKey: string | null = null, focusContent = true) => {
    const selectedApp = target === "apps" ? appKey : null;
    const path = selectedApp ? workspacePath(selectedApp) : pathForScreen(target);
    if (`${window.location.pathname}${window.location.search}` !== path) {
      window.history[replace ? "replaceState" : "pushState"]({}, "", path);
    }
    setWorkspaceKey(selectedApp);
    focusAfterNavigation.current = focusContent;
    setNotice(null);
    activeScreen.current = target;
    setScreen(target);
    void loadScreen(target).catch(handleLoadError);
  }, [handleLoadError, loadScreen]);
  const navigate = useCallback((target: Screen, replace = false, appKey: string | null = null) => {
    setWindows((current) => openWindow(current, target, target === "apps" ? appKey : null));
    selectRoute(target, replace, appKey);
  }, [selectRoute]);
  const focusWindow = (entry: DesktopWindowEntry) => {
    if (windowID(activeScreen.current, workspaceFromURL()) === entry.id) return;
    setWindows((current) => openWindow(current, entry.screen, entry.workspaceKey));
    // Clicking a field in a background window must not move focus to its heading.
    selectRoute(entry.screen, false, entry.workspaceKey, false);
  };
  const dismiss = (id: string, close: boolean) => {
    const next = dismissWindow(windows, id, close);
    setWindows(next);
    const front = frontWindow(next);
    selectRoute(front?.screen ?? "home", false, front?.workspaceKey ?? null);
  };

  const initialize = useCallback(async () => {
    initializationController.current?.abort();
    const controller = new AbortController();
    initializationController.current = controller;
    screenLoadGeneration.current += 1;
    screenLoadController.current?.abort();
    setNotice(null);
    setPhase("loading");
    try {
      const setup = await api.setupStatus(controller.signal);
      if (controller.signal.aborted) return;
      setSetupStatus(setup);
      if (!setup.administratorConfigured) {
        setPhase("setup-admin");
        return;
      }
      if (!setup.onboardingComplete) {
        await api.organizations(controller.signal);
        if (!controller.signal.aborted) setPhase("setup-wizard");
        return;
      }
      const target = screenFromPath();
      activeScreen.current = target;
      const loaded = await loadScreen(target);
      if (controller.signal.aborted || !loaded) return;
      setScreen(target);
      setPhase("ready");
    } catch (error) {
      // Effect cleanup, another initialization or a refresh invalidates this run.
      // Only the current request may decide whether to show login or an error.
      if (controller.signal.aborted) return;
      if (error instanceof APIError && error.status === 401) {
        setPhase("login");
        return;
      }
      setNotice({ message: userError(preferredLanguage(), error), detail: error instanceof Error ? error.message : undefined, error: true });
      setPhase("unavailable");
    }
  }, [loadScreen]);

  useEffect(() => {
    void initialize();
    return () => {
      initializationController.current?.abort();
      screenLoadGeneration.current += 1;
      screenLoadController.current?.abort();
    };
  }, [initialize]);
  useEffect(() => { document.documentElement.lang = language; }, [language]);
  useEffect(() => {
    const onPopState = () => {
      const target = screenFromPath();
      const selectedApp = workspaceFromURL();
      setWorkspaceKey(selectedApp);
      setWindows((current) => openWindow(current, target, selectedApp));
      focusAfterNavigation.current = true;
      activeScreen.current = target;
      setScreen(target);
      if (phase === "ready") void loadScreen(target).catch(handleLoadError);
      else if (phase === "loading") void initialize();
    };
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, [phase, handleLoadError, initialize, loadScreen]);
  useEffect(() => {
    if (phase !== "ready" || !focusAfterNavigation.current || !loadedScreens.has(screen)) return;
    focusAfterNavigation.current = false;
    window.requestAnimationFrame(() => mainRef.current?.focus());
  }, [loadedScreens, phase, screen, workspaceKey]);
  useEffect(() => {
    const label = systemApplications.find((item) => item.id === screen)!;
    const app = data?.apps.find((value) => value.key === workspaceKey);
    const appName = app ? localized(app, language, "name") : data?.applications.find((value) => value.appKey === workspaceKey)?.name;
    const title = phase === "login" ? copy(language, "登录 Center", "Sign in to Center")
      : phase === "setup-admin" ? copy(language, "创建管理员", "Create administrator")
      : phase === "loading" ? copy(language, "正在连接…", "Connecting…")
      : phase === "unavailable" ? copy(language, "无法连接 Center", "Center unavailable")
      : workspaceKey ? appName ?? copy(language, "应用", "Application") : copy(language, label.zh, label.en);
    document.title = `${title} · Vastora`;
  }, [phase, language, screen, workspaceKey, data?.apps, data?.applications]);
  useEffect(() => {
    if (phase !== "ready") return;
    let cancelled = false;
    let timer = 0;
    const interval = screen === "home" || screen === "overview" || screen === "settings" ? 30000 : 5000;
    const poll = async () => {
      if (document.visibilityState === "visible") {
        try { await loadScreen(screen); } catch (error) {
          if (error instanceof APIError && error.status === 401 && !cancelled) setPhase("login");
        }
      }
      if (!cancelled) timer = window.setTimeout(() => void poll(), interval);
    };
    timer = window.setTimeout(() => void poll(), interval);
    return () => { cancelled = true; window.clearTimeout(timer); };
  }, [phase, screen, loadScreen]);

  const mutate = useCallback<Mutate>(async (operation, success, options) => {
    setNotice(null);
    try {
      await operation();
    } catch (error) {
      if (error instanceof APIError && error.status === 401) {
        setPhase("login");
        return;
      }
      if (!(error instanceof APIError)) {
        setConnection("reconnecting");
        setConnectionError(error);
      }
      if (options?.reportError !== false) setNotice({ message: userError(language, error), detail: error instanceof Error ? error.message : undefined, error: true });
      throw error;
    }
    setNotice(success ? { message: success } : null);
    try {
      await loadScreen(activeScreen.current);
    } catch (error) {
      if (error instanceof APIError && error.status === 401) {
        setPhase("login");
      } else {
        const refreshMessage = copy(language, "操作已完成，但页面状态刷新失败；系统会自动重试。", "The operation completed, but the page could not refresh; it will retry automatically.");
        setNotice({ message: success ? `${success} ${refreshMessage}` : refreshMessage });
      }
    }
  }, [language, loadScreen]);
  const updateCenterStatus = useCallback((centerUpdate: CenterUpdateStatus) => {
    screenLoadGeneration.current += 1;
    screenLoadController.current?.abort();
    screenLoadController.current = null;
    setLoadingScreen(null);
    setData((current) => current ? { ...current, centerUpdate, status: { ...current.status, version: centerUpdate.currentVersion } } : current);
  }, []);
  const refreshSettings = useCallback(async () => { if (activeScreen.current === "settings") await loadScreen("settings"); }, [loadScreen]);

  if (phase === "loading") return <StartupState language={language} desktop={screen === "home"} />;
  if (phase === "unavailable") return <CenteredState language={language} message={notice?.message} onRetry={initialize} />;
  if (phase === "setup-admin") return <CredentialPage language={language} mode="setup" onLanguage={setLanguage} onSubmit={async (username, password) => {
    await api.setupAdmin(username, password);
    setSetupStatus(await api.setupStatus());
    setPhase("setup-wizard");
  }} />;
  if (phase === "setup-wizard") return <Suspense fallback={<StartupState language={language} desktop={screen === "home"} />}><SetupWizard
    builtinHeadscaleAvailable={setupStatus?.builtinHeadscaleAvailable ?? false}
    cloudflareConfigured={setupStatus?.cloudflareConfigured ?? false}
    cloudflareTurnstileConfigured={setupStatus?.cloudflareTurnstileConfigured ?? false}
    cloudflareOAuthAvailable={setupStatus?.cloudflareOAuthAvailable ?? false}
    publicNetworkHelperAvailable={setupStatus?.publicNetworkHelperAvailable ?? false}
    cloudflareZone={setupStatus?.cloudflareZone}
    language={language}
    onLanguage={setLanguage}
    gatewayAddressCandidates={setupStatus?.gatewayAddressCandidates ?? []}
    observedPublicAddress={setupStatus?.observedPublicAddress ?? ""}
    publicAddressDetection={setupStatus?.publicAddressDetection ?? "unavailable"}
    publicAddressCandidates={setupStatus?.publicAddressCandidates ?? []}
    suggestedGatewayAddress={setupStatus?.suggestedGatewayAddress ?? ""}
    suggestedAgentConnectUrl={setupStatus?.suggestedAgentConnectUrl ?? ""}
    onComplete={async (input) => {
      await api.completeSetup(input);
      setSetupStatus(await api.setupStatus());
      try {
        window.history.replaceState({}, "", pathForScreen("nodes"));
        activeScreen.current = "nodes";
        setScreen("nodes");
        setWorkspaceKey(null);
        setWindows(openWindow(emptyWindows(), "nodes"));
        await loadScreen("nodes");
        setAddFirstNode(true);
        setPhase("ready");
      } catch (error) {
        setNotice({ message: userError(language, error), detail: error instanceof Error ? error.message : undefined, error: true });
        setPhase("unavailable");
      }
    }}
  /></Suspense>;
  if (phase === "login") return <CredentialPage language={language} loginProtection={setupStatus?.loginProtection} mode="login" onLanguage={setLanguage} onSubmit={async (username, password, turnstileToken) => { await api.login(username, password, turnstileToken); const setup = await api.setupStatus(); setSetupStatus(setup); if (!setup.onboardingComplete) { setPhase("setup-wizard"); return; } const target = screenFromPath(); activeScreen.current = target; await loadScreen(target); setScreen(target); setWorkspaceKey(workspaceFromURL()); setWindows(openWindow(emptyWindows(), target, workspaceFromURL())); setPhase("ready"); }} />;
  if (!data) return <StartupState language={language} desktop={screen === "home"} />;

  const appName = (key: string) => { const app = data.apps.find((value) => value.key === key); return app ? localized(app, language, "name") : data.applications.find((value) => value.appKey === key)?.name ?? copy(language, "应用", "Application"); };
  const desktopApps = desktopApplications(data, language);
  const windowTitle = (entry: DesktopWindowEntry) => { const item = systemApplications.find((app) => app.id === entry.screen)!; return entry.workspaceKey ? appName(entry.workspaceKey) : copy(language, item.zh, item.en); };
  const openApp = (key: string) => navigate("apps", false, key);
  const statusAlerts = <>
    {connection === "reconnecting" ? <Alert aria-live="assertive" variant="destructive"><CircleAlertIcon /><AlertTitle>{copy(language, "与 Center 的连接已中断", "Connection to Center was interrupted")}</AlertTitle><AlertDescription className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between"><span>{copy(language, `${userError(language, connectionError)} 页面保留的是上次成功同步的数据${lastSync ? `（${lastSync.toLocaleTimeString(language)}）` : ""}。`, `${userError(language, connectionError)} This page is showing the last successful data${lastSync ? ` from ${lastSync.toLocaleTimeString(language)}` : ""}.`)}</span><Button disabled={loadingScreen === screen} onClick={() => void loadScreen(screen).catch(handleLoadError)} size="sm" variant="outline">{loadingScreen === screen ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" />}{copy(language, "立即重试", "Retry now")}</Button></AlertDescription></Alert> : null}
    {notice ? <Alert aria-live="polite" variant={notice.error ? "destructive" : "default"}>{notice.error ? <CircleAlertIcon /> : <CircleCheckIcon />}<AlertTitle className="flex items-start justify-between gap-3"><span>{notice.message}</span><Button aria-label={copy(language, "关闭提示", "Dismiss notice")} onClick={() => setNotice(null)} size="xs" variant="ghost">{copy(language, "关闭", "Dismiss")}</Button></AlertTitle>{notice.detail && notice.detail !== notice.message ? <AlertDescription><details><summary className="cursor-pointer">{copy(language, "查看技术详情", "Technical details")}</summary><code className="mt-2 block break-all text-xs">{notice.detail}</code></details></AlertDescription> : null}</Alert> : null}
  </>;
  return (
    <TooltipProvider>
      <a className="fixed left-4 top-4 z-50 -translate-y-24 rounded-lg bg-background px-3 py-2 text-sm font-medium shadow-lg transition-transform focus:translate-y-0" href="#main-content">{copy(language, "跳到主要内容", "Skip to main content")}</a>
      <DesktopShell screen={screen} language={language} loading={loadingScreen === screen} workspace={workspaceKey ? { key: workspaceKey, name: appName(workspaceKey) } : null} apps={desktopApps} windows={windows} windowTitle={windowTitle} onFocusWindow={focusWindow} onDismissWindow={dismiss} onNavigate={navigate} onOpenApp={openApp} onLanguage={setLanguage} desktop={
        <div className="desktop-home-content" id={screen === "home" ? "main-content" : undefined} role={screen === "home" ? "main" : undefined} ref={screen === "home" ? mainRef : undefined} tabIndex={screen === "home" ? -1 : undefined}>
          {screen === "home" ? statusAlerts : null}
          <DesktopView data={data} language={language} onNavigate={navigate} onOpenApp={openApp} />
        </div>
      } renderWindow={(entry) => {
        const active = entry.id === windowID(screen, workspaceKey);
        const { screen: windowScreen, workspaceKey: windowWorkspace } = entry;
        // Each keyed frame keeps its own view instance and local form/scroll state.
        return (
          <div className={`desktop-window-content${windowScreen === "settings" ? " desktop-settings-window" : ""}${windowScreen === "apps" && !windowWorkspace ? " desktop-store-window" : ""}`} id={active ? "main-content" : undefined} role={active ? "main" : undefined} ref={active ? mainRef : undefined} tabIndex={-1}>
            {active ? statusAlerts : null}
            <Suspense fallback={<ScreenLoading language={language} desktop={false} />}>
              {!loadedScreens.has(windowScreen) ? <ScreenLoading language={language} desktop={false} /> : null}
              {loadedScreens.has(windowScreen) && windowScreen === "overview" ? <HomeView data={data} language={language} onNavigate={navigate} mutate={mutate} /> : null}
              {loadedScreens.has(windowScreen) && windowScreen === "nodes" ? <NodesView data={data} language={language} mutate={mutate} onAddFirstNodeHandled={() => setAddFirstNode(false)} onNavigate={navigate} startAdding={addFirstNode} /> : null}
              {loadedScreens.has(windowScreen) && windowScreen === "apps" ? <AppsView active={active} data={data} language={language} mutate={mutate} workspaceKey={windowWorkspace} onOpenApp={openApp} onStore={() => navigate("apps")} onSettings={() => { navigate("settings"); window.history.replaceState({}, "", "/settings#catalog"); window.dispatchEvent(new HashChangeEvent("hashchange")); }} /> : null}
              {loadedScreens.has(windowScreen) && windowScreen === "network" ? <NetworkView data={data} language={language} mutate={mutate} /> : null}
              {loadedScreens.has(windowScreen) && windowScreen === "activity" ? <ActivityView actions={data.actions} agents={data.agents} language={language} onNavigate={navigate} /> : null}
              {loadedScreens.has(windowScreen) && windowScreen === "assistant" ? <AssistantView language={language} /> : null}
              {loadedScreens.has(windowScreen) && windowScreen === "settings" ? <SettingsView data={data} language={language} mutate={mutate} onCenterUpdateStatus={updateCenterStatus} onLogout={async () => { await api.logout(); setData(null); setLoadedScreens(new Set()); setWindows(emptyWindows()); setPhase("login"); }} onNavigate={navigate} onRefresh={refreshSettings} /> : null}
            </Suspense>
          </div>
        );
      }} />
    </TooltipProvider>
  );
}

function StartupState({ language, desktop }: { language: Language; desktop: boolean }) {
  return <main className="desktop-shell desktop-startup"><ScreenLoading language={language} desktop={desktop} /></main>;
}

function CenteredState({ language, message, onRetry }: { language: Language; message?: string; onRetry: () => Promise<void> }) {
  return <AuthShell language={language} title={copy(language, "无法连接 Center", "Center unavailable")} description={message}>
    <Button className="auth-submit" onClick={() => void onRetry()}><RefreshCwIcon aria-hidden="true" />{copy(language, "重试", "Retry")}</Button>
  </AuthShell>;
}

function ScreenLoading({ language, desktop }: { language: Language; desktop: boolean }) {
  return <div className="screen-loading" role="status" aria-live="polite" aria-atomic="true">
    <div className="screen-loading-indicator">
      <Spinner aria-hidden="true" role="presentation" className="motion-reduce:animate-none" />
      <span>{desktop ? copy(language, "正在载入桌面…", "Loading desktop…") : copy(language, "正在载入…", "Loading…")}</span>
    </div>
  </div>;
}

export function EmptyPage({ language, title, description }: { language: Language; title?: string; description?: string }) {
  return <section className="flex flex-col gap-6"><PageHeading title={title ?? copy(language, "暂无内容", "Nothing here yet")} description={description} /></section>;
}

export function SignOutButton({ language, onLogout }: { language: Language; onLogout: () => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const signOut = async () => { setBusy(true); setError(""); try { await onLogout(); } catch (signOutError) { setError(userError(language, signOutError)); } finally { setBusy(false); } };
  return <div className="flex flex-col items-end gap-2"><Button disabled={busy} onClick={() => void signOut()} variant="outline">{busy ? <Spinner data-icon="inline-start" /> : <LogOutIcon data-icon="inline-start" />}{copy(language, "退出登录", "Sign out")}</Button>{error ? <span className="max-w-64 text-right text-xs text-destructive" role="alert">{error}</span> : null}</div>;
}
