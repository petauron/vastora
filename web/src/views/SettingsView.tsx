import { useState, type FormEvent } from "react";
import { ChevronDownIcon, DatabaseBackupIcon, DatabaseIcon, DownloadIcon, KeyRoundIcon, PlusIcon, RefreshCwIcon, SettingsIcon, ShieldCheckIcon } from "lucide-react";
import { api } from "../api";
import { SignOutButton, type AppData, type Mutate } from "../App";
import { administratorPasswordMinLength } from "../lib/security";
import type { CatalogSource } from "../types";
import type { Language } from "../translations";
import { PageHeading, StateBadge, TechnicalError, copy, formatDate, userError } from "./shared";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { CenterUpdateCard } from "./CenterUpdateCard";

export function SettingsView({ data, language, mutate, onLogout }: { data: AppData; language: Language; mutate: Mutate; onLogout: () => Promise<void> }) {
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
        <CardContent><dl className="grid gap-4 text-sm sm:grid-cols-3"><div><dt className="text-muted-foreground">{copy(language, "版本", "Version")}</dt><dd className="mt-1 font-medium">{data.status.version}</dd></div><div><dt className="text-muted-foreground">{copy(language, "节点", "Nodes")}</dt><dd className="mt-1 font-medium">{data.agents.filter((agent) => agent.status === "active").length}</dd></div><div><dt className="text-muted-foreground">{copy(language, "应用", "Apps")}</dt><dd className="mt-1 font-medium">{data.applications.length}</dd></div></dl></CardContent>
        <CardFooter className="justify-end"><Button onClick={() => setPasswordOpen(true)} size="sm" variant="outline"><KeyRoundIcon data-icon="inline-start" />{copy(language, "修改管理员密码", "Change administrator password")}</Button></CardFooter>
      </Card>
      <CenterUpdateCard initial={data.centerUpdate} language={language} />
      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><ShieldCheckIcon />{copy(language, "数据与故障排查", "Data & troubleshooting")}</CardTitle><CardDescription>{copy(language, "备份 Center 配置，或下载不含密钥的诊断信息。", "Back up Center configuration or download diagnostics that contain no secret values.")}</CardDescription></CardHeader>
        <CardContent>
          <div className="grid gap-3 sm:grid-cols-2"><Button className="h-auto justify-start py-3" onClick={() => setBackupOpen(true)} variant="outline"><DatabaseBackupIcon data-icon="inline-start" /><span className="text-left"><span className="block">{copy(language, "下载加密备份", "Download encrypted backup")}</span><span className="block text-xs font-normal text-muted-foreground">{copy(language, "包含数据库和密钥，需要密码恢复", "Database and keys; password required to restore")}</span></span></Button><Button className="h-auto justify-start py-3" disabled={diagnosticsBusy} onClick={() => void downloadDiagnostics()} variant="outline">{diagnosticsBusy ? <Spinner data-icon="inline-start" /> : <DownloadIcon data-icon="inline-start" />}<span className="text-left"><span className="block">{copy(language, "下载诊断报告", "Download diagnostics")}</span><span className="block text-xs font-normal text-muted-foreground">{copy(language, "版本、健康状态和最近错误，不含 Token", "Version, health, and recent errors; no tokens")}</span></span></Button></div>
          {diagnosticsError ? <FieldError className="mt-3" role="alert">{diagnosticsError}</FieldError> : null}
          <p className="mt-4 text-xs leading-5 text-muted-foreground">{copy(language, "恢复时先停止 Center，再使用 vastora center restore 将备份恢复到新的空数据目录。", "To restore, stop Center and use vastora center restore with a new empty data directory.")}</p>
        </CardContent>
      </Card>
      <CatalogSettings data={data} language={language} mutate={mutate} onAdd={() => setAdding(true)} />
      <SourceSheet language={language} onClose={() => setAdding(false)} onSubmit={async (source) => { await mutate(() => api.createSource(source), copy(language, "应用目录已添加。", "App catalog added.")); setAdding(false); }} open={adding} />
      <BackupSheet language={language} onClose={() => setBackupOpen(false)} open={backupOpen} />
      <PasswordSheet language={language} onClose={() => setPasswordOpen(false)} open={passwordOpen} />
    </section>
  );
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
  const submit = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); setError(""); if (password !== confirmation) { setError(copy(language, "两次输入的密码不一致。", "The passwords do not match.")); return; } setBusy(true); try { await api.downloadBackup(password); setPassword(""); setConfirmation(""); onClose(); } catch (submitError) { setError(userError(language, submitError)); } finally { setBusy(false); } };
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={open}><SheetContent><SheetHeader><SheetTitle>{copy(language, "下载加密备份", "Download encrypted backup")}</SheetTitle><SheetDescription>{copy(language, "这个密码不会保存，也无法找回。恢复时必须使用完全相同的密码。", "This password is not stored and cannot be recovered. The exact same password is required to restore.")}</SheetDescription></SheetHeader><form className="flex flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 px-4"><FieldGroup><Field data-invalid={Boolean(error)}><FieldLabel htmlFor="backup-password">{copy(language, "备份密码", "Backup password")}</FieldLabel><Input autoComplete="new-password" id="backup-password" minLength={12} onChange={(event) => setPassword(event.target.value)} required type="password" value={password} /><FieldDescription>{copy(language, "至少 12 个字符；请保存在密码管理器中。", "At least 12 characters. Save it in a password manager.")}</FieldDescription></Field><Field data-invalid={Boolean(error)}><FieldLabel htmlFor="backup-confirmation">{copy(language, "再次输入", "Enter again")}</FieldLabel><Input autoComplete="new-password" id="backup-confirmation" minLength={12} onChange={(event) => setConfirmation(event.target.value)} required type="password" value={confirmation} />{error ? <FieldError>{error}</FieldError> : null}</Field></FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || password.length < 12 || password !== confirmation} type="submit">{busy ? <Spinner data-icon="inline-start" /> : <DatabaseBackupIcon data-icon="inline-start" />}{copy(language, "创建并下载", "Create and download")}</Button></SheetFooter></form></SheetContent></Sheet>;
}

function SourceCard({ language, mutate, source }: { language: Language; mutate: Mutate; source: CatalogSource }) {
  const [busy, setBusy] = useState(false);
  const refresh = async () => { setBusy(true); try { await mutate(() => api.refreshSource(source.id), copy(language, "目录已刷新。", "Catalog refreshed.")); } catch { /* The shared notice already explains the failure. */ } finally { setBusy(false); } };
  return <Card><CardHeader><CardTitle className="flex items-center gap-2"><DatabaseIcon />{source.displayName}</CardTitle><CardDescription className="truncate">{source.url}</CardDescription><CardAction><StateBadge value={source.lastError ? "failed" : source.enabled ? "active" : "disabled"} /></CardAction></CardHeader><CardContent><dl className="grid grid-cols-2 gap-3 text-sm"><div><dt className="text-muted-foreground">{copy(language, "上次同步", "Last sync")}</dt><dd className="mt-1">{formatDate(language, source.fetchedAt)}</dd></div><div><dt className="text-muted-foreground">{copy(language, "刷新间隔", "Refresh interval")}</dt><dd className="mt-1">{Math.round(source.refreshIntervalSeconds / 60)} min</dd></div></dl>{source.lastError ? <div className="mt-3"><TechnicalError error={source.lastError} language={language} /></div> : null}</CardContent><CardFooter className="justify-end"><Button disabled={busy} onClick={() => void refresh()} size="sm" variant="outline">{busy ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" />}{copy(language, "刷新", "Refresh")}</Button></CardFooter></Card>;
}

type SourceInput = { id: string; displayName: string; url: string; publicKey: string; bearerToken: string; customCA: string; refreshIntervalSeconds: number };

function SourceSheet({ language, onClose, onSubmit, open }: { language: Language; onClose: () => void; onSubmit: (source: SourceInput) => Promise<void>; open: boolean }) {
  const [source, setSource] = useState<SourceInput>({ id: "", displayName: "", url: "", publicKey: "", bearerToken: "", customCA: "", refreshIntervalSeconds: 3600 });
  const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  const submit = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); setBusy(true); setError(""); try { await onSubmit(source); setSource({ id: "", displayName: "", url: "", publicKey: "", bearerToken: "", customCA: "", refreshIntervalSeconds: 3600 }); } catch (submitError) { setError(userError(language, submitError)); } finally { setBusy(false); } };
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={open}><SheetContent className="sm:max-w-lg"><SheetHeader><SheetTitle>{copy(language, "添加应用目录", "Add app catalog")}</SheetTitle><SheetDescription>{copy(language, "目录必须使用固定公钥签名。认证与自定义 CA 会加密保存。", "The catalog must be signed by the pinned public key. Authentication and custom CAs are encrypted at rest.")}</SheetDescription></SheetHeader><form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Field><FieldLabel htmlFor="source-id">ID</FieldLabel><Input id="source-id" onChange={(event) => setSource((current) => ({ ...current, id: event.target.value }))} pattern="[a-z0-9-]+" required value={source.id} /></Field><Field><FieldLabel htmlFor="source-name">{copy(language, "显示名称", "Display name")}</FieldLabel><Input id="source-name" onChange={(event) => setSource((current) => ({ ...current, displayName: event.target.value }))} required value={source.displayName} /></Field><Field><FieldLabel htmlFor="source-url">URL</FieldLabel><Input id="source-url" onChange={(event) => setSource((current) => ({ ...current, url: event.target.value }))} required type="url" value={source.url} /></Field><Field><FieldLabel htmlFor="source-key">{copy(language, "Ed25519 公钥", "Ed25519 public key")}</FieldLabel><Textarea id="source-key" onChange={(event) => setSource((current) => ({ ...current, publicKey: event.target.value }))} required value={source.publicKey} /></Field><Field><FieldLabel htmlFor="source-token">Bearer Token</FieldLabel><Input id="source-token" onChange={(event) => setSource((current) => ({ ...current, bearerToken: event.target.value }))} type="password" value={source.bearerToken} /><FieldDescription>{copy(language, "公开目录可留空。", "Leave empty for a public catalog.")}</FieldDescription></Field><Field><FieldLabel htmlFor="source-ca">{copy(language, "自定义 CA（可选）", "Custom CA (optional)")}</FieldLabel><Textarea id="source-ca" onChange={(event) => setSource((current) => ({ ...current, customCA: event.target.value }))} value={source.customCA} /></Field>{error ? <FieldError>{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "添加目录", "Add catalog")}</Button></SheetFooter></form></SheetContent></Sheet>;
}
