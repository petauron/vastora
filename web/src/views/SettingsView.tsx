import { useEffect, useRef, useState, type FormEvent } from "react";
import { BotIcon, ChevronDownIcon, DatabaseBackupIcon, DatabaseIcon, DownloadIcon, KeyRoundIcon, PencilIcon, PlusIcon, PowerIcon, RefreshCwIcon, SettingsIcon, ShieldCheckIcon, Trash2Icon } from "lucide-react";
import { api } from "../api";
import { SignOutButton, type AppData, type Mutate } from "../App";
import { administratorPasswordMinLength } from "../lib/security";
import type { AssistantProvider, CatalogSource, CenterUpdateStatus } from "../types";
import type { Language } from "../translations";
import { PageHeading, StateBadge, TechnicalError, copy, formatDate, userError } from "./shared";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { CenterUpdateCard } from "./CenterUpdateCard";
import { SystemDomainSettings } from "./SystemDomainSettings";

export function SettingsView({ data, language, mutate, onCenterUpdateStatus, onLogout, onRefresh }: { data: AppData; language: Language; mutate: Mutate; onCenterUpdateStatus: (status: CenterUpdateStatus) => void; onLogout: () => Promise<void>; onRefresh: () => Promise<void> }) {
  const [adding, setAdding] = useState(false);
  const [backupOpen, setBackupOpen] = useState(false);
  const [passwordOpen, setPasswordOpen] = useState(false);
  const [diagnosticsBusy, setDiagnosticsBusy] = useState(false);
  const [diagnosticsError, setDiagnosticsError] = useState("");
  const downloadDiagnostics = async () => { setDiagnosticsBusy(true); setDiagnosticsError(""); try { await api.downloadDiagnostics(); } catch (error) { setDiagnosticsError(userError(language, error)); } finally { setDiagnosticsBusy(false); } };
  return (
    <section className="flex flex-col gap-7">
      <PageHeading title={copy(language, "设置", "Settings")} description={copy(language, "管理 Center、数据保护、应用目录和登录会话。网络集成位于“网络”页面。", "Manage Center, data protection, app catalogs, and your session. Network integrations live on the Network page.")} action={<SignOutButton language={language} onLogout={onLogout} />} />
      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><SettingsIcon />Center</CardTitle><CardDescription>{copy(language, "当前运行信息", "Current runtime information")}</CardDescription></CardHeader>
        <CardContent><dl className="grid gap-4 text-sm sm:grid-cols-3"><div><dt className="text-muted-foreground">{copy(language, "版本", "Version")}</dt><dd className="mt-1 font-medium">{data.centerUpdate.currentVersion}</dd></div><div><dt className="text-muted-foreground">{copy(language, "节点", "Nodes")}</dt><dd className="mt-1 font-medium">{data.agents.filter((agent) => agent.status === "active").length}</dd></div><div><dt className="text-muted-foreground">{copy(language, "应用", "Apps")}</dt><dd className="mt-1 font-medium">{data.applications.length}</dd></div></dl></CardContent>
        <CardFooter className="justify-end"><Button onClick={() => setPasswordOpen(true)} size="sm" variant="outline"><KeyRoundIcon data-icon="inline-start" />{copy(language, "修改管理员密码", "Change administrator password")}</Button></CardFooter>
      </Card>
      <SystemDomainSettings domain={data.systemDomain} language={language} />
      <CenterUpdateCard language={language} onRefresh={onRefresh} onStatusChange={onCenterUpdateStatus} status={data.centerUpdate} />
      <AssistantProviderSettings language={language} />
      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><ShieldCheckIcon />{copy(language, "数据与故障排查", "Data & troubleshooting")}</CardTitle><CardDescription>{copy(language, "备份 Center 配置，或下载不含密钥的诊断信息。", "Back up Center configuration or download diagnostics that contain no secret values.")}</CardDescription></CardHeader>
        <CardContent>
          <div className="grid gap-3 sm:grid-cols-2"><Button className="h-auto justify-start py-3" onClick={() => setBackupOpen(true)} variant="outline"><DatabaseBackupIcon data-icon="inline-start" /><span className="text-left"><span className="block">{copy(language, "下载加密备份", "Download encrypted backup")}</span><span className="block text-xs font-normal text-muted-foreground">{copy(language, "包含数据库和密钥，需要密码恢复", "Database and keys; password required to restore")}</span></span></Button><Button className="h-auto justify-start py-3" disabled={diagnosticsBusy} onClick={() => void downloadDiagnostics()} variant="outline">{diagnosticsBusy ? <Spinner data-icon="inline-start" /> : <DownloadIcon data-icon="inline-start" />}<span className="text-left"><span className="block">{copy(language, "下载诊断报告", "Download diagnostics")}</span><span className="block text-xs font-normal text-muted-foreground">{copy(language, "版本、健康状态和最近错误，不含 Token", "Version, health, and recent errors; no tokens")}</span></span></Button></div>
          {diagnosticsError ? <FieldError className="mt-3" role="alert">{diagnosticsError}</FieldError> : null}
          <p className="mt-4 text-xs leading-5 text-muted-foreground">{copy(language, "恢复时先停止 Center，再使用 vastora center restore 将备份恢复到新的空数据目录。", "To restore, stop Center and use vastora center restore with a new empty data directory.")}</p>
        </CardContent>
      </Card>
      <CatalogSettings data={data} language={language} mutate={mutate} onAdd={() => setAdding(true)} />
      {adding ? <SourceSheet language={language} onClose={() => setAdding(false)} onSubmit={async (source) => { await mutate(() => api.createSource(source), copy(language, "应用目录已添加。", "App catalog added.")); setAdding(false); }} open /> : null}
      {backupOpen ? <BackupSheet language={language} onClose={() => setBackupOpen(false)} open /> : null}
      {passwordOpen ? <PasswordSheet language={language} onClose={() => setPasswordOpen(false)} open /> : null}
    </section>
  );
}

function AssistantProviderSettings({ language }: { language: Language }) {
  const [provider, setProvider] = useState<AssistantProvider | null>(null);
  const [apiUrl, setAPIURL] = useState("");
  const [apiKey, setAPIKey] = useState("");
  const [model, setModel] = useState("");
  const [allowPrivate, setAllowPrivate] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    const controller = new AbortController();
    void api.assistantProvider(controller.signal).then((value) => {
      setProvider(value);
      setAPIURL(value.apiUrl || "");
      setModel(value.model || "");
      setAllowPrivate(value.allowPrivate);
    }).catch((loadError) => { if (!controller.signal.aborted) setError(userError(language, loadError)); });
    return () => controller.abort();
  }, [language]);

  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault(); setBusy(true); setError("");
    try {
      const value = await api.saveAssistantProvider({ apiUrl, apiKey, model, allowPrivate });
      setProvider(value); setAPIKey("");
    } catch (saveError) {
      setError(userError(language, saveError));
    } finally {
      setBusy(false);
    }
  };
  const validate = async () => {
    setBusy(true); setError("");
    try { setProvider(await api.validateAssistantProvider()); }
    catch (validateError) { setError(userError(language, validateError)); }
    finally { setBusy(false); }
  };

  const savedProviderSelected = Boolean(provider?.apiKeySet)
    && !apiKey
    && apiUrl.trim() === provider?.apiUrl
    && model.trim() === provider?.model
    && allowPrivate === provider?.allowPrivate;

  return <Card>
    <CardHeader><CardTitle className="flex items-center gap-2"><BotIcon />{copy(language, "集群助手模型", "Cluster assistant model")}</CardTitle><CardDescription>{copy(language, "连接 OpenAI 兼容服务。密钥加密保存且之后只显示是否已设置。", "Connect an OpenAI-compatible provider. The key is encrypted and only its configured state is returned later.")}</CardDescription><CardAction>{provider ? <StateBadge language={language} value={provider.status} /> : <Spinner />}</CardAction></CardHeader>
    <form onSubmit={(event) => void save(event)}>
      <CardContent><FieldGroup><Field><FieldLabel htmlFor="assistant-api-url">API URL</FieldLabel><Input autoCapitalize="none" autoCorrect="off" id="assistant-api-url" onChange={(event) => setAPIURL(event.target.value)} placeholder="https://api.example.com/v1" required spellCheck={false} type="url" value={apiUrl} /><FieldDescription>{copy(language, "必须是无凭据的固定 HTTP(S) 地址；公网服务必须使用 HTTPS。", "Use an exact credential-free HTTP(S) URL. Public providers require HTTPS.")}</FieldDescription></Field><Field><FieldLabel htmlFor="assistant-model">{copy(language, "模型标识", "Model identifier")}</FieldLabel><Input autoCapitalize="none" autoCorrect="off" id="assistant-model" onChange={(event) => setModel(event.target.value)} placeholder="gpt-5.4-mini" required spellCheck={false} value={model} /></Field><Field><FieldLabel htmlFor="assistant-api-key">API Key</FieldLabel><Input autoComplete="new-password" id="assistant-api-key" onChange={(event) => setAPIKey(event.target.value)} placeholder={provider?.apiKeySet ? copy(language, "留空以保留已保存的密钥", "Leave blank to keep the saved key") : copy(language, "输入 API Key", "Enter an API key")} required={!provider?.apiKeySet} type="password" value={apiKey} /><FieldDescription>{provider?.apiKeySet ? copy(language, "已保存密钥；浏览器和诊断报告无法读取。", "A key is stored; browsers and diagnostics cannot read it.") : copy(language, "密钥只发送给 Center，不会进入模型消息或工具参数。", "The key is sent only to Center and never enters model messages or tool arguments.")}</FieldDescription></Field><Field orientation="horizontal"><div className="flex-1"><FieldLabel htmlFor="assistant-private-provider">{copy(language, "信任私有模型地址", "Trust a private model endpoint")}</FieldLabel><FieldDescription>{copy(language, "仅为自己控制的内网或本机模型开启。开启后允许私有 IP 和私有 HTTP。", "Enable only for a private or local model gateway you control. This permits private IPs and private HTTP.")}</FieldDescription></div><Switch checked={allowPrivate} id="assistant-private-provider" onCheckedChange={setAllowPrivate} /></Field>{provider?.lastError ? <TechnicalError error={provider.lastError} language={language} /> : null}{error ? <FieldError role="alert">{error}</FieldError> : null}</FieldGroup></CardContent>
      <CardFooter className="flex-wrap justify-end gap-2"><Button disabled={busy || !savedProviderSelected} onClick={() => void validate()} title={savedProviderSelected ? undefined : copy(language, "先保存当前配置，再测试连接", "Save the current configuration before testing it")} type="button" variant="outline">{busy ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" />}{copy(language, "测试连接", "Test connection")}</Button><Button disabled={busy || !apiUrl.trim() || !model.trim() || !provider?.apiKeySet && !apiKey} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "保存模型配置", "Save model settings")}</Button></CardFooter>
    </form>
  </Card>;
}

function CatalogSettings({ data, language, mutate, onAdd }: { data: AppData; language: Language; mutate: Mutate; onAdd: () => void }) {
  return (
    <details className="group rounded-2xl border bg-card shadow-xs">
      <summary className="flex min-h-16 cursor-pointer list-none items-center gap-3 px-5 py-4">
        <span className="grid size-10 shrink-0 place-items-center rounded-xl bg-muted text-muted-foreground"><DatabaseIcon aria-hidden="true" className="size-5" /></span>
        <span className="min-w-0 flex-1">
          <span className="block font-semibold">{copy(language, "高级：应用目录", "Advanced: app catalogs")}</span>
          <span className="mt-1 block text-sm font-normal text-muted-foreground">{copy(language, "官方目录已预先配置；仅在接入其他可信目录时使用。", "The official catalog is already configured. Use this only for another trusted catalog.")}</span>
        </span>
        <ChevronDownIcon aria-hidden="true" className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-180" />
      </summary>
      <div className="border-t px-5 py-5">
        <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
          <div><h2 className="font-semibold">{copy(language, "已连接目录", "Connected catalogs")}</h2><p className="mt-1 text-sm text-muted-foreground">{copy(language, "目录必须使用固定签名公钥。", "Catalogs must use a pinned signing key.")}</p></div>
          <Button onClick={onAdd} size="sm"><PlusIcon data-icon="inline-start" />{copy(language, "添加目录", "Add catalog")}</Button>
        </div>
        <div className="grid gap-4 lg:grid-cols-2">{data.sources.map((source) => <SourceCard key={source.id} language={language} mutate={mutate} source={source} />)}</div>
      </div>
    </details>
  );
}

function PasswordSheet({ language, onClose, open }: { language: Language; onClose: () => void; open: boolean }) {
  const [currentPassword, setCurrentPassword] = useState(""); const [newPassword, setNewPassword] = useState(""); const [confirmation, setConfirmation] = useState(""); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  const close = () => { setCurrentPassword(""); setNewPassword(""); setConfirmation(""); setError(""); onClose(); };
  const submit = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); setError(""); if (newPassword !== confirmation) { setError(copy(language, "两次输入的新密码不一致。", "The new passwords do not match.")); return; } setBusy(true); try { await api.changePassword(currentPassword, newPassword); close(); } catch (submitError) { setError(userError(language, submitError)); } finally { setBusy(false); } };
  return <Sheet onOpenChange={(next) => { if (!next) close(); }} open={open}><SheetContent><SheetHeader><SheetTitle>{copy(language, "修改管理员密码", "Change administrator password")}</SheetTitle><SheetDescription>{copy(language, "修改后会自动退出其他浏览器中的登录会话。", "Changing it automatically signs out other browser sessions.")}</SheetDescription></SheetHeader><form className="flex flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 px-4"><FieldGroup><Field><FieldLabel htmlFor="current-password">{copy(language, "当前密码", "Current password")}</FieldLabel><Input autoComplete="current-password" id="current-password" onChange={(event) => setCurrentPassword(event.target.value)} required type="password" value={currentPassword} /></Field><Field><FieldLabel htmlFor="new-password">{copy(language, "新密码", "New password")}</FieldLabel><Input autoComplete="new-password" id="new-password" minLength={administratorPasswordMinLength} onChange={(event) => setNewPassword(event.target.value)} required type="password" value={newPassword} /><FieldDescription>{copy(language, "至少 10 个字符。", "At least 10 characters.")}</FieldDescription></Field><Field data-invalid={Boolean(error)}><FieldLabel htmlFor="confirm-new-password">{copy(language, "再次输入新密码", "Enter new password again")}</FieldLabel><Input aria-invalid={Boolean(error)} autoComplete="new-password" id="confirm-new-password" minLength={administratorPasswordMinLength} onChange={(event) => setConfirmation(event.target.value)} required type="password" value={confirmation} />{error ? <FieldError>{error}</FieldError> : null}</Field></FieldGroup></div><SheetFooter><Button onClick={close} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !currentPassword || newPassword.length < administratorPasswordMinLength || newPassword !== confirmation} type="submit">{busy ? <Spinner data-icon="inline-start" /> : <KeyRoundIcon data-icon="inline-start" />}{copy(language, "修改密码", "Change password")}</Button></SheetFooter></form></SheetContent></Sheet>;
}

function BackupSheet({ language, onClose, open }: { language: Language; onClose: () => void; open: boolean }) {
  const [password, setPassword] = useState(""); const [confirmation, setConfirmation] = useState(""); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  const requestEpoch = useRef(0);
  const close = () => { requestEpoch.current += 1; setPassword(""); setConfirmation(""); setBusy(false); setError(""); onClose(); };
  const submit = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); setError(""); if (password !== confirmation) { setError(copy(language, "两次输入的密码不一致。", "The passwords do not match.")); return; } const epoch = requestEpoch.current; setBusy(true); try { await api.downloadBackup(password); if (epoch === requestEpoch.current) close(); } catch (submitError) { if (epoch === requestEpoch.current) setError(userError(language, submitError)); } finally { if (epoch === requestEpoch.current) setBusy(false); } };
  return <Sheet onOpenChange={(next) => { if (!next) close(); }} open={open}><SheetContent><SheetHeader><SheetTitle>{copy(language, "下载加密备份", "Download encrypted backup")}</SheetTitle><SheetDescription>{copy(language, "这个密码不会保存，也无法找回。恢复时必须使用完全相同的密码。", "This password is not stored and cannot be recovered. The exact same password is required to restore.")}</SheetDescription></SheetHeader><form className="flex flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 px-4"><FieldGroup><Field data-invalid={Boolean(error)}><FieldLabel htmlFor="backup-password">{copy(language, "备份密码", "Backup password")}</FieldLabel><Input autoComplete="new-password" id="backup-password" minLength={12} onChange={(event) => setPassword(event.target.value)} required type="password" value={password} /><FieldDescription>{copy(language, "至少 12 个字符；请保存在密码管理器中。", "At least 12 characters. Save it in a password manager.")}</FieldDescription></Field><Field data-invalid={Boolean(error)}><FieldLabel htmlFor="backup-confirmation">{copy(language, "再次输入", "Enter again")}</FieldLabel><Input autoComplete="new-password" id="backup-confirmation" minLength={12} onChange={(event) => setConfirmation(event.target.value)} required type="password" value={confirmation} />{error ? <FieldError>{error}</FieldError> : null}</Field></FieldGroup></div><SheetFooter><Button onClick={close} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || password.length < 12 || password !== confirmation} type="submit">{busy ? <Spinner data-icon="inline-start" /> : <DatabaseBackupIcon data-icon="inline-start" />}{copy(language, "创建并下载", "Create and download")}</Button></SheetFooter></form></SheetContent></Sheet>;
}

function SourceCard({ language, mutate, source }: { language: Language; mutate: Mutate; source: CatalogSource }) {
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState(false);
  const official = source.id === "vastora-official";
  const refresh = async () => { setBusy(true); try { await mutate(() => api.refreshSource(source.id), copy(language, "目录已刷新。", "Catalog refreshed.")); } catch { /* The shared notice already explains the failure. */ } finally { setBusy(false); } };
  const toggle = async () => { setBusy(true); try { await mutate(() => api.updateSource(source.id, { enabled: !source.enabled }), source.enabled ? copy(language, "目录已停用。", "Catalog disabled.") : copy(language, "目录已启用，将按计划刷新。", "Catalog enabled and queued for refresh.")); } catch { /* Shared feedback owns the error. */ } finally { setBusy(false); } };
  const remove = async () => { if (!window.confirm(copy(language, `删除目录“${source.displayName}”？已安装应用仍可卸载，但该目录的应用将不再可安装。`, `Delete “${source.displayName}”? Installed apps remain uninstallable, but apps from this catalog will no longer be available to install.`))) return; setBusy(true); try { await mutate(() => api.deleteSource(source.id), copy(language, "目录已删除。", "Catalog deleted.")); } catch { /* Shared feedback owns the error. */ } finally { setBusy(false); } };
  const statusDescription = source.status === "pending" ? copy(language, "等待首次验证。", "Waiting for first verification.") : source.status === "stale" ? copy(language, "正在继续使用最后一次验证通过的缓存。", "Continuing to use the last verified cache.") : source.status === "failed" ? copy(language, "尚无可用的已验证缓存。", "No verified cache is available yet.") : source.status === "disabled" ? copy(language, "不会自动刷新，也不会提供新的安装。", "Automatic refresh and new installs are paused.") : copy(language, "目录签名与内容已验证。", "Catalog signature and content are verified.");
  return <><Card><CardHeader><CardTitle className="flex min-w-0 items-center gap-2"><DatabaseIcon className="shrink-0" /><span className="truncate" title={source.displayName}>{source.displayName}</span></CardTitle><CardDescription className="truncate" title={source.url}>{source.url}</CardDescription><CardAction><StateBadge language={language} value={source.status} /></CardAction></CardHeader><CardContent><p className="mb-3 text-sm text-muted-foreground">{statusDescription}</p><dl className="grid gap-3 text-sm sm:grid-cols-3"><div><dt className="text-muted-foreground">{copy(language, "上次验证", "Last verified")}</dt><dd className="mt-1">{formatDate(language, source.fetchedAt)}</dd></div><div><dt className="text-muted-foreground">{copy(language, "上次检查", "Last checked")}</dt><dd className="mt-1">{formatDate(language, source.checkedAt)}</dd></div><div><dt className="text-muted-foreground">{copy(language, "刷新间隔", "Refresh interval")}</dt><dd className="mt-1">{Math.round(source.refreshIntervalSeconds / 60)} min</dd></div></dl>{source.lastError ? <div className="mt-3"><TechnicalError error={source.lastError} language={language} /></div> : null}</CardContent><CardFooter className="flex-wrap justify-end gap-2">{!official ? <><Button className="min-h-11" disabled={busy} onClick={() => setEditing(true)} size="sm" variant="outline"><PencilIcon data-icon="inline-start" />{copy(language, "编辑", "Edit")}</Button><Button className="min-h-11" disabled={busy} onClick={() => void toggle()} size="sm" variant="outline"><PowerIcon data-icon="inline-start" />{source.enabled ? copy(language, "停用", "Disable") : copy(language, "启用", "Enable")}</Button><Button className="min-h-11" disabled={busy} onClick={() => void remove()} size="sm" variant="destructive"><Trash2Icon data-icon="inline-start" />{copy(language, "删除", "Delete")}</Button></> : null}<Button className="min-h-11" disabled={busy || !source.enabled} onClick={() => void refresh()} size="sm" variant="outline">{busy ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" />}{copy(language, "刷新", "Refresh")}</Button></CardFooter></Card>{!official && editing ? <SourceEditSheet language={language} mutate={mutate} onClose={() => setEditing(false)} open source={source} /> : null}</>;
}

function SourceEditSheet({ language, mutate, onClose, open, source }: { language: Language; mutate: Mutate; onClose: () => void; open: boolean; source: CatalogSource }) {
  const [displayName, setDisplayName] = useState(source.displayName); const [url, setURL] = useState(source.url); const [publicKey, setPublicKey] = useState(source.publicKey); const [refreshSeconds, setRefreshSeconds] = useState(source.refreshIntervalSeconds); const [bearerToken, setBearerToken] = useState(""); const [customCA, setCustomCA] = useState(""); const [clearBearer, setClearBearer] = useState(false); const [clearCA, setClearCA] = useState(false); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  const close = () => { setDisplayName(source.displayName); setURL(source.url); setPublicKey(source.publicKey); setRefreshSeconds(source.refreshIntervalSeconds); setBearerToken(""); setCustomCA(""); setClearBearer(false); setClearCA(false); setError(""); onClose(); };
  const submit = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); setBusy(true); setError(""); const update: Parameters<typeof api.updateSource>[1] = { displayName, url, publicKey, refreshIntervalSeconds: refreshSeconds }; if (clearBearer) update.bearerToken = ""; else if (bearerToken) update.bearerToken = bearerToken; if (clearCA) update.customCA = ""; else if (customCA) update.customCA = customCA; try { await mutate(() => api.updateSource(source.id, update), copy(language, "目录设置已保存，将重新验证。", "Catalog settings saved and queued for verification.")); close(); } catch (submitError) { setError(userError(language, submitError)); } finally { setBusy(false); } };
  return <Sheet onOpenChange={(next) => { if (!next) close(); }} open={open}><SheetContent className="data-[side=right]:w-[calc(100%-1rem)] data-[side=right]:sm:max-w-lg"><SheetHeader><SheetTitle>{copy(language, "编辑应用目录", "Edit app catalog")}</SheetTitle><SheetDescription>{copy(language, "目录 ID 和历史版本身份不可更改。留空的凭据字段会保留现有值。", "The catalog ID and historical version identities cannot change. Blank credential fields preserve the stored values.")}</SheetDescription></SheetHeader><form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Field><FieldLabel>{copy(language, "目录 ID", "Catalog ID")}</FieldLabel><Input disabled value={source.id} /></Field><Field><FieldLabel htmlFor={`source-name-${source.id}`}>{copy(language, "显示名称", "Display name")}</FieldLabel><Input id={`source-name-${source.id}`} onChange={(event) => setDisplayName(event.target.value)} required value={displayName} /></Field><Field><FieldLabel htmlFor={`source-url-${source.id}`}>URL</FieldLabel><Input id={`source-url-${source.id}`} onChange={(event) => setURL(event.target.value)} required type="url" value={url} /></Field><Field><FieldLabel htmlFor={`source-key-${source.id}`}>{copy(language, "Ed25519 公钥", "Ed25519 public key")}</FieldLabel><Textarea id={`source-key-${source.id}`} onChange={(event) => setPublicKey(event.target.value)} required value={publicKey} /></Field><Field><FieldLabel htmlFor={`source-refresh-${source.id}`}>{copy(language, "刷新间隔（秒）", "Refresh interval (seconds)")}</FieldLabel><Input id={`source-refresh-${source.id}`} max={604800} min={300} onChange={(event) => setRefreshSeconds(Number(event.target.value))} required type="number" value={refreshSeconds} /></Field><Field><FieldLabel htmlFor={`source-token-${source.id}`}>Bearer Token</FieldLabel><Input disabled={clearBearer} id={`source-token-${source.id}`} onChange={(event) => setBearerToken(event.target.value)} placeholder={source.bearerTokenSet ? copy(language, "留空以保留现有 Token", "Leave blank to keep the stored token") : copy(language, "未配置", "Not configured")} type="password" value={bearerToken} /></Field>{source.bearerTokenSet ? <Field orientation="horizontal"><div className="flex-1"><FieldLabel htmlFor={`source-token-clear-${source.id}`}>{copy(language, "清除已保存的 Token", "Clear stored token")}</FieldLabel><FieldDescription>{copy(language, "保存后无法撤销。", "This cannot be undone after saving.")}</FieldDescription></div><Switch checked={clearBearer} id={`source-token-clear-${source.id}`} onCheckedChange={(checked) => { setClearBearer(checked); if (checked) setBearerToken(""); }} /></Field> : null}<Field><FieldLabel htmlFor={`source-ca-${source.id}`}>{copy(language, "新的自定义 CA", "New custom CA")}</FieldLabel><Textarea disabled={clearCA} id={`source-ca-${source.id}`} onChange={(event) => setCustomCA(event.target.value)} placeholder={source.customCASet ? copy(language, "留空以保留现有 CA", "Leave blank to keep the stored CA") : copy(language, "未配置", "Not configured")} value={customCA} /></Field>{source.customCASet ? <Field orientation="horizontal"><div className="flex-1"><FieldLabel htmlFor={`source-ca-clear-${source.id}`}>{copy(language, "清除已保存的 CA", "Clear stored CA")}</FieldLabel><FieldDescription>{copy(language, "后续连接将恢复使用系统信任库。", "Future connections will use the system trust store.")}</FieldDescription></div><Switch checked={clearCA} id={`source-ca-clear-${source.id}`} onCheckedChange={(checked) => { setClearCA(checked); if (checked) setCustomCA(""); }} /></Field> : null}{error ? <FieldError role="alert">{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={close} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "保存", "Save")}</Button></SheetFooter></form></SheetContent></Sheet>;
}

type SourceInput = { id: string; displayName: string; url: string; publicKey: string; bearerToken: string; customCA: string; refreshIntervalSeconds: number };

function SourceSheet({ language, onClose, onSubmit, open }: { language: Language; onClose: () => void; onSubmit: (source: SourceInput) => Promise<void>; open: boolean }) {
  const [source, setSource] = useState<SourceInput>({ id: "", displayName: "", url: "", publicKey: "", bearerToken: "", customCA: "", refreshIntervalSeconds: 3600 });
  const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  const requestEpoch = useRef(0);
  const close = () => { requestEpoch.current += 1; setSource({ id: "", displayName: "", url: "", publicKey: "", bearerToken: "", customCA: "", refreshIntervalSeconds: 3600 }); setBusy(false); setError(""); onClose(); };
  const submit = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); const epoch = requestEpoch.current; setBusy(true); setError(""); try { await onSubmit(source); if (epoch === requestEpoch.current) close(); } catch (submitError) { if (epoch === requestEpoch.current) setError(userError(language, submitError)); } finally { if (epoch === requestEpoch.current) setBusy(false); } };
  return <Sheet onOpenChange={(next) => { if (!next) close(); }} open={open}><SheetContent className="data-[side=right]:w-[calc(100%-1rem)] data-[side=right]:sm:max-w-lg"><SheetHeader><SheetTitle>{copy(language, "添加应用目录", "Add app catalog")}</SheetTitle><SheetDescription>{copy(language, "目录必须使用固定公钥签名。Bearer Token 会加密保存；API 只返回凭据是否已配置。", "The catalog must use a pinned signing key. Bearer tokens are encrypted at rest; the API returns only whether credentials are configured.")}</SheetDescription></SheetHeader><form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Field><FieldLabel htmlFor="source-id">ID</FieldLabel><Input id="source-id" onChange={(event) => setSource((current) => ({ ...current, id: event.target.value }))} pattern="[a-z][a-z0-9-]{1,62}" required value={source.id} /><FieldDescription>{copy(language, "创建后不可修改，用于隔离不同目录的应用身份。", "Cannot be changed after creation; it namespaces app identities.")}</FieldDescription></Field><Field><FieldLabel htmlFor="source-name">{copy(language, "显示名称", "Display name")}</FieldLabel><Input id="source-name" onChange={(event) => setSource((current) => ({ ...current, displayName: event.target.value }))} required value={source.displayName} /></Field><Field><FieldLabel htmlFor="source-url">URL</FieldLabel><Input id="source-url" onChange={(event) => setSource((current) => ({ ...current, url: event.target.value }))} required type="url" value={source.url} /><FieldDescription>{copy(language, "必须是无内嵌用户名或密码的 HTTPS 地址。", "Must be an HTTPS URL without embedded credentials.")}</FieldDescription></Field><Field><FieldLabel htmlFor="source-key">{copy(language, "Ed25519 公钥", "Ed25519 public key")}</FieldLabel><Textarea id="source-key" onChange={(event) => setSource((current) => ({ ...current, publicKey: event.target.value }))} required value={source.publicKey} /></Field><Field><FieldLabel htmlFor="source-token">Bearer Token</FieldLabel><Input id="source-token" onChange={(event) => setSource((current) => ({ ...current, bearerToken: event.target.value }))} type="password" value={source.bearerToken} /><FieldDescription>{copy(language, "公开目录可留空。", "Leave empty for a public catalog.")}</FieldDescription></Field><Field><FieldLabel htmlFor="source-ca">{copy(language, "自定义 CA（可选）", "Custom CA (optional)")}</FieldLabel><Textarea id="source-ca" onChange={(event) => setSource((current) => ({ ...current, customCA: event.target.value }))} value={source.customCA} /></Field>{error ? <FieldError role="alert">{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={close} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "添加目录", "Add catalog")}</Button></SheetFooter></form></SheetContent></Sheet>;
}
