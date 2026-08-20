import { lazy, Suspense, useCallback, useEffect, useState, type FormEvent } from "react";
import { AppWindowIcon, CircleAlertIcon, CircleCheckIcon, HistoryIcon, HomeIcon, LanguagesIcon, LogOutIcon, NetworkIcon, ServerIcon, SettingsIcon, type LucideIcon } from "lucide-react";
import { APIError, api } from "./api";
import { emptyAppData, loadScreenData, pathForScreen, screenFromPath } from "./app-data";
import type { AppData, Screen, SetupStatus } from "./types";
import type { Language } from "./translations";
import { Brand, PageHeading, copy, userError } from "./views/shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Sidebar, SidebarContent, SidebarFooter, SidebarGroup, SidebarGroupContent, SidebarHeader, SidebarInset, SidebarMenu, SidebarMenuButton, SidebarMenuItem, SidebarProvider, SidebarTrigger, useSidebar } from "@/components/ui/sidebar";
import { Spinner } from "@/components/ui/spinner";
import { TooltipProvider } from "@/components/ui/tooltip";

export type { AppData, Screen } from "./types";
type Phase = "loading" | "setup-admin" | "setup-wizard" | "login" | "ready" | "unavailable";
export type Mutate = (operation: () => Promise<unknown>, success?: string) => Promise<void>;

const preferredLanguage = (): Language => {
  const saved = window.localStorage.getItem("vastora.language");
  if (saved === "en" || saved === "zh-CN") return saved;
  return navigator.language.toLowerCase().startsWith("zh") ? "zh-CN" : "en";
};

const navigation = [
  { id: "home" as const, icon: HomeIcon, zh: "主页", en: "Home" },
  { id: "nodes" as const, icon: ServerIcon, zh: "节点", en: "Nodes" },
  { id: "apps" as const, icon: AppWindowIcon, zh: "应用", en: "Apps" },
  { id: "network" as const, icon: NetworkIcon, zh: "网络", en: "Network" },
  { id: "activity" as const, icon: HistoryIcon, zh: "活动", en: "Activity" }
];

const ActivityView = lazy(() => import("./views/ActivityView").then((module) => ({ default: module.ActivityView })));
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
  const [data, setData] = useState<AppData | null>(null);
  const [loadedScreens, setLoadedScreens] = useState<Set<Screen>>(() => new Set());
  const [loadingScreen, setLoadingScreen] = useState<Screen | null>(null);
  const [setupStatus, setSetupStatus] = useState<SetupStatus | null>(null);
  const [addFirstNode, setAddFirstNode] = useState(false);
  const [notice, setNotice] = useState<{ message: string; detail?: string; error?: boolean } | null>(null);

  const setLanguage = (next: Language) => {
    window.localStorage.setItem("vastora.language", next);
    setLanguageState(next);
  };

  const loadScreen = useCallback(async (target: Screen) => {
    setLoadingScreen(target);
    try {
      const patch = await loadScreenData(target);
      setData((current) => ({ ...(current ?? emptyAppData(patch.status)), ...patch }));
      setLoadedScreens((current) => {
        if (current.has(target)) return current;
        return new Set(current).add(target);
      });
    } finally {
      setLoadingScreen((current) => current === target ? null : current);
    }
  }, []);

  const handleLoadError = useCallback((error: unknown) => {
    if (error instanceof APIError && error.status === 401) {
      setPhase("login");
      return;
    }
    setNotice({ message: userError(language, error), detail: error instanceof Error ? error.message : undefined, error: true });
  }, [language]);

  const navigate = useCallback((target: Screen, replace = false) => {
    const path = pathForScreen(target);
    if (window.location.pathname !== path) {
      window.history[replace ? "replaceState" : "pushState"]({}, "", path);
    }
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
  useEffect(() => { document.documentElement.lang = language; }, [language]);
  useEffect(() => {
    const onPopState = () => {
      const target = screenFromPath();
      setScreen(target);
      if (phase === "ready") void loadScreen(target).catch(handleLoadError);
    };
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, [phase, handleLoadError, loadScreen]);
  useEffect(() => {
    const label = navigation.find((item) => item.id === screen);
    document.title = `${label ? copy(language, label.zh, label.en) : copy(language, "设置", "Settings")} · Vastora`;
  }, [language, screen]);
  useEffect(() => {
    if (phase !== "ready") return;
    let cancelled = false;
    let timer = 0;
    const poll = async () => {
      if (document.visibilityState === "visible") {
        try { await loadScreen(screen); } catch (error) {
          if (error instanceof APIError && error.status === 401 && !cancelled) setPhase("login");
        }
      }
      if (!cancelled) timer = window.setTimeout(() => void poll(), screen === "home" || screen === "settings" ? 30000 : 5000);
    };
    timer = window.setTimeout(() => void poll(), screen === "home" || screen === "settings" ? 30000 : 5000);
    return () => { cancelled = true; window.clearTimeout(timer); };
  }, [phase, screen, loadScreen]);

  const mutate = useCallback<Mutate>(async (operation, success) => {
    try {
      await operation();
    } catch (error) {
      if (error instanceof APIError && error.status === 401) {
        setPhase("login");
        return;
      }
      setNotice({ message: userError(language, error), detail: error instanceof Error ? error.message : undefined, error: true });
      throw error;
    }
    setNotice(success ? { message: success } : null);
    try {
      await loadScreen(screen);
    } catch (error) {
      if (error instanceof APIError && error.status === 401) {
        setPhase("login");
      } else {
        const refreshMessage = copy(language, "操作已完成，但页面状态刷新失败；系统会自动重试。", "The operation completed, but the page could not refresh; it will retry automatically.");
        setNotice({ message: success ? `${success} ${refreshMessage}` : refreshMessage });
      }
    }
  }, [language, screen, loadScreen]);

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
    cloudflareOAuthAvailable={setupStatus?.cloudflareOAuthAvailable ?? false}
    cloudflareZone={setupStatus?.cloudflareZone}
    language={language}
    onLanguage={setLanguage}
    publicAddressCandidates={setupStatus?.publicAddressCandidates ?? []}
    suggestedAgentConnectUrl={setupStatus?.suggestedAgentConnectUrl ?? ""}
    onComplete={async (input) => {
      await api.completeSetup(input);
      setSetupStatus(await api.setupStatus());
      try {
        window.history.replaceState({}, "", pathForScreen("nodes"));
        setScreen("nodes");
        await loadScreen("nodes");
        setAddFirstNode(true);
        setPhase("ready");
      } catch (error) {
        setNotice({ message: userError(language, error), detail: error instanceof Error ? error.message : undefined, error: true });
        setPhase("unavailable");
      }
    }}
  /></Suspense>;
  if (phase === "login") return <CredentialPage language={language} mode="login" onLanguage={setLanguage} onSubmit={async (username, password) => { await api.login(username, password); const setup = await api.setupStatus(); setSetupStatus(setup); if (!setup.onboardingComplete) { setPhase("setup-wizard"); return; } const target = screenFromPath(); await loadScreen(target); setScreen(target); setPhase("ready"); }} />;
  if (!data) return <CenteredState language={language} onRetry={initialize} />;

  const currentLabel = navigation.find((item) => item.id === screen);
  return (
    <TooltipProvider>
      <a className="fixed left-4 top-4 z-50 -translate-y-24 rounded-lg bg-background px-3 py-2 text-sm font-medium shadow-lg transition-transform focus:translate-y-0" href="#main-content">{copy(language, "跳到主要内容", "Skip to main content")}</a>
      <SidebarProvider>
        <Sidebar collapsible="icon">
          <SidebarHeader className="px-3 pb-3 pt-5">
            <Brand />
            <div className="mt-3 flex items-center gap-2 px-2 text-xs text-muted-foreground"><span className="size-2 rounded-full bg-success" aria-hidden="true" />{copy(language, "Center 连接正常", "Center connected")}</div>
          </SidebarHeader>
          <SidebarContent>
            <SidebarGroup>
              <SidebarGroupContent>
                <SidebarMenu>
                  {navigation.map((item) => {
                    return <SidebarMenuItem key={item.id}><NavigationButton active={screen === item.id} icon={item.icon} label={copy(language, item.zh, item.en)} onSelect={() => navigate(item.id)} /></SidebarMenuItem>;
                  })}
                </SidebarMenu>
              </SidebarGroupContent>
            </SidebarGroup>
          </SidebarContent>
          <SidebarFooter className="border-t border-sidebar-border p-3">
            <SidebarMenu><SidebarMenuItem><NavigationButton active={screen === "settings"} icon={SettingsIcon} label={copy(language, "设置", "Settings")} onSelect={() => navigate("settings")} /></SidebarMenuItem></SidebarMenu>
          </SidebarFooter>
        </Sidebar>
        <SidebarInset>
          <header className="flex h-14 items-center gap-3 border-b border-border/70 px-4 md:px-7">
            <SidebarTrigger />
            <span className="text-sm font-medium text-muted-foreground">{currentLabel ? copy(language, currentLabel.zh, currentLabel.en) : copy(language, "设置", "Settings")}</span>
            <div className="flex-1" />
            {loadingScreen === screen ? <span aria-live="polite" className="flex items-center gap-2 text-xs text-muted-foreground"><Spinner />{copy(language, "正在更新", "Updating")}</span> : null}
            <NativeSelect aria-label={copy(language, "界面语言", "Interface language")} onChange={(event) => setLanguage(event.target.value as Language)} size="sm" value={language}><option value="zh-CN">简体中文</option><option value="en">English</option></NativeSelect>
          </header>
          <div className="mx-auto flex w-full max-w-6xl flex-1 flex-col gap-6 px-4 py-7 md:px-8 md:py-10" id="main-content" tabIndex={-1}>
            {notice ? <Alert aria-live="polite" variant={notice.error ? "destructive" : "default"}>{notice.error ? <CircleAlertIcon /> : <CircleCheckIcon />}<AlertTitle>{notice.message}</AlertTitle>{notice.detail && notice.detail !== notice.message ? <AlertDescription><details><summary className="cursor-pointer">{copy(language, "查看技术详情", "Technical details")}</summary><code className="mt-2 block break-all text-xs">{notice.detail}</code></details></AlertDescription> : null}</Alert> : null}
            <Suspense fallback={<ScreenLoading language={language} />}>
              {!loadedScreens.has(screen) ? <ScreenLoading language={language} /> : null}
              {loadedScreens.has(screen) && screen === "home" ? <HomeView data={data} language={language} onNavigate={navigate} mutate={mutate} /> : null}
              {loadedScreens.has(screen) && screen === "nodes" ? <NodesView data={data} language={language} mutate={mutate} onAddFirstNodeHandled={() => setAddFirstNode(false)} onNavigate={navigate} startAdding={addFirstNode} /> : null}
              {loadedScreens.has(screen) && screen === "apps" ? <AppsView data={data} language={language} mutate={mutate} /> : null}
              {loadedScreens.has(screen) && screen === "network" ? <NetworkView data={data} language={language} mutate={mutate} /> : null}
              {loadedScreens.has(screen) && screen === "activity" ? <ActivityView actions={data.actions} agents={data.agents} language={language} /> : null}
              {loadedScreens.has(screen) && screen === "settings" ? <SettingsView data={data} language={language} mutate={mutate} onLogout={async () => { await api.logout(); setData(null); setLoadedScreens(new Set()); setPhase("login"); }} /> : null}
            </Suspense>
          </div>
        </SidebarInset>
      </SidebarProvider>
    </TooltipProvider>
  );
}

function NavigationButton({ active, icon: Icon, label, onSelect }: { active: boolean; icon: LucideIcon; label: string; onSelect: () => void }) {
  const { isMobile, setOpenMobile } = useSidebar();
  return <SidebarMenuButton className="min-h-11 rounded-xl px-3" isActive={active} onClick={() => { onSelect(); if (isMobile) setOpenMobile(false); }} tooltip={label}><Icon /><span>{label}</span></SidebarMenuButton>;
}

function CredentialPage({ language, mode, onLanguage, onSubmit }: { language: Language; mode: "setup" | "login"; onLanguage: (language: Language) => void; onSubmit: (username: string, password: string) => Promise<void> }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault(); setBusy(true); setError("");
    try { await onSubmit(username, password); } catch (submitError) { setError(userError(language, submitError)); } finally { setBusy(false); }
  };
  return (
    <main className="grid min-h-svh place-items-center bg-muted/35 p-5">
      <div className="flex w-full max-w-sm flex-col gap-5">
        <div className="flex items-center justify-between"><Brand /><Button aria-label={copy(language, "切换语言", "Change language")} onClick={() => onLanguage(language === "zh-CN" ? "en" : "zh-CN")} size="icon" variant="ghost"><LanguagesIcon /></Button></div>
        <Card>
          <CardHeader><CardTitle>{mode === "setup" ? copy(language, "创建管理员", "Create administrator") : copy(language, "登录 Center", "Sign in to Center")}</CardTitle><CardDescription>{mode === "setup" ? copy(language, "先保护 Center，下一步再配置位置和网络。不需要 bootstrap token。", "Secure Center first, then configure its location and network. No bootstrap token is required.") : copy(language, "使用管理员账号继续。", "Continue with your administrator account.")}</CardDescription></CardHeader>
          <CardContent>
            <form onSubmit={(event) => void submit(event)}><FieldGroup><Field data-invalid={Boolean(error)}><FieldLabel htmlFor="username">{copy(language, "账号", "Username")}</FieldLabel><Input aria-invalid={Boolean(error)} autoComplete="username" id="username" minLength={3} onChange={(event) => setUsername(event.target.value)} required value={username} /></Field><Field data-invalid={Boolean(error)}><FieldLabel htmlFor="password">{copy(language, "密码", "Password")}</FieldLabel><Input aria-invalid={Boolean(error)} autoComplete={mode === "setup" ? "new-password" : "current-password"} id="password" minLength={12} onChange={(event) => setPassword(event.target.value)} required type="password" value={password} />{error ? <FieldError>{error}</FieldError> : null}</Field><Button disabled={busy} size="lg" type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{mode === "setup" ? copy(language, "创建并继续", "Create and continue") : copy(language, "登录", "Sign in")}</Button></FieldGroup></form>
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
  return <Button onClick={() => void onLogout()} variant="outline"><LogOutIcon data-icon="inline-start" />{copy(language, "退出登录", "Sign out")}</Button>;
}
