import { useCallback, useEffect, useState, type ComponentType, type FormEvent, type ReactNode } from "react";
import { BoxesIcon, CircleCheckIcon, LayoutDashboardIcon, LogOutIcon, NetworkIcon, PackageOpenIcon, PlusIcon, RefreshCwIcon, RocketIcon, SettingsIcon, ShieldCheckIcon } from "lucide-react";
import { APIError, api } from "./api";
import { text, type Language, type MessageKey } from "./translations";
import type { AppView, CatalogSource, DashboardStatus } from "./types";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Sidebar, SidebarContent, SidebarFooter, SidebarGroup, SidebarGroupContent, SidebarGroupLabel, SidebarHeader, SidebarInset, SidebarMenu, SidebarMenuButton, SidebarMenuItem, SidebarProvider, SidebarTrigger } from "@/components/ui/sidebar";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { TooltipProvider } from "@/components/ui/tooltip";

type Screen = "overview" | "catalog" | "nodes" | "deployments" | "settings";
type Phase = "loading" | "setup" | "login" | "ready" | "unavailable";
type Icon = ComponentType<{ className?: string }>;
type DashboardData = { status: DashboardStatus; sources: CatalogSource[]; apps: AppView[] };

const navigation: Array<{ id: Screen; label: MessageKey; icon: Icon }> = [
  { id: "overview", label: "overview", icon: LayoutDashboardIcon },
  { id: "catalog", label: "catalog", icon: PackageOpenIcon },
  { id: "nodes", label: "nodes", icon: NetworkIcon },
  { id: "deployments", label: "deployments", icon: RocketIcon },
  { id: "settings", label: "settings", icon: SettingsIcon }
];

export function App() {
  const [language, setLanguage] = useState<Language>("en");
  const [phase, setPhase] = useState<Phase>("loading");
  const [screen, setScreen] = useState<Screen>("overview");
  const [data, setData] = useState<DashboardData | null>(null);
  const [notice, setNotice] = useState("");
  const [noticeError, setNoticeError] = useState(false);
  const t = useCallback((key: MessageKey) => text(language, key), [language]);

  const loadDashboard = useCallback(async () => {
    const [status, sources, apps] = await Promise.all([api.status(), api.sources(), api.apps()]);
    setData({ status, sources: sources.sources, apps: apps.apps });
  }, []);

  const initialize = useCallback(async () => {
    setPhase("loading");
    setNotice("");
    try {
      const setup = await api.setupStatus();
      if (!setup.configured) return setPhase("setup");
      try {
        await loadDashboard();
        setPhase("ready");
      } catch (error) {
        if (error instanceof APIError && error.status === 401) return setPhase("login");
        throw error;
      }
    } catch (error) {
      setPhase("unavailable");
      setNotice(error instanceof Error ? error.message : "The control plane is unavailable.");
    }
  }, [loadDashboard]);

  useEffect(() => { void initialize(); }, [initialize]);

  const onSetup = async (bootstrapToken: string, password: string) => {
    await api.setupAdmin(bootstrapToken, password);
    await loadDashboard();
    setPhase("ready");
  };
  const onLogin = async (password: string) => {
    await api.login(password);
    await loadDashboard();
    setPhase("ready");
  };
  const onCreateSource = async (source: SourceFormValue) => {
    await api.createSource(source);
    await api.refreshSource(source.id);
    await loadDashboard();
    setScreen("catalog");
    setNoticeError(false);
    setNotice(t("sourceSaved"));
  };
  const onRefreshSource = async (id: string) => {
    try {
      await api.refreshSource(id);
      await loadDashboard();
      setNoticeError(false);
      setNotice(t("refreshSucceeded"));
    } catch (error) {
      if (error instanceof APIError && error.status === 401) {
        setPhase("login");
        return;
      }
      setNoticeError(true);
      setNotice(error instanceof Error ? error.message : "Request failed");
    }
  };
  const onLogout = async () => {
    await api.logout();
    setData(null);
    setPhase("login");
    setNotice("");
  };

  if (phase === "loading") return <CenteredMessage message={t("loading")} />;
  if (phase === "unavailable") return <CenteredMessage message={notice || t("unavailable")} retry={initialize} />;
  if (phase === "setup") return <CredentialPage language={language} setLanguage={setLanguage} mode="setup" onSubmit={({ bootstrapToken, password }) => onSetup(bootstrapToken, password)} />;
  if (phase === "login") return <CredentialPage language={language} setLanguage={setLanguage} mode="login" onSubmit={({ password }) => onLogin(password)} />;
  if (!data) return <CenteredMessage message={t("unavailable")} retry={initialize} />;

  return (
    <TooltipProvider>
      <SidebarProvider>
        <Sidebar collapsible="icon">
          <SidebarHeader className="border-b border-sidebar-border px-3 py-4"><Brand /></SidebarHeader>
          <SidebarContent>
            <SidebarGroup>
              <SidebarGroupLabel>CONTROL PLANE</SidebarGroupLabel>
              <SidebarGroupContent><SidebarMenu>{navigation.map((item) => {
                const IconComponent = item.icon;
                return <SidebarMenuItem key={item.id}><SidebarMenuButton isActive={screen === item.id} onClick={() => setScreen(item.id)} tooltip={t(item.label)}><IconComponent /><span>{t(item.label)}</span></SidebarMenuButton></SidebarMenuItem>;
              })}</SidebarMenu></SidebarGroupContent>
            </SidebarGroup>
          </SidebarContent>
          <SidebarFooter className="border-t border-sidebar-border p-3"><p className="px-2 text-xs text-muted-foreground">Vastora control plane</p></SidebarFooter>
        </Sidebar>
        <SidebarInset>
          <header className="flex h-14 items-center gap-3 border-b border-border px-4 md:px-7">
            <SidebarTrigger />
            <div className="flex-1" />
            <LanguageSelect language={language} setLanguage={setLanguage} />
            <Button variant="outline" size="sm" onClick={() => void onLogout()}><LogOutIcon /><span className="hidden sm:inline">{t("signOut")}</span></Button>
          </header>
          <section className="mx-auto flex w-full max-w-6xl flex-1 flex-col gap-7 px-4 py-7 md:px-7" aria-live="polite">
            {notice ? <Notice error={noticeError} message={notice} /> : null}
            {screen === "overview" ? <Overview status={data.status} t={t} /> : null}
            {screen === "catalog" ? <CatalogView language={language} sources={data.sources} apps={data.apps} t={t} onRefresh={onRefreshSource} onCreateSource={onCreateSource} /> : null}
            {screen === "nodes" ? <EmptyState title={t("nodes")} message={t("noNodes")} icon={NetworkIcon} /> : null}
            {screen === "deployments" ? <EmptyState title={t("deployments")} message={t("noDeployments")} icon={RocketIcon} /> : null}
            {screen === "settings" ? <SettingsView t={t} /> : null}
          </section>
        </SidebarInset>
      </SidebarProvider>
    </TooltipProvider>
  );
}

function CredentialPage({ language, setLanguage, mode, onSubmit }: { language: Language; setLanguage: (language: Language) => void; mode: "setup" | "login"; onSubmit: (input: { bootstrapToken: string; password: string }) => Promise<void> }) {
  const [bootstrapToken, setBootstrapToken] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const t = (key: MessageKey) => text(language, key);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try { await onSubmit({ bootstrapToken, password }); }
    catch (submissionError) { setError(submissionError instanceof Error ? submissionError.message : "Request failed"); }
    finally { setBusy(false); }
  };
  return (
    <div className="min-h-svh bg-background p-4 sm:p-8">
      <header className="mx-auto flex max-w-6xl items-center justify-between border-b border-border pb-4"><Brand /><LanguageSelect language={language} setLanguage={setLanguage} /></header>
      <main className="mx-auto grid min-h-[calc(100svh-8rem)] max-w-md place-items-center py-10">
        <form className="w-full space-y-6" onSubmit={(event) => void submit(event)}>
          <div className="space-y-2"><p className="font-mono text-xs tracking-[0.16em] text-primary">CONTROL PLANE</p><h1 className="text-3xl font-semibold tracking-tight">{mode === "setup" ? t("setupTitle") : t("loginTitle")}</h1>{mode === "setup" ? <p className="text-sm leading-6 text-muted-foreground">{t("setupDescription")}</p> : null}</div>
          <FieldGroup>
            {mode === "setup" ? <Field><FieldLabel htmlFor="bootstrap-token">{t("bootstrapToken")}</FieldLabel><Input id="bootstrap-token" autoComplete="off" onChange={(event) => setBootstrapToken(event.target.value)} required type="password" value={bootstrapToken} /></Field> : null}
            <Field><FieldLabel htmlFor="administrator-password">{t("password")}</FieldLabel><Input id="administrator-password" autoComplete={mode === "setup" ? "new-password" : "current-password"} minLength={12} onChange={(event) => setPassword(event.target.value)} required type="password" value={password} /></Field>
            {error ? <FieldError>{error}</FieldError> : null}
            <Button className="w-full" disabled={busy} type="submit">{busy ? <Spinner /> : null}{mode === "setup" ? t("completeSetup") : t("signIn")}</Button>
          </FieldGroup>
        </form>
      </main>
    </div>
  );
}

function Overview({ status, t }: { status: DashboardStatus; t: (key: MessageKey) => string }) {
  const rows: Array<[MessageKey, string | number]> = [["version", status.version], ["catalogSources", status.catalogSources], ["catalogApps", status.catalogApps], ["enrolledNodes", status.nodes], ["activeDeployments", status.deployments]];
  return <section className="space-y-5"><PageHeading eyebrow="CONTROL PLANE" title={t("systemOverview")} /><div className="border-y border-border">{rows.map(([label, value]) => <div className="grid grid-cols-[1fr_auto] gap-4 border-b border-border px-1 py-4 last:border-0 sm:px-3" key={label}><span className="text-sm text-muted-foreground">{t(label)}</span><strong className="font-mono text-sm font-medium tabular-nums">{value}</strong></div>)}</div></section>;
}

function CatalogView({ language, sources, apps, t, onRefresh, onCreateSource }: { language: Language; sources: CatalogSource[]; apps: AppView[]; t: (key: MessageKey) => string; onRefresh: (id: string) => Promise<void>; onCreateSource: (source: SourceFormValue) => Promise<void> }) {
  const [showForm, setShowForm] = useState(false);
  return <section className="space-y-9"><div className="flex flex-wrap items-end justify-between gap-4"><PageHeading eyebrow="TRUSTED INPUTS" title={t("sources")} /><Button onClick={() => setShowForm(true)}><PlusIcon />{t("addSource")}</Button></div>{showForm ? <SourceForm t={t} onCancel={() => setShowForm(false)} onSave={async (value) => { await onCreateSource(value); setShowForm(false); }} /> : null}<CatalogSourceTable language={language} sources={sources} t={t} onRefresh={onRefresh} /><div className="space-y-4"><PageHeading eyebrow="VERIFIED CONTENT" title={t("catalog")} /><AppTable apps={apps} language={language} t={t} /></div></section>;
}

function CatalogSourceTable({ language, sources, t, onRefresh }: { language: Language; sources: CatalogSource[]; t: (key: MessageKey) => string; onRefresh: (id: string) => Promise<void> }) {
  const [refreshingID, setRefreshingID] = useState<string | null>(null);
  const refresh = async (id: string) => { setRefreshingID(id); try { await onRefresh(id); } finally { setRefreshingID(null); } };
  return <Table><TableHeader><TableRow><TableHead>{t("source")}</TableHead><TableHead>{t("status")}</TableHead><TableHead>{t("signature")}</TableHead><TableHead>{t("fetched")}</TableHead><TableHead><span className="sr-only">{t("refresh")}</span></TableHead></TableRow></TableHeader><TableBody>{sources.length === 0 ? <EmptyTableRow columns={5} message={t("noSources")} /> : sources.map((source) => <TableRow key={source.id}><TableCell><p className="font-medium">{source.displayName}</p><p className="font-mono text-xs text-muted-foreground">{source.id}</p></TableCell><TableCell><StatusText source={source} t={t} /></TableCell><TableCell>Ed25519</TableCell><TableCell className="text-muted-foreground">{source.fetchedAt ? new Intl.DateTimeFormat(language, { dateStyle: "medium", timeStyle: "short" }).format(new Date(source.fetchedAt)) : "—"}</TableCell><TableCell className="text-right"><Button variant="outline" size="sm" disabled={refreshingID === source.id} onClick={() => void refresh(source.id)}>{refreshingID === source.id ? <Spinner /> : <RefreshCwIcon />}{t("refresh")}</Button></TableCell></TableRow>)}</TableBody></Table>;
}

function AppTable({ language, apps, t }: { language: Language; apps: AppView[]; t: (key: MessageKey) => string }) {
  return <Table><TableHeader><TableRow><TableHead>{t("app")}</TableHead><TableHead>{t("source")}</TableHead><TableHead>{t("versionColumn")}</TableHead><TableHead>{t("description")}</TableHead></TableRow></TableHeader><TableBody>{apps.length === 0 ? <EmptyTableRow columns={4} message={t("noApps")} /> : apps.map((entry) => <TableRow key={entry.key}><TableCell><p className="font-medium">{entry.app.name[language]}</p><p className="font-mono text-xs text-muted-foreground">{entry.key}</p></TableCell><TableCell>{entry.sourceId}</TableCell><TableCell className="font-mono">{entry.app.version}</TableCell><TableCell className="whitespace-normal text-muted-foreground">{entry.app.description[language]}</TableCell></TableRow>)}</TableBody></Table>;
}

function EmptyTableRow({ columns, message }: { columns: number; message: string }) { return <TableRow><TableCell className="py-10 text-center text-muted-foreground" colSpan={columns}>{message}</TableCell></TableRow>; }

type SourceFormValue = { id: string; displayName: string; url: string; publicKey: string; bearerToken: string; customCA: string; refreshIntervalSeconds: number };

function SourceForm({ t, onCancel, onSave }: { t: (key: MessageKey) => string; onCancel: () => void; onSave: (value: SourceFormValue) => Promise<void> }) {
  const [value, setValue] = useState<SourceFormValue>({ id: "", displayName: "", url: "", publicKey: "", bearerToken: "", customCA: "", refreshIntervalSeconds: 21600 });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const update = <K extends keyof SourceFormValue>(key: K, next: SourceFormValue[K]) => setValue((current) => ({ ...current, [key]: next }));
  const submit = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); setBusy(true); setError(""); try { await onSave(value); } catch (submissionError) { setError(submissionError instanceof Error ? submissionError.message : "Request failed"); } finally { setBusy(false); } };
  return <form className="border-y border-border py-6" onSubmit={(event) => void submit(event)}><FieldGroup className="md:grid md:grid-cols-2 md:gap-x-6"><FormField id="source-id" label={t("sourceId")}><Input id="source-id" onChange={(event) => update("id", event.target.value)} pattern="[a-z][a-z0-9-]{1,62}" required value={value.id} /></FormField><FormField id="display-name" label={t("displayName")}><Input id="display-name" onChange={(event) => update("displayName", event.target.value)} required value={value.displayName} /></FormField><FormField id="catalog-url" label={t("catalogURL")}><Input id="catalog-url" onChange={(event) => update("url", event.target.value)} placeholder="https://catalog.example.invalid/v1.json" required type="url" value={value.url} /></FormField><FormField id="public-key" label={t("publicKey")}><Input id="public-key" onChange={(event) => update("publicKey", event.target.value)} required value={value.publicKey} /></FormField><FormField id="bearer-token" label={t("bearerToken")}><Input id="bearer-token" autoComplete="off" onChange={(event) => update("bearerToken", event.target.value)} type="password" value={value.bearerToken} /><FieldDescription>{t("privateTokenStored")}</FieldDescription></FormField><FormField id="refresh-interval" label={t("refreshInterval")}><Input id="refresh-interval" min={300} max={604800} onChange={(event) => update("refreshIntervalSeconds", Number(event.target.value))} required type="number" value={value.refreshIntervalSeconds} /></FormField><Field className="md:col-span-2"><FieldLabel htmlFor="custom-ca">{t("customCA")}</FieldLabel><Textarea id="custom-ca" onChange={(event) => update("customCA", event.target.value)} value={value.customCA} /></Field>{error ? <FieldError className="md:col-span-2">{error}</FieldError> : null}<div className="flex items-center justify-end gap-2 md:col-span-2"><Button variant="outline" onClick={onCancel} type="button">{t("cancel")}</Button><Button disabled={busy} type="submit">{busy ? <Spinner /> : null}{t("saveSource")}</Button></div></FieldGroup></form>;
}

function FormField({ id, label, children }: { id: string; label: string; children: ReactNode }) { return <Field><FieldLabel htmlFor={id}>{label}</FieldLabel>{children}</Field>; }
function SettingsView({ t }: { t: (key: MessageKey) => string }) { return <section className="space-y-5"><PageHeading eyebrow="SECURITY" title={t("settings")} /><Alert><ShieldCheckIcon /><AlertTitle>{t("privateTokenStored")}</AlertTitle><AlertDescription>Catalog bearer credentials can only be changed by replacing them; their values are never returned by the API.</AlertDescription></Alert></section>; }
function StatusText({ source, t }: { source: CatalogSource; t: (key: MessageKey) => string }) { if (source.lastError) return <span className="text-destructive">{source.lastError}</span>; return <span className="inline-flex items-center gap-1.5 text-sm text-primary"><CircleCheckIcon className="size-4" />{source.enabled ? t("enabled") : t("configured")}</span>; }
function EmptyState({ title, message, icon: IconComponent }: { title: string; message: string; icon: Icon }) { return <section className="space-y-5"><PageHeading eyebrow="CONTROL PLANE" title={title} /><Empty className="min-h-64 border border-dashed border-border"><EmptyHeader><EmptyMedia variant="icon"><IconComponent /></EmptyMedia><EmptyTitle>{title}</EmptyTitle><EmptyDescription>{message}</EmptyDescription></EmptyHeader></Empty></section>; }
function Notice({ error, message }: { error: boolean; message: string }) { return <Alert variant={error ? "destructive" : "default"}>{error ? <ShieldCheckIcon /> : <CircleCheckIcon />}<AlertTitle>{message}</AlertTitle></Alert>; }
function CenteredMessage({ message, retry }: { message: string; retry?: () => Promise<void> }) { return <main className="grid min-h-svh place-items-center bg-background p-6"><div className="w-full max-w-sm space-y-5"><Brand /><Alert><BoxesIcon /><AlertTitle>{message}</AlertTitle>{retry ? <div className="mt-4"><Button onClick={() => void retry()}>Retry</Button></div> : null}</Alert></div></main>; }
function PageHeading({ eyebrow, title }: { eyebrow: string; title: string }) { return <div className="space-y-1"><p className="font-mono text-xs tracking-[0.16em] text-primary">{eyebrow}</p><h1 className="text-2xl font-semibold tracking-tight">{title}</h1></div>; }
function Brand() { return <div className="flex items-center gap-2.5 font-semibold tracking-tight"><span className="grid size-7 place-items-center rounded-md bg-primary text-primary-foreground" aria-hidden="true"><svg className="size-4" viewBox="0 0 24 24"><path fill="currentColor" d="M12 2.5 20 7v10l-8 4.5L4 17V7l8-4.5Zm0 3.1L7.1 8.4v7.2l4.9 2.8 4.9-2.8V8.4L12 5.6Zm-3 4.1 3 1.7 3-1.7v3.5L12 15l-3-1.8V9.7Z" /></svg></span><span>Petauron Vastora</span></div>; }
function LanguageSelect({ language, setLanguage }: { language: Language; setLanguage: (language: Language) => void }) { return <NativeSelect aria-label="Language" size="sm" value={language} onChange={(event) => setLanguage(event.target.value as Language)}><option value="en">English</option><option value="zh-CN">简体中文</option></NativeSelect>; }
