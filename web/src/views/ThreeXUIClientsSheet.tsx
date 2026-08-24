import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { CheckIcon, CopyIcon, ExternalLinkIcon, LinkIcon, PencilIcon, PlusIcon, RefreshCwIcon, RotateCcwIcon, ServerIcon, Trash2Icon, UsersIcon } from "lucide-react";
import { api } from "../api";
import type { Application, ApplicationCommand, ThreeXUIClient, ThreeXUIClientCommandInput, ThreeXUIClientInbound } from "../types";
import type { Language } from "../translations";
import { copy, userError } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { useApplicationCommandExecutor } from "../hooks/use-application-command-executor";
import { bytesFromGB, dateInputValueInTimeZone, endOfDayEpochInTimeZone, formatBytes, gigabytesFromBytes, nextRenewalDateInTimeZone, SubscriptionTrafficPlanFields } from "./TrafficPlanFields";
import { hasObservedThreeXUIState, mergeCachedCommand, mergeCommandUpdate } from "./threeXUICommandState";

type Editor = { client?: ThreeXUIClient } | null;
type RevealedLink = { title: string; value: string } | null;

export function ThreeXUIClientsSheet({ application, advancedURL, language, onClose, siteTimezone }: { application: Application | null; advancedURL?: string; language: Language; onClose: () => void; siteTimezone?: string }) {
  const [command, setCommand] = useState<ApplicationCommand | null>(null);
  const [error, setError] = useState("");
  const [refreshError, setRefreshError] = useState("");
  const [showingCached, setShowingCached] = useState(false);
  const [notice, setNotice] = useState("");
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(1);
  const [editor, setEditor] = useState<Editor>(null);
  const [editorDirty, setEditorDirty] = useState(false);
  const [deleteClient, setDeleteClient] = useState<ThreeXUIClient | null>(null);
  const [resetClient, setResetClient] = useState<ThreeXUIClient | null>(null);
  const [revealed, setRevealed] = useState<RevealedLink>(null);
  const { execute, running: busy } = useApplicationCommandExecutor(application?.id);
  const clients = command?.clients ?? [];
  const inbounds = command?.inbounds ?? [];
  const filteredClients = useMemo(() => clients.filter((client) => client.email.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase())), [clients, search]);
  const pageCount = Math.max(1, Math.ceil(filteredClients.length / 25));
  const visibleClients = filteredClients.slice((Math.min(page, pageCount) - 1) * 25, Math.min(page, pageCount) * 25);
  const openEditor = (next: NonNullable<Editor>) => { setEditorDirty(false); setEditor(next); };
  const discardEditor = () => {
    if (editorDirty && !window.confirm(copy(language, "放弃尚未保存的修改？", "Discard unsaved changes?"))) return false;
    setEditorDirty(false);
    setEditor(null);
    return true;
  };
  const requestClose = () => {
    if (!discardEditor()) return;
    onClose();
  };

  const runCommand = useCallback(async (input: Omit<ThreeXUIClientCommandInput, "applicationId">) => {
    if (!application) throw new Error("Application is unavailable");
    setError("");
    const next = await execute(
      () => api.createThreeXUIClientCommand({ applicationId: application.id, ...input }),
      (value) => setCommand((current) => mergeCommandUpdate(current, value))
    );
    if (next?.state === "failed") throw new Error(next.error || "The 3x-ui operation failed");
    return next;
  }, [application?.id, execute]);

  useEffect(() => {
    setEditor(null); setEditorDirty(false); setDeleteClient(null); setResetClient(null); setRevealed(null);
    setNotice(""); setSearch(""); setPage(1); setError(""); setRefreshError(""); setShowingCached(false); setCommand(null);
  }, [application?.id]);

  useEffect(() => {
    if (!application) return;
    let cancelled = false;
    let freshResolved = false;
    setRefreshError("");
    void api.latestApplicationCommand(application.id, "3xui.clients.manage").then((cached) => {
      if (cancelled || freshResolved || !hasObservedThreeXUIState(cached)) return;
      setCommand((current) => mergeCachedCommand(current, cached));
      setShowingCached(true);
    }).catch(() => { /* First use has no cached observation yet. */ });
    void runCommand({ action: "list" }).then(() => {
      freshResolved = true;
      if (!cancelled) setShowingCached(false);
    }).catch((loadError) => {
      if (!cancelled) setRefreshError(readableError(language, loadError));
    });
    return () => { cancelled = true; };
  }, [application?.id, language, runCommand]);

  const refresh = () => {
    setRefreshError("");
    void runCommand({ action: "list" }).then(() => setShowingCached(false)).catch((loadError) => setRefreshError(readableError(language, loadError)));
  };

  const run = async (input: Omit<ThreeXUIClientCommandInput, "applicationId">) => {
    setRevealed(null);
    setNotice("");
    try {
      const next = await runCommand(input);
      if (next && input.action !== "list" && input.action !== "reveal_link" && input.action !== "reveal_subscription") setNotice(copy(language, "更改已同步到所选节点。", "The change was synced to the selected nodes."));
    } catch (operationError) {
      setError(readableError(language, operationError));
      throw operationError;
    }
  };

  const reveal = async (client: ThreeXUIClient, action: "reveal_link" | "reveal_subscription") => {
    const publishedInbound = inbounds.find((inbound) => inbound.connectHostname && client.inboundIds.includes(inbound.id));
    try {
      const next = await runCommand({ action, email: client.email, inboundId: action === "reveal_link" ? publishedInbound?.id : undefined });
      if (!next) return;
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

  return <Sheet onOpenChange={(open) => { if (!open) requestClose(); }} open={Boolean(application)}>
    <SheetContent className="w-full sm:max-w-3xl">
      <SheetHeader>
        <SheetTitle>{copy(language, "管理 3x-ui 客户端", "Manage 3x-ui clients")}</SheetTitle>
        <SheetDescription>{copy(language, "在这里完成日常管理；路由、协议参数等少用设置仍在 3x-ui 中调整。", "Handle everyday tasks here. Use 3x-ui only for advanced routing and protocol details.")}</SheetDescription>
      </SheetHeader>

      {editor ? <ClientEditor busy={busy} editor={editor} inbounds={inbounds} language={language} onCancel={discardEditor} onDirtyChange={setEditorDirty} onSave={async (input) => { await run(input); setEditorDirty(false); setEditor(null); }} siteTimezone={siteTimezone} /> : <>
        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-2">
          <div className="flex flex-wrap items-center gap-2">
            <Button disabled={busy || inbounds.length === 0} onClick={() => openEditor({})} size="sm"><PlusIcon data-icon="inline-start" />{copy(language, "添加客户端", "Add client")}</Button>
            <Button aria-label={copy(language, "刷新客户端", "Refresh clients")} disabled={busy} onClick={refresh} size="icon-sm" variant="outline">{busy ? <Spinner /> : <RefreshCwIcon />}</Button>
            <span aria-live="polite" className="text-xs text-muted-foreground">{busy && clients.length ? copy(language, `正在后台刷新 · 当前显示 ${clients.length} 个客户端`, `Refreshing in the background · showing ${clients.length} clients`) : busy ? copy(language, "正在等待节点响应…", "Waiting for the node…") : showingCached ? copy(language, `${clients.length} 个客户端 · 上次同步结果`, `${clients.length} clients · last synced result`) : copy(language, `${clients.length} 个客户端`, `${clients.length} clients`)}</span>
            {advancedURL ? <Button className="ml-auto" nativeButton={false} render={<a href={advancedURL} rel="noreferrer" target="_blank" />} size="sm" variant="ghost"><ExternalLinkIcon data-icon="inline-start" />{copy(language, "高级设置", "Advanced settings")}</Button> : null}
          </div>

          {clients.length > 8 ? <div><label className="sr-only" htmlFor="three-xui-client-search">{copy(language, "搜索客户端", "Search clients")}</label><Input id="three-xui-client-search" onChange={(event) => { setSearch(event.target.value); setPage(1); }} placeholder={copy(language, "搜索客户端", "Search clients")} type="search" value={search} /></div> : null}

          {revealed ? <Alert><LinkIcon /><AlertTitle>{revealed.title}</AlertTitle><AlertDescription><p>{copy(language, "地址已尝试复制到剪贴板。请只发送给你信任的设备。", "The URL was copied when permitted. Share it only with devices you trust.")}</p><div className="mt-3 flex items-start gap-2 rounded-lg bg-muted p-3"><code className="min-w-0 flex-1 break-all text-xs">{revealed.value}</code><Button aria-label={copy(language, "复制地址", "Copy URL")} onClick={() => void navigator.clipboard?.writeText(revealed.value)} size="icon-sm" variant="outline"><CopyIcon /></Button></div></AlertDescription></Alert> : null}
          {notice ? <p aria-live="polite" className="text-sm text-muted-foreground">{notice}</p> : null}
          {error ? <Alert variant="destructive"><AlertTitle>{copy(language, "操作没有完成", "Operation did not complete")}</AlertTitle><AlertDescription>{error}</AlertDescription></Alert> : null}
          {refreshError && clients.length ? <Alert><RefreshCwIcon /><AlertTitle>{copy(language, "正在显示上次同步结果", "Showing the last synced result")}</AlertTitle><AlertDescription><p>{refreshError}</p><Button className="mt-3" disabled={busy} onClick={refresh} size="sm" variant="outline">{busy ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" />}{copy(language, "重新读取", "Retry")}</Button></AlertDescription></Alert> : null}
          {busy && clients.length === 0 ? <div className="flex min-h-28 items-center justify-center gap-2 rounded-2xl border text-sm text-muted-foreground"><Spinner />{copy(language, "正在从节点读取客户端…", "Loading clients from the node…")}</div> : null}
          {!busy && refreshError && clients.length === 0 ? <Alert variant="destructive"><AlertTitle>{copy(language, "无法读取客户端", "Could not load clients")}</AlertTitle><AlertDescription><p>{refreshError}</p><Button className="mt-3" onClick={refresh} size="sm" variant="outline"><RefreshCwIcon data-icon="inline-start" />{copy(language, "重试", "Retry")}</Button></AlertDescription></Alert> : null}
          {inbounds.length === 0 && !busy && !refreshError ? <Alert><AlertTitle>{copy(language, "还没有 VLESS REALITY 入站", "No VLESS REALITY inbound yet")}</AlertTitle><AlertDescription>{copy(language, "先返回应用卡片一键创建入站，再来添加客户端。", "Create a VLESS REALITY inbound from the app card first, then add clients here.")}</AlertDescription></Alert> : null}

          {!busy && clients.length === 0 && !refreshError ? <Empty className="border"><EmptyHeader><EmptyMedia variant="icon"><UsersIcon /></EmptyMedia><EmptyTitle>{copy(language, "还没有客户端", "No clients yet")}</EmptyTitle><EmptyDescription>{copy(language, "为手机、电脑或路由器各创建一个客户端，便于单独停用和查看流量。", "Create one client per phone, computer, or router so each can be disabled and tracked separately.")}</EmptyDescription>{inbounds.length ? <Button className="mt-3" onClick={() => openEditor({})} size="sm"><PlusIcon data-icon="inline-start" />{copy(language, "添加第一个客户端", "Add first client")}</Button> : null}</EmptyHeader></Empty> : null}

          <div className="grid gap-3">
            {visibleClients.map((client) => {
              const publishedInbound = inbounds.find((inbound) => inbound.connectHostname && client.inboundIds.includes(inbound.id));
				const inboundNames = inbounds.filter((inbound) => client.inboundIds.includes(inbound.id)).map((inbound) => inbound.displayName || inbound.nodeName || inbound.name);
              return <div className="rounded-2xl border bg-card p-4" key={client.email}>
                <div className="flex flex-wrap items-start gap-3">
                  <div className="min-w-0 flex-1"><div className="flex flex-wrap items-center gap-2"><h3 className="truncate font-medium">{client.email}</h3><Badge variant={client.enabled ? "secondary" : "outline"}>{client.enabled ? copy(language, "已启用", "Enabled") : copy(language, "已停用", "Disabled")}</Badge></div><p className="mt-1 text-xs text-muted-foreground">{inboundNames.length ? copy(language, `已接入 ${inboundNames.length} 个节点：${inboundNames.join("、")}`, `Connected to ${inboundNames.length} node(s): ${inboundNames.join(", ")}`) : copy(language, "未连接节点", "No node attached")}</p></div>
                  <Switch aria-label={copy(language, `启用 ${client.email}`, `Enable ${client.email}`)} checked={client.enabled} disabled={busy} onCheckedChange={(enabled) => void run({ action: "set_enabled", email: client.email, enabled }).catch(() => undefined)} />
                </div>
                <div className="mt-4 grid gap-3 text-xs sm:grid-cols-4"><Metric label={copy(language, "全节点已用", "Used across all nodes")} value={`${formatBytes(client.usedBytes)}${client.totalBytes ? ` / ${formatBytes(client.totalBytes)}` : ""}`} /><Metric label={copy(language, "有效期", "Expires")} value={formatExpiry(client.expiryTime, language, siteTimezone)} /><Metric label={copy(language, "自动续期", "Auto-renewal")} value={client.resetDays ? copy(language, `每 ${client.resetDays} 天`, `Every ${client.resetDays} days`) : copy(language, "关闭", "Off")} /><Metric label={copy(language, "设备数限制", "IP limit")} value={client.limitIp ? String(client.limitIp) : copy(language, "不限", "Unlimited")} /></div>
	                {resetClient?.email === client.email ? <Alert className="mt-4"><RotateCcwIcon /><AlertTitle>{copy(language, `重置“${client.email}”的订阅用量？`, `Reset subscription usage for “${client.email}”?`)}</AlertTitle><AlertDescription><p>{copy(language, "这会把该客户端在所有 VLESS 节点上的合计用量清零，相当于立即开始一个新套餐周期，且不能撤销。", "This clears the client's combined usage across every VLESS node, immediately starting a new allowance cycle. It cannot be undone.")}</p><div className="mt-3 flex gap-2"><Button disabled={busy} onClick={() => setResetClient(null)} size="sm" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy} onClick={() => void run({ action: "reset_traffic", email: client.email }).then(() => setResetClient(null)).catch(() => undefined)} size="sm">{copy(language, "确认重置", "Reset usage")}</Button></div></AlertDescription></Alert> : null}
                {deleteClient?.email === client.email ? <Alert className="mt-4" variant="destructive"><Trash2Icon /><AlertTitle>{copy(language, `删除“${client.email}”？`, `Delete “${client.email}”?`)}</AlertTitle><AlertDescription><p>{copy(language, "该客户端会立即无法连接，此操作不能撤销。", "This client will stop connecting immediately. This cannot be undone.")}</p><div className="mt-3 flex gap-2"><Button disabled={busy} onClick={() => setDeleteClient(null)} size="sm" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy} onClick={() => void run({ action: "delete", email: client.email }).then(() => setDeleteClient(null)).catch(() => undefined)} size="sm" variant="destructive">{copy(language, "确认删除", "Delete client")}</Button></div></AlertDescription></Alert> : <div className="mt-4 flex flex-wrap gap-2"><Button disabled={busy || !publishedInbound} onClick={() => void reveal(client, "reveal_link")} size="sm" variant="outline"><LinkIcon data-icon="inline-start" />{copy(language, "复制 VLESS", "Copy VLESS")}</Button><Button disabled={busy || !command?.subscriptionAvailable} onClick={() => void reveal(client, "reveal_subscription")} size="sm" variant="outline"><LinkIcon data-icon="inline-start" />{copy(language, "复制订阅", "Copy subscription")}</Button><Button disabled={busy} onClick={() => openEditor({ client })} size="icon-sm" title={copy(language, "编辑", "Edit")} variant="ghost"><PencilIcon /><span className="sr-only">{copy(language, "编辑", "Edit")}</span></Button><Button disabled={busy} onClick={() => { setDeleteClient(null); setResetClient(client); }} size="icon-sm" title={copy(language, "重置流量", "Reset traffic")} variant="ghost"><RotateCcwIcon /><span className="sr-only">{copy(language, "重置流量", "Reset traffic")}</span></Button><Button disabled={busy} onClick={() => { setResetClient(null); setDeleteClient(client); }} size="icon-sm" title={copy(language, "删除", "Delete")} variant="ghost"><Trash2Icon /><span className="sr-only">{copy(language, "删除", "Delete")}</span></Button></div>}
                {!publishedInbound ? <p className="mt-2 text-xs text-muted-foreground">{copy(language, "为入站完成公网发布后才能导出 VLESS 链接。", "Publish the inbound before exporting a VLESS URL.")}</p> : null}
                {!command?.subscriptionAvailable ? <p className="mt-1 text-xs text-muted-foreground">{copy(language, "开启公网订阅后才能复制订阅地址。", "Enable public subscription before copying a subscription URL.")}</p> : null}
                {command?.subscriptionAvailable ? <p className="mt-1 text-xs text-muted-foreground">{copy(language, "同一地址会自动适配 OpenClash、Mihomo 和其他客户端。", "The same URL automatically adapts to OpenClash, Mihomo, and other clients.")}</p> : null}
              </div>;
            })}
          </div>
          {pageCount > 1 ? <div className="flex items-center justify-between"><Button disabled={page <= 1} onClick={() => setPage((value) => Math.max(1, value - 1))} size="sm" variant="outline">{copy(language, "上一页", "Previous")}</Button><span className="text-xs tabular-nums text-muted-foreground">{Math.min(page, pageCount)} / {pageCount}</span><Button disabled={page >= pageCount} onClick={() => setPage((value) => Math.min(pageCount, value + 1))} size="sm" variant="outline">{copy(language, "下一页", "Next")}</Button></div> : null}
        </div>
        <SheetFooter><Button onClick={requestClose} variant="outline">{copy(language, "关闭", "Close")}</Button></SheetFooter>
      </>}
    </SheetContent>
  </Sheet>;
}

function ClientEditor({ busy, editor, inbounds, language, onCancel, onDirtyChange, onSave, siteTimezone }: { busy: boolean; editor: NonNullable<Editor>; inbounds: ThreeXUIClientInbound[]; language: Language; onCancel: () => void; onDirtyChange: (dirty: boolean) => void; onSave: (input: Omit<ThreeXUIClientCommandInput, "applicationId">) => Promise<void>; siteTimezone?: string }) {
  const client = editor.client;
  const initial = useRef(clientEditorDraft(client, inbounds, siteTimezone));
  const [name, setName] = useState(initial.current.name);
  const [inboundIDs, setInboundIDs] = useState<number[]>(initial.current.inboundIDs);
  const [quota, setQuota] = useState(initial.current.quota);
  const [resetDays, setResetDays] = useState(initial.current.resetDays);
  const [expiry, setExpiry] = useState(initial.current.expiry);
  const [limitIP, setLimitIP] = useState(initial.current.limitIP);
  const [enabled, setEnabled] = useState(initial.current.enabled);
  const [error, setError] = useState("");
  const dirty = name !== initial.current.name || !sameSelection(inboundIDs, initial.current.inboundIDs) || quota !== initial.current.quota || resetDays !== initial.current.resetDays || expiry !== initial.current.expiry || limitIP !== initial.current.limitIP || enabled !== initial.current.enabled;
  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange]);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError("");
    const totalBytes = bytesFromGB(quota);
    const renewalDays = Number(resetDays || 0);
    const expiryTime = expiry ? endOfDayEpochInTimeZone(expiry, siteTimezone) : 0;
    const limit = limitIP ? Number(limitIP) : 0;
    if (!name.trim() || !Number.isFinite(totalBytes) || totalBytes < 0 || !Number.isInteger(renewalDays) || renewalDays < 0 || renewalDays > 0 && !expiryTime || !Number.isInteger(limit) || limit < 0) {
      setError(copy(language, "请检查名称、流量和设备数。", "Check the name, quota, and IP limit."));
      return;
    }
    try {
      await onSave(client ? { action: "update", email: client.email, newEmail: name.trim(), inboundIds: inboundIDs, totalBytes, resetDays: renewalDays, expiryTime, limitIp: limit } : { action: "create", newEmail: name.trim(), inboundIds: inboundIDs, enabled, totalBytes, resetDays: renewalDays, expiryTime, limitIp: limit });
    } catch (saveError) {
      setError(readableError(language, saveError));
    }
  };
  return <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}>
    <div className="flex-1 overflow-y-auto px-4">
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor="three-xui-client-name">{copy(language, "名称", "Name")}</FieldLabel>
          <Input autoFocus id="three-xui-client-name" maxLength={64} onChange={(event) => setName(event.target.value)} placeholder={copy(language, "例如：我的 MacBook", "For example: My MacBook")} required value={name} />
          <FieldDescription>{copy(language, "这是设备名称，不需要填写真实邮箱。", "This is a device label, not a real email address.")}</FieldDescription>
        </Field>
        <ClientNodePicker busy={busy} inbounds={inbounds} language={language} onChange={setInboundIDs} selected={inboundIDs} />
        <SubscriptionTrafficPlanFields expiry={expiry} idPrefix="three-x-ui-client" language={language} minimumDate={dateInputValueInTimeZone(new Date(), siteTimezone)} onExpiryChange={setExpiry} onQuotaChange={setQuota} onResetDaysChange={(value) => { setResetDays(value); if (!client || Number(value) > 0) setExpiry(nextRenewalDateInTimeZone(Number(value), siteTimezone)); }} quota={quota} resetDays={resetDays} />
        <Field>
          <FieldLabel htmlFor="three-xui-client-limit-ip">{copy(language, "同时使用的设备数", "Simultaneous devices")}</FieldLabel>
          <Input id="three-xui-client-limit-ip" min="0" onChange={(event) => setLimitIP(event.target.value)} placeholder={copy(language, "留空表示不限", "Leave empty for unlimited")} step="1" type="number" value={limitIP} />
        </Field>
        {!client ? <Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="three-xui-client-enabled">{copy(language, "创建后立即启用", "Enable after creation")}</FieldLabel><FieldDescription>{copy(language, "关闭时会保存客户端，但暂时不能连接。", "When off, the client is saved but cannot connect yet.")}</FieldDescription></div><Switch checked={enabled} id="three-xui-client-enabled" onCheckedChange={setEnabled} /></Field> : null}
        {error ? <FieldError role="alert">{error}</FieldError> : null}
      </FieldGroup>
    </div>
    <SheetFooter><Button disabled={busy} onClick={onCancel} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !dirty || !name.trim() || inboundIDs.length === 0} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{client ? copy(language, "保存修改", "Save changes") : copy(language, "添加客户端", "Add client")}</Button></SheetFooter>
  </form>;
}

function ClientNodePicker({ busy, inbounds, language, onChange, selected }: { busy: boolean; inbounds: ThreeXUIClientInbound[]; language: Language; onChange: (ids: number[]) => void; selected: number[] }) {
  const toggle = (id: number) => onChange(selected.includes(id) ? selected.filter((value) => value !== id) : [...selected, id]);
  return <fieldset className="grid gap-3">
    <legend className="text-sm font-medium">{copy(language, "使用哪些节点", "Nodes to use")}</legend>
    <div className="flex items-start justify-between gap-3">
      <p className="text-sm text-muted-foreground">{copy(language, "默认使用全部节点；以后添加的新入站也会自动同步现有客户端。", "All nodes are selected by default. New inbounds also receive existing clients automatically.")}</p>
      <Button disabled={busy || selected.length === inbounds.length} onClick={() => onChange(inbounds.map((inbound) => inbound.id))} size="sm" type="button" variant="ghost">{copy(language, "全选", "Select all")}</Button>
    </div>
    <div className="grid gap-2 sm:grid-cols-2">
      {inbounds.map((inbound) => {
        const checked = selected.includes(inbound.id);
        return <button aria-checked={checked} className={`flex min-h-14 items-center gap-3 rounded-xl border p-3 text-left transition-colors ${checked ? "border-primary bg-primary/5" : "bg-card hover:bg-accent"}`} disabled={busy} key={inbound.id} onClick={() => toggle(inbound.id)} role="checkbox" type="button">
          <span className={`flex size-9 shrink-0 items-center justify-center rounded-full ${checked ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground"}`}>{checked ? <CheckIcon className="size-4" /> : <ServerIcon className="size-4" />}</span>
			<span className="min-w-0 flex-1"><span className="block truncate text-sm font-medium">{inbound.displayName || inbound.nodeName || inbound.name}</span><span className="mt-0.5 block truncate text-xs text-muted-foreground">{inbound.connectHostname || inbound.name}</span></span>
        </button>;
      })}
    </div>
    <p aria-live="polite" className="text-xs text-muted-foreground">{copy(language, `已选择 ${selected.length} / ${inbounds.length} 个节点`, `${selected.length} of ${inbounds.length} nodes selected`)}</p>
  </fieldset>;
}

function clientEditorDraft(client: ThreeXUIClient | undefined, inbounds: ThreeXUIClientInbound[], siteTimezone?: string) {
  return {
    name: client?.email ?? "",
    inboundIDs: client ? client.inboundIds.filter((id) => inbounds.some((inbound) => inbound.id === id)) : inbounds.map((inbound) => inbound.id),
    quota: gigabytesFromBytes(client?.totalBytes),
    resetDays: String(client?.resetDays || 0),
    expiry: client?.expiryTime ? dateInputValueInTimeZone(client.expiryTime, siteTimezone) : "",
    limitIP: client?.limitIp ? String(client.limitIp) : "",
    enabled: client?.enabled ?? true
  };
}

function sameSelection(left: number[], right: number[]) {
  return left.length === right.length && left.every((id) => right.includes(id));
}

function Metric({ label, value }: { label: string; value: string }) { return <div><p className="text-muted-foreground">{label}</p><p className="mt-1 font-medium tabular-nums">{value}</p></div>; }

function formatExpiry(value: number, language: Language, timeZone?: string) {
  if (!value) return copy(language, "永不过期", "Never");
  try {
    return new Intl.DateTimeFormat(language, { dateStyle: "medium", timeZone: timeZone || "UTC" }).format(value);
  } catch {
    return new Intl.DateTimeFormat(language, { dateStyle: "medium", timeZone: "UTC" }).format(value);
  }
}
function readableError(language: Language, error: unknown) {
  if (!(error instanceof Error) || !error.message) return copy(language, "操作失败，请稍后重试。", "Operation failed. Try again shortly.");
  const normalized = error.message.toLowerCase();
  if (normalized.includes("session expired") || normalized.includes("live connection") || normalized.includes("did not respond in time")) return userError(language, error);
  return error.message.replace(/^center:\s*/i, "");
}
