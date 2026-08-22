import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { CopyIcon, ExternalLinkIcon, LinkIcon, PencilIcon, PlusIcon, RefreshCwIcon, RotateCcwIcon, Trash2Icon, UsersIcon } from "lucide-react";
import { api } from "../api";
import type { Application, ApplicationCommand, ThreeXUIClient, ThreeXUIClientCommandInput, ThreeXUIClientInbound } from "../types";
import type { Language } from "../translations";
import { copy } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";

type Editor = { client?: ThreeXUIClient } | null;
type RevealedLink = { title: string; value: string } | null;

const gibibyte = 1024 * 1024 * 1024;

export function ThreeXUIClientsSheet({ application, advancedURL, language, onClose }: { application: Application | null; advancedURL?: string; language: Language; onClose: () => void }) {
  const [command, setCommand] = useState<ApplicationCommand | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(1);
  const [editor, setEditor] = useState<Editor>(null);
  const [deleteClient, setDeleteClient] = useState<ThreeXUIClient | null>(null);
  const [revealed, setRevealed] = useState<RevealedLink>(null);
  const clients = command?.clients ?? [];
  const inbounds = command?.inbounds ?? [];
  const filteredClients = useMemo(() => clients.filter((client) => client.email.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase())), [clients, search]);
  const pageCount = Math.max(1, Math.ceil(filteredClients.length / 25));
  const visibleClients = filteredClients.slice((Math.min(page, pageCount) - 1) * 25, Math.min(page, pageCount) * 25);

  const runCommand = useCallback(async (input: Omit<ThreeXUIClientCommandInput, "applicationId">) => {
    if (!application) throw new Error("Application is unavailable");
    setBusy(true);
    setError("");
    try {
      let next = await api.createThreeXUIClientCommand({ applicationId: application.id, ...input });
      const adopt = (value: ApplicationCommand) => setCommand((current) => value.clientsObserved || !current ? value : { ...value, clients: current.clients, clientsObserved: current.clientsObserved, inbounds: value.inbounds?.length ? value.inbounds : current.inbounds });
      adopt(next);
      for (let attempt = 0; next.state === "pending" || next.state === "running"; attempt += 1) {
        if (attempt >= 120) throw new Error("The node did not respond in time");
        await new Promise((resolve) => window.setTimeout(resolve, 1000));
        next = await api.applicationCommand(next.id);
        adopt(next);
      }
      if (next.state === "failed") throw new Error(next.error || "The 3x-ui operation failed");
      return next;
    } finally {
      setBusy(false);
    }
  }, [application]);

  useEffect(() => {
    if (!application) {
      setCommand(null); setEditor(null); setDeleteClient(null); setRevealed(null); setError("");
      setNotice(""); setSearch(""); setPage(1);
      return;
    }
    let cancelled = false;
    void runCommand({ action: "list" }).catch((loadError) => {
      if (!cancelled) setError(readableError(language, loadError));
    });
    return () => { cancelled = true; };
  }, [application?.id, language, runCommand]);

  const run = async (input: Omit<ThreeXUIClientCommandInput, "applicationId">) => {
    setRevealed(null);
    setNotice("");
    try {
      await runCommand(input);
      if (input.action !== "list" && input.action !== "reveal_link" && input.action !== "reveal_subscription") setNotice(copy(language, "更改已同步到 3x-ui。", "The change was synced to 3x-ui."));
    } catch (operationError) {
      setError(readableError(language, operationError));
      throw operationError;
    }
  };

  const reveal = async (client: ThreeXUIClient, action: "reveal_link" | "reveal_subscription") => {
    const publishedInbound = inbounds.find((inbound) => inbound.connectHostname && client.inboundIds.includes(inbound.id));
    try {
      const next = await runCommand({ action, email: client.email, inboundId: action === "reveal_link" ? publishedInbound?.id : undefined });
      const result = await api.revealApplicationCommand(next.id);
      const title = action === "reveal_link"
        ? copy(language, "VLESS 客户端链接", "VLESS client link")
        : copy(language, "订阅地址", "Subscription URL");
      setRevealed({ title, value: result.shareUri });
      await navigator.clipboard?.writeText(result.shareUri).catch(() => undefined);
    } catch (operationError) {
      setError(readableError(language, operationError));
    }
  };

  return <Sheet onOpenChange={(open) => { if (!open && editor && !window.confirm(copy(language, "放弃尚未保存的修改？", "Discard unsaved changes?"))) return; if (!open) onClose(); }} open={Boolean(application)}>
    <SheetContent className="w-full sm:max-w-3xl">
      <SheetHeader>
        <SheetTitle>{copy(language, "管理 3x-ui 客户端", "Manage 3x-ui clients")}</SheetTitle>
        <SheetDescription>{copy(language, "在这里完成日常管理；路由、协议参数等少用设置仍在 3x-ui 中调整。", "Handle everyday tasks here. Use 3x-ui only for advanced routing and protocol details.")}</SheetDescription>
      </SheetHeader>

      {editor ? <ClientEditor busy={busy} editor={editor} inbounds={inbounds} language={language} onCancel={() => setEditor(null)} onSave={async (input) => { await run(input); setEditor(null); }} /> : <>
        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-2">
          <div className="flex flex-wrap items-center gap-2">
            <Button disabled={busy || inbounds.length === 0} onClick={() => setEditor({})} size="sm"><PlusIcon data-icon="inline-start" />{copy(language, "添加客户端", "Add client")}</Button>
            <Button aria-label={copy(language, "刷新客户端", "Refresh clients")} disabled={busy} onClick={() => void run({ action: "list" }).catch(() => undefined)} size="icon-sm" variant="outline">{busy ? <Spinner /> : <RefreshCwIcon />}</Button>
            <span aria-live="polite" className="text-xs text-muted-foreground">{busy ? copy(language, "正在等待节点响应…", "Waiting for the node…") : copy(language, `${clients.length} 个客户端`, `${clients.length} clients`)}</span>
            {advancedURL ? <Button className="ml-auto" nativeButton={false} render={<a href={advancedURL} rel="noreferrer" target="_blank" />} size="sm" variant="ghost"><ExternalLinkIcon data-icon="inline-start" />{copy(language, "高级设置", "Advanced settings")}</Button> : null}
          </div>

          {clients.length > 8 ? <div><label className="sr-only" htmlFor="three-xui-client-search">{copy(language, "搜索客户端", "Search clients")}</label><Input id="three-xui-client-search" onChange={(event) => { setSearch(event.target.value); setPage(1); }} placeholder={copy(language, "搜索客户端", "Search clients")} type="search" value={search} /></div> : null}

          {revealed ? <Alert><LinkIcon /><AlertTitle>{revealed.title}</AlertTitle><AlertDescription><p>{copy(language, "地址已尝试复制到剪贴板。请只发送给你信任的设备。", "The URL was copied when permitted. Share it only with devices you trust.")}</p><div className="mt-3 flex items-start gap-2 rounded-lg bg-muted p-3"><code className="min-w-0 flex-1 break-all text-xs">{revealed.value}</code><Button aria-label={copy(language, "复制地址", "Copy URL")} onClick={() => void navigator.clipboard?.writeText(revealed.value)} size="icon-sm" variant="outline"><CopyIcon /></Button></div></AlertDescription></Alert> : null}
          {notice ? <p aria-live="polite" className="text-sm text-muted-foreground">{notice}</p> : null}
          {error ? <Alert variant="destructive"><AlertTitle>{copy(language, "操作没有完成", "Operation did not complete")}</AlertTitle><AlertDescription>{error}</AlertDescription></Alert> : null}
          {busy && clients.length === 0 ? <div className="flex min-h-28 items-center justify-center gap-2 rounded-2xl border text-sm text-muted-foreground"><Spinner />{copy(language, "正在从节点读取客户端…", "Loading clients from the node…")}</div> : null}
          {inbounds.length === 0 && !busy ? <Alert><AlertTitle>{copy(language, "还没有 VLESS REALITY 入站", "No VLESS REALITY inbound yet")}</AlertTitle><AlertDescription>{copy(language, "先返回应用卡片一键创建入站，再来添加客户端。", "Create a VLESS REALITY inbound from the app card first, then add clients here.")}</AlertDescription></Alert> : null}

          {!busy && clients.length === 0 ? <Empty className="border"><EmptyHeader><EmptyMedia variant="icon"><UsersIcon /></EmptyMedia><EmptyTitle>{copy(language, "还没有客户端", "No clients yet")}</EmptyTitle><EmptyDescription>{copy(language, "为手机、电脑或路由器各创建一个客户端，便于单独停用和查看流量。", "Create one client per phone, computer, or router so each can be disabled and tracked separately.")}</EmptyDescription>{inbounds.length ? <Button className="mt-3" onClick={() => setEditor({})} size="sm"><PlusIcon data-icon="inline-start" />{copy(language, "添加第一个客户端", "Add first client")}</Button> : null}</EmptyHeader></Empty> : null}

          <div className="grid gap-3">
            {visibleClients.map((client) => {
              const publishedInbound = inbounds.find((inbound) => inbound.connectHostname && client.inboundIds.includes(inbound.id));
              const inboundNames = inbounds.filter((inbound) => client.inboundIds.includes(inbound.id)).map((inbound) => inbound.name);
              return <div className="rounded-2xl border bg-card p-4" key={client.email}>
                <div className="flex flex-wrap items-start gap-3">
                  <div className="min-w-0 flex-1"><div className="flex flex-wrap items-center gap-2"><h3 className="truncate font-medium">{client.email}</h3><Badge variant={client.enabled ? "secondary" : "outline"}>{client.enabled ? copy(language, "已启用", "Enabled") : copy(language, "已停用", "Disabled")}</Badge></div><p className="mt-1 text-xs text-muted-foreground">{inboundNames.length ? inboundNames.join(" · ") : copy(language, "未连接入站", "No inbound attached")}</p></div>
                  <Switch aria-label={copy(language, `启用 ${client.email}`, `Enable ${client.email}`)} checked={client.enabled} disabled={busy} onCheckedChange={(enabled) => void run({ action: "set_enabled", email: client.email, enabled }).catch(() => undefined)} />
                </div>
                <div className="mt-4 grid gap-3 text-xs sm:grid-cols-3"><Metric label={copy(language, "已用流量", "Traffic used")} value={`${formatBytes(client.usedBytes)}${client.totalBytes ? ` / ${formatBytes(client.totalBytes)}` : ""}`} /><Metric label={copy(language, "有效期", "Expires")} value={formatExpiry(client.expiryTime, language)} /><Metric label={copy(language, "设备数限制", "IP limit")} value={client.limitIp ? String(client.limitIp) : copy(language, "不限", "Unlimited")} /></div>
                {deleteClient?.email === client.email ? <Alert className="mt-4" variant="destructive"><Trash2Icon /><AlertTitle>{copy(language, `删除“${client.email}”？`, `Delete “${client.email}”?`)}</AlertTitle><AlertDescription><p>{copy(language, "该客户端会立即无法连接，此操作不能撤销。", "This client will stop connecting immediately. This cannot be undone.")}</p><div className="mt-3 flex gap-2"><Button disabled={busy} onClick={() => setDeleteClient(null)} size="sm" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy} onClick={() => void run({ action: "delete", email: client.email }).then(() => setDeleteClient(null)).catch(() => undefined)} size="sm" variant="destructive">{copy(language, "确认删除", "Delete client")}</Button></div></AlertDescription></Alert> : <div className="mt-4 flex flex-wrap gap-2"><Button disabled={busy || !publishedInbound} onClick={() => void reveal(client, "reveal_link")} size="sm" variant="outline"><LinkIcon data-icon="inline-start" />{copy(language, "复制 VLESS", "Copy VLESS")}</Button><Button disabled={busy || !command?.subscriptionAvailable} onClick={() => void reveal(client, "reveal_subscription")} size="sm" variant="outline"><LinkIcon data-icon="inline-start" />{copy(language, "复制订阅", "Copy subscription")}</Button><Button disabled={busy} onClick={() => setEditor({ client })} size="icon-sm" title={copy(language, "编辑", "Edit")} variant="ghost"><PencilIcon /><span className="sr-only">{copy(language, "编辑", "Edit")}</span></Button><Button disabled={busy} onClick={() => void run({ action: "reset_traffic", email: client.email }).catch(() => undefined)} size="icon-sm" title={copy(language, "重置流量", "Reset traffic")} variant="ghost"><RotateCcwIcon /><span className="sr-only">{copy(language, "重置流量", "Reset traffic")}</span></Button><Button disabled={busy} onClick={() => setDeleteClient(client)} size="icon-sm" title={copy(language, "删除", "Delete")} variant="ghost"><Trash2Icon /><span className="sr-only">{copy(language, "删除", "Delete")}</span></Button></div>}
                {!publishedInbound ? <p className="mt-2 text-xs text-muted-foreground">{copy(language, "为入站完成公网发布后才能导出 VLESS 链接。", "Publish the inbound before exporting a VLESS URL.")}</p> : null}
                {!command?.subscriptionAvailable ? <p className="mt-1 text-xs text-muted-foreground">{copy(language, "开启公网订阅后才能复制订阅地址。", "Enable public subscription before copying a subscription URL.")}</p> : null}
                {command?.subscriptionAvailable ? <p className="mt-1 text-xs text-muted-foreground">{copy(language, "同一地址会自动适配 OpenClash、Mihomo 和其他客户端。", "The same URL automatically adapts to OpenClash, Mihomo, and other clients.")}</p> : null}
              </div>;
            })}
          </div>
          {pageCount > 1 ? <div className="flex items-center justify-between"><Button disabled={page <= 1} onClick={() => setPage((value) => Math.max(1, value - 1))} size="sm" variant="outline">{copy(language, "上一页", "Previous")}</Button><span className="text-xs tabular-nums text-muted-foreground">{Math.min(page, pageCount)} / {pageCount}</span><Button disabled={page >= pageCount} onClick={() => setPage((value) => Math.min(pageCount, value + 1))} size="sm" variant="outline">{copy(language, "下一页", "Next")}</Button></div> : null}
        </div>
        <SheetFooter><Button onClick={onClose} variant="outline">{copy(language, "关闭", "Close")}</Button></SheetFooter>
      </>}
    </SheetContent>
  </Sheet>;
}

function ClientEditor({ busy, editor, inbounds, language, onCancel, onSave }: { busy: boolean; editor: NonNullable<Editor>; inbounds: ThreeXUIClientInbound[]; language: Language; onCancel: () => void; onSave: (input: Omit<ThreeXUIClientCommandInput, "applicationId">) => Promise<void> }) {
  const client = editor.client;
  const [name, setName] = useState(client?.email ?? "");
  const [inboundID, setInboundID] = useState(client?.inboundIds[0] ?? inbounds[0]?.id ?? 0);
  const [quota, setQuota] = useState(client?.totalBytes ? String(Math.round(client.totalBytes / gibibyte * 100) / 100) : "");
  const [expiry, setExpiry] = useState(client?.expiryTime ? localDateValue(client.expiryTime) : "");
  const [limitIP, setLimitIP] = useState(client?.limitIp ? String(client.limitIp) : "");
  const [enabled, setEnabled] = useState(client?.enabled ?? true);
  const [error, setError] = useState("");
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault(); setError("");
    const totalBytes = quota ? Math.round(Number(quota) * gibibyte) : 0;
    const expiryTime = expiry ? new Date(`${expiry}T23:59:59`).getTime() : 0;
    const limit = limitIP ? Number(limitIP) : 0;
    if (!name.trim() || !Number.isFinite(totalBytes) || totalBytes < 0 || !Number.isInteger(limit) || limit < 0) { setError(copy(language, "请检查名称、流量和设备数。", "Check the name, quota, and IP limit.")); return; }
    try {
      await onSave(client ? { action: "update", email: client.email, newEmail: name.trim(), totalBytes, expiryTime, limitIp: limit } : { action: "create", newEmail: name.trim(), inboundId: inboundID, enabled, totalBytes, expiryTime, limitIp: limit });
    } catch (saveError) { setError(readableError(language, saveError)); }
  };
  return <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Field><FieldLabel htmlFor="three-xui-client-name">{copy(language, "名称", "Name")}</FieldLabel><Input autoFocus id="three-xui-client-name" maxLength={64} onChange={(event) => setName(event.target.value)} placeholder={copy(language, "例如：我的 MacBook", "For example: My MacBook")} required value={name} /><FieldDescription>{copy(language, "这是设备名称，不需要填写真实邮箱。", "This is a device label, not a real email address.")}</FieldDescription></Field><Field><FieldLabel htmlFor="three-xui-client-inbound">{copy(language, "使用的入站", "Inbound")}</FieldLabel><NativeSelect disabled={Boolean(client)} id="three-xui-client-inbound" onChange={(event) => setInboundID(Number(event.target.value))} required value={inboundID}>{inbounds.map((inbound) => <option key={inbound.id} value={inbound.id}>{inbound.name}{inbound.connectHostname ? ` · ${inbound.connectHostname}` : ""}</option>)}</NativeSelect>{client ? <FieldDescription>{copy(language, "更改客户端所属入站属于高级操作，请在 3x-ui 中完成。", "Changing inbound attachments is an advanced task handled in 3x-ui.")}</FieldDescription> : null}</Field><Field><FieldLabel htmlFor="three-xui-client-quota">{copy(language, "流量上限（GB）", "Traffic limit (GB)")}</FieldLabel><Input id="three-xui-client-quota" min="0" onChange={(event) => setQuota(event.target.value)} placeholder={copy(language, "留空表示不限", "Leave empty for unlimited")} step="0.1" type="number" value={quota} /></Field><Field><FieldLabel htmlFor="three-xui-client-expiry">{copy(language, "到期日期", "Expiry date")}</FieldLabel><Input id="three-xui-client-expiry" min={new Date().toISOString().slice(0, 10)} onChange={(event) => setExpiry(event.target.value)} type="date" value={expiry} /><FieldDescription>{copy(language, "留空表示永不过期。", "Leave empty to never expire.")}</FieldDescription></Field><Field><FieldLabel htmlFor="three-xui-client-limit-ip">{copy(language, "同时使用的设备数", "Simultaneous devices")}</FieldLabel><Input id="three-xui-client-limit-ip" min="0" onChange={(event) => setLimitIP(event.target.value)} placeholder={copy(language, "留空表示不限", "Leave empty for unlimited")} step="1" type="number" value={limitIP} /></Field>{!client ? <Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="three-xui-client-enabled">{copy(language, "创建后立即启用", "Enable after creation")}</FieldLabel><FieldDescription>{copy(language, "关闭时会保存客户端，但暂时不能连接。", "When off, the client is saved but cannot connect yet.")}</FieldDescription></div><Switch checked={enabled} id="three-xui-client-enabled" onCheckedChange={setEnabled} /></Field> : null}{error ? <FieldError role="alert">{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button disabled={busy} onClick={onCancel} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !name.trim() || (!client && !inboundID)} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{client ? copy(language, "保存修改", "Save changes") : copy(language, "添加客户端", "Add client")}</Button></SheetFooter></form>;
}

function Metric({ label, value }: { label: string; value: string }) { return <div><p className="text-muted-foreground">{label}</p><p className="mt-1 font-medium tabular-nums">{value}</p></div>; }

function formatBytes(value: number) {
  if (!value) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  return `${(value / 1024 ** index).toFixed(index > 1 ? 1 : 0)} ${units[index]}`;
}

function formatExpiry(value: number, language: Language) { return value ? new Intl.DateTimeFormat(language, { dateStyle: "medium" }).format(value) : copy(language, "永不过期", "Never"); }
function localDateValue(value: number) { const date = new Date(value); const offset = date.getTimezoneOffset() * 60_000; return new Date(date.getTime() - offset).toISOString().slice(0, 10); }
function readableError(language: Language, error: unknown) { return error instanceof Error && error.message ? error.message.replace(/^center:\s*/i, "") : copy(language, "操作失败，请稍后重试。", "Operation failed. Try again shortly."); }
