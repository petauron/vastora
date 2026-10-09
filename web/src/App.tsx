import { lazy, Suspense, useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { CircleAlertIcon, CircleCheckIcon, LanguagesIcon, LogOutIcon, RefreshCwIcon, WifiOffIcon } from "lucide-react";
import { APIError, api } from "./api";
import { emptyAppData, loadScreenData, pathForScreen, screenFromPath } from "./app-data";
import { administratorPasswordMinLength } from "./lib/security";
import type { AppData, CenterUpdateStatus, Screen, SetupStatus } from "./types";
import type { Language } from "./translations";
import { Brand, PageHeading, copy, userError } from "./views/shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { ThemeToggle } from "@/components/theme";
import { DesktopShell } from "@/components/desktop/DesktopShell";
import { systemApplications, workspaceFromURL, workspacePath } from "@/components/desktop/navigation";
import { localized } from "./views/appAccess";
import { desktopApplications } from "./views/applicationLaunch";
import { Spinner } from "@/components/ui/spinner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { Turnstile } from "@/components/Turnstile";

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
const DesktopView = lazy(() => import("./views/DesktopView").then((module) => ({ default: module.DesktopView })));
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
  const [recentAppKeys, setRecentAppKeys] = useState<string[]>(() => { const key = workspaceFromURL(); return key ? [key] : []; });
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
      if (screenLoadGeneration.current !== generation || activeScreen.current !== target) return;
      setData((current) => ({ ...(current ?? emptyAppData(patch.status)), ...patch }));
      setLoadedScreens((current) => {
        if (current.has(target)) return current;
        return new Set(current).add(target);
      });
      setConnection("connected");
      setConnectionError(null);
      setLastSync(new Date());
    } catch (error) {
      if (controller.signal.aborted || screenLoadGeneration.current !== generation || activeScreen.current !== target) return;
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

  const navigate = useCallback((target: Screen, replace = false, appKey: string | null = null) => {
    const selectedApp = target === "apps" ? appKey : null;
    const path = selectedApp ? workspacePath(selectedApp) : pathForScreen(target);
    if (`${window.location.pathname}${window.location.search}` !== path) {
      window.history[replace ? "replaceState" : "pushState"]({}, "", path);
    }
    setWorkspaceKey(selectedApp);
    if (selectedApp) setRecentAppKeys((current) => [selectedApp, ...current.filter((key) => key !== selectedApp)].slice(0, 4));
    focusAfterNavigation.current = true;
    setNotice(null);
    activeScreen.current = target;
    setScreen(target);
    void loadScreen(target).catch(handleLoadError);
  }, [handleLoadError, loadScreen]);

  const initialize = useCallback(async () => {
    setPhase("loading");
    try {
      const setup = await api.setupStatus();
      setSetupStatus(setup);
      if (!setup.administratorConfigured) {
        setPhase("setup-admin");
        return;
      }
      if (!setup.onboardingComplete) {
        try {
          await api.organizations();
          setPhase("setup-wizard");
        } catch (error) {
          if (error instanceof APIError && error.status === 401) {
            setPhase("login");
            return;
          }
          throw error;
        }
        return;
      }
      try {
        const target = screenFromPath();
        activeScreen.current = target;
        await loadScreen(target);
        setScreen(target);
        setPhase("ready");
      } catch (error) {
        if (error instanceof APIError && error.status === 401) {
          setPhase("login");
          return;
        }
        throw error;
      }
    } catch (error) {
      setNotice({ message: userError(preferredLanguage(), error), detail: error instanceof Error ? error.message : undefined, error: true });
      setPhase("unavailable");
    }
  }, [loadScreen]);

  useEffect(() => { void initialize(); }, [initialize]);
  useEffect(() => () => {
    screenLoadGeneration.current += 1;
    screenLoadController.current?.abort();
  }, []);
  useEffect(() => { document.documentElement.lang = language; }, [language]);
  useEffect(() => {
    const onPopState = () => {
      const target = screenFromPath();
      const selectedApp = workspaceFromURL();
      setWorkspaceKey(selectedApp);
      if (selectedApp) setRecentAppKeys((current) => [selectedApp, ...current.filter((key) => key !== selectedApp)].slice(0, 4));
      focusAfterNavigation.current = true;
      activeScreen.current = target;
      setScreen(target);
      if (phase === "ready") void loadScreen(target).catch(handleLoadError);
    };
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, [phase, handleLoadError, loadScreen]);
  useEffect(() => {
    if (phase !== "ready" || !focusAfterNavigation.current || !loadedScreens.has(screen)) return;
    focusAfterNavigation.current = false;
    window.requestAnimationFrame(() => mainRef.current?.focus());
  }, [loadedScreens, phase, screen, workspaceKey]);
  useEffect(() => {
    const label = systemApplications.find((item) => item.id === screen)!;
    const app = data?.apps.find((value) => value.key === workspaceKey);
    const appName = app ? localized(app, language, "name") : data?.applications.find((value) => value.appKey === workspaceKey)?.name;
    document.title = `${workspaceKey ? appName ?? copy(language, "应用", "Application") : copy(language, label.zh, label.en)} · Vastora`;
  }, [language, screen, workspaceKey, data?.apps, data?.applications]);
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
  const refreshSettings = useCallback(() => activeScreen.current === "settings" ? loadScreen("settings") : Promise.resolve(), [loadScreen]);

  if (phase === "loading") return <CenteredState language={language} loading />;
  if (phase === "unavailable") return <CenteredState language={language} message={notice?.message} onRetry={initialize} />;
  if (phase === "setup-admin") return <CredentialPage language={language} mode="setup" onLanguage={setLanguage} onSubmit={async (username, password) => {
    await api.setupAdmin(username, password);
    setSetupStatus(await api.setupStatus());
    setPhase("setup-wizard");
  }} />;
  if (phase === "setup-wizard") return <Suspense fallback={<CenteredState language={language} loading />}><SetupWizard
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
        await loadScreen("nodes");
        setAddFirstNode(true);
        setPhase("ready");
      } catch (error) {
        setNotice({ message: userError(language, error), detail: error instanceof Error ? error.message : undefined, error: true });
        setPhase("unavailable");
      }
    }}
  /></Suspense>;
  if (phase === "login") return <CredentialPage language={language} loginProtection={setupStatus?.loginProtection} mode="login" onLanguage={setLanguage} onSubmit={async (username, password, turnstileToken) => { await api.login(username, password, turnstileToken); const setup = await api.setupStatus(); setSetupStatus(setup); if (!setup.onboardingComplete) { setPhase("setup-wizard"); return; } const target = screenFromPath(); activeScreen.current = target; await loadScreen(target); setScreen(target); setPhase("ready"); }} />;
  if (!data) return <CenteredState language={language} onRetry={initialize} />;

  const appName = (key: string) => { const app = data.apps.find((value) => value.key === key); return app ? localized(app, language, "name") : data.applications.find((value) => value.appKey === key)?.name ?? copy(language, "应用", "Application"); };
  const desktopApps = desktopApplications(data, language);
  const recentApps = recentAppKeys.flatMap((key) => { const app = desktopApps.find((app) => app.key === key); return app ? [app] : []; });
  const openApp = (key: string) => navigate("apps", false, key);
  return (
    <TooltipProvider>
      <a className="fixed left-4 top-4 z-50 -translate-y-24 rounded-lg bg-background px-3 py-2 text-sm font-medium shadow-lg transition-transform focus:translate-y-0" href="#main-content">{copy(language, "跳到主要内容", "Skip to main content")}</a>
      <DesktopShell screen={screen} language={language} connected={connection === "connected"} loading={loadingScreen === screen} workspace={workspaceKey ? { key: workspaceKey, name: appName(workspaceKey) } : null} apps={desktopApps} recentApps={recentApps} onNavigate={navigate} onOpenApp={openApp} onCloseWorkspace={() => { if (workspaceKey) setRecentAppKeys((keys) => keys.filter((key) => key !== workspaceKey)); navigate("home"); }} onLanguage={setLanguage}>
          <div className={screen === "home" ? "desktop-home-content" : `desktop-window-content${screen === "settings" ? " desktop-settings-window" : ""}${screen === "apps" && !workspaceKey ? " desktop-store-window" : ""}`} id="main-content" role="main" ref={mainRef} tabIndex={-1}>
            {connection === "reconnecting" ? <Alert aria-live="assertive" variant="destructive"><WifiOffIcon /><AlertTitle>{copy(language, "与 Center 的连接已中断", "Connection to Center was interrupted")}</AlertTitle><AlertDescription className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between"><span>{copy(language, `${userError(language, connectionError)} 页面保留的是上次成功同步的数据${lastSync ? `（${lastSync.toLocaleTimeString(language)}）` : ""}。`, `${userError(language, connectionError)} This page is showing the last successful data${lastSync ? ` from ${lastSync.toLocaleTimeString(language)}` : ""}.`)}</span><Button disabled={loadingScreen === screen} onClick={() => void loadScreen(screen).catch(handleLoadError)} size="sm" variant="outline">{loadingScreen === screen ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" />}{copy(language, "立即重试", "Retry now")}</Button></AlertDescription></Alert> : null}
            {notice ? <Alert aria-live="polite" variant={notice.error ? "destructive" : "default"}>{notice.error ? <CircleAlertIcon /> : <CircleCheckIcon />}<AlertTitle className="flex items-start justify-between gap-3"><span>{notice.message}</span><Button aria-label={copy(language, "关闭提示", "Dismiss notice")} onClick={() => setNotice(null)} size="xs" variant="ghost">{copy(language, "关闭", "Dismiss")}</Button></AlertTitle>{notice.detail && notice.detail !== notice.message ? <AlertDescription><details><summary className="cursor-pointer">{copy(language, "查看技术详情", "Technical details")}</summary><code className="mt-2 block break-all text-xs">{notice.detail}</code></details></AlertDescription> : null}</Alert> : null}
            <Suspense fallback={<ScreenLoading language={language} />}>
              {!loadedScreens.has(screen) ? <ScreenLoading language={language} /> : null}
              {loadedScreens.has(screen) && screen === "home" ? <DesktopView data={data} language={language} onNavigate={navigate} onOpenApp={openApp} /> : null}
              {loadedScreens.has(screen) && screen === "overview" ? <HomeView data={data} language={language} onNavigate={navigate} mutate={mutate} /> : null}
              {loadedScreens.has(screen) && screen === "nodes" ? <NodesView data={data} language={language} mutate={mutate} onAddFirstNodeHandled={() => setAddFirstNode(false)} onNavigate={navigate} startAdding={addFirstNode} /> : null}
              {loadedScreens.has(screen) && screen === "apps" ? <AppsView data={data} language={language} mutate={mutate} workspaceKey={workspaceKey} onOpenApp={openApp} onStore={() => navigate("apps")} onSettings={() => { navigate("settings"); window.history.replaceState({}, "", "/settings#catalog"); }} /> : null}
              {loadedScreens.has(screen) && screen === "network" ? <NetworkView data={data} language={language} mutate={mutate} /> : null}
              {loadedScreens.has(screen) && screen === "activity" ? <ActivityView actions={data.actions} agents={data.agents} language={language} onNavigate={navigate} /> : null}
              {loadedScreens.has(screen) && screen === "assistant" ? <AssistantView language={language} /> : null}
              {loadedScreens.has(screen) && screen === "settings" ? <SettingsView data={data} language={language} mutate={mutate} onCenterUpdateStatus={updateCenterStatus} onLogout={async () => { await api.logout(); setData(null); setLoadedScreens(new Set()); setRecentAppKeys([]); setPhase("login"); }} onNavigate={navigate} onRefresh={refreshSettings} /> : null}
            </Suspense>
          </div>
      </DesktopShell>
    </TooltipProvider>
  );
}

function CredentialPage({ language, loginProtection, mode, onLanguage, onSubmit }: { language: Language; loginProtection?: SetupStatus["loginProtection"]; mode: "setup" | "login"; onLanguage: (language: Language) => void; onSubmit: (username: string, password: string, turnstileToken: string) => Promise<void> }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [turnstileError, setTurnstileError] = useState("");
  const [turnstileToken, setTurnstileToken] = useState("");
  const [turnstileReset, setTurnstileReset] = useState(0);
  const captchaRequired = mode === "login" && loginProtection?.captchaRequired === true;
  const reportTurnstileError = useCallback(() => setTurnstileError(copy(language, "安全验证没有加载成功，请检查网络后重试。", "The security check did not load. Check your connection and retry.")), [language]);
  const acceptTurnstileToken = useCallback((token: string) => { setTurnstileToken(token); if (token) setTurnstileError(""); }, []);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy || captchaRequired && !turnstileToken) return;
    setBusy(true); setError(""); setTurnstileError("");
    try {
      await onSubmit(username, password, turnstileToken);
    } catch (submitError) {
      if (submitError instanceof APIError) {
        if (submitError.code === "captcha_failed" && captchaRequired) {
          setTurnstileError(copy(language, "安全验证失败，请完成新的验证后再试。", "The security check failed. Complete a new check and try again."));
        } else if (submitError.code === "login_throttled" || submitError.code === "login_protection_unavailable" || submitError.code === "captcha_failed") {
          setError(copy(language, "暂时无法登录，请稍后再试。", "Unable to sign in right now. Try again later."));
        } else if (submitError.code === "invalid_credentials") {
          setError(copy(language, "账号或密码不正确。", "The username or password is incorrect."));
        } else {
          setError(userError(language, submitError));
        }
        if (captchaRequired || submitError.captchaRequired) setTurnstileReset((current) => current + 1);
      } else {
        setError(userError(language, submitError));
      }
    } finally { setBusy(false); }
  };
  return (
    <main className="grid min-h-svh place-items-center bg-muted/35 p-5">
      <div className="flex w-full max-w-sm flex-col gap-5">
        <div className="flex items-center justify-between"><Brand /><div className="flex items-center gap-1"><ThemeToggle language={language} /><Button aria-label={copy(language, "切换语言", "Change language")} onClick={() => onLanguage(language === "zh-CN" ? "en" : "zh-CN")} size="icon" variant="ghost"><LanguagesIcon /></Button></div></div>
        <Card>
          <CardHeader><CardTitle>{mode === "setup" ? copy(language, "创建管理员", "Create administrator") : copy(language, "登录 Center", "Sign in to Center")}</CardTitle><CardDescription>{mode === "setup" ? copy(language, "创建账号后即可继续设置。", "Create an account to continue setup.") : copy(language, "使用管理员账号继续。", "Continue with your administrator account.")}</CardDescription></CardHeader>
          <CardContent>
            <form onSubmit={(event) => void submit(event)}><FieldGroup><Field data-invalid={Boolean(error)}><FieldLabel htmlFor="username">{copy(language, "账号", "Username")}</FieldLabel><Input aria-invalid={Boolean(error)} autoComplete="username" id="username" minLength={3} onChange={(event) => setUsername(event.target.value)} required value={username} /></Field><Field data-invalid={Boolean(error)}><FieldLabel htmlFor="password">{copy(language, "密码", "Password")}</FieldLabel><Input aria-describedby={error ? "credential-error" : undefined} aria-invalid={Boolean(error)} autoComplete={mode === "setup" ? "new-password" : "current-password"} id="password" minLength={mode === "setup" ? administratorPasswordMinLength : undefined} onChange={(event) => setPassword(event.target.value)} required type="password" value={password} />{mode === "setup" ? <FieldDescription>{copy(language, "至少 10 个字符。", "At least 10 characters.")}</FieldDescription> : null}{error ? <FieldError id="credential-error" role="alert">{error}</FieldError> : null}</Field>{captchaRequired && loginProtection?.turnstileSiteKey ? <Field data-invalid={Boolean(turnstileError)}><FieldLabel htmlFor="center-login-turnstile">{copy(language, "安全验证", "Security check")}</FieldLabel><Turnstile language={language} onError={reportTurnstileError} onToken={acceptTurnstileToken} resetKey={turnstileReset} siteKey={loginProtection.turnstileSiteKey} />{turnstileError ? <><FieldError role="alert">{turnstileError}</FieldError><Button onClick={() => { setTurnstileError(""); setTurnstileReset((current) => current + 1); }} size="sm" type="button" variant="outline">{copy(language, "重新加载验证", "Reload security check")}</Button></> : null}</Field> : null}<Button disabled={busy || captchaRequired && !turnstileToken} size="lg" type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{mode === "setup" ? copy(language, "创建并继续", "Create and continue") : copy(language, "登录", "Sign in")}</Button></FieldGroup></form>
          </CardContent>
        </Card>
      </div>
    </main>
  );
}

function CenteredState({ language, loading, message, onRetry }: { language: Language; loading?: boolean; message?: string; onRetry?: () => Promise<void> }) {
  return <main className="grid min-h-svh place-items-center p-6"><div className="flex w-full max-w-sm flex-col gap-5"><Brand /><Card><CardHeader><CardTitle>{loading ? copy(language, "正在连接…", "Connecting…") : copy(language, "无法连接 Center", "Center unavailable")}</CardTitle><CardDescription>{message}</CardDescription></CardHeader>{onRetry ? <CardContent><Button onClick={() => void onRetry()} variant="outline">{copy(language, "重试", "Retry")}</Button></CardContent> : null}</Card></div></main>;
}

function ScreenLoading({ language }: { language: Language }) {
  return <div aria-live="polite" className="flex min-h-48 items-center justify-center gap-3 rounded-2xl border bg-card text-sm text-muted-foreground"><Spinner />{copy(language, "正在准备页面…", "Preparing this page…")}</div>;
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
