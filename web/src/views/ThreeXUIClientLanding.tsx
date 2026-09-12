import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { ArrowLeftIcon, RefreshCwIcon } from "lucide-react";
import { api } from "../api";
import type { LandingClientGrant, LandingClientMode, LandingPublishingMode, LandingView } from "../landing-types";
import type { ThreeXUIClient, ThreeXUIClientInbound } from "../types";
import type { Language } from "../translations";
import { copy, userError } from "./shared";
import { SelectControl } from "../components/SelectControl";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Separator } from "@/components/ui/separator";
import { Spinner } from "@/components/ui/spinner";

const pendingGrant = (grant: LandingClientGrant) => !["ready", "paused", "revoked", "failed"].includes(grant.status);

export function ThreeXUIClientLanding({ client, inbounds, language, onClose }: { client: ThreeXUIClient; inbounds: ThreeXUIClientInbound[]; language: Language; onClose: () => void }) {
  const [grants, setGrants] = useState<LandingClientGrant[]>([]);
  const [landing, setLanding] = useState<LandingView | null>(null);
  const [published, setPublished] = useState<LandingClientMode | null>(null);
  const [mode, setMode] = useState<LandingPublishingMode>("fixed");
  const [grantMode, setGrantMode] = useState<LandingPublishingMode>("fixed");
  const [serviceId, setServiceId] = useState("");
  const [landingNodeId, setLandingNodeId] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const errorRef = useRef<HTMLDivElement>(null);
  const entries = inbounds.filter((entry) => entry.serviceId && !entry.vlessDisabled && client.inboundIds.includes(entry.id));
  const entry = entries.find((value) => value.serviceId === serviceId);
  const existing = grants.find((grant) => grant.serviceId === serviceId && grant.landingNodeId === landingNodeId);
  const applying = grants.some(pendingGrant);
  const choices = [
    { value: "fixed", label: copy(language, "组合节点", "Fixed nodes") },
    { value: "advanced", label: copy(language, "自选策略", "Client strategies") },
    { value: "both", label: copy(language, "同时提供", "Both") },
  ];
  const load = useCallback(async (signal?: AbortSignal, initialize = false) => {
    if (!client.id) throw new Error(copy(language, "请先刷新客户端列表。", "Refresh the client list first."));
    const [nextGrants, nextLanding, nextMode] = await Promise.all([api.clientLandingGrants(client.id, signal), api.landing(signal), api.clientLandingMode(client.id, signal)]);
    if (signal?.aborted) return;
    setGrants(nextGrants); setLanding(nextLanding); setPublished(nextMode);
    if (initialize) setMode(nextMode.mode);
  }, [client.id, language]);
  useEffect(() => {
    const abort = new AbortController();
    void load(abort.signal, true).catch((reason) => { if (!abort.signal.aborted) setError(userError(language, reason)); }).finally(() => { if (!abort.signal.aborted) setLoading(false); });
    return () => abort.abort();
  }, [load, language]);
  useEffect(() => {
    if (!applying || busy) return;
    const abort = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const refresh = async () => {
      try { await load(abort.signal); } catch (reason) { if (!abort.signal.aborted) setError(userError(language, reason)); }
      if (!abort.signal.aborted) timer = setTimeout(refresh, 3000);
    };
    timer = setTimeout(refresh, 3000);
    return () => { abort.abort(); clearTimeout(timer); };
  }, [applying, busy, language, load]);
  const confirm = (names: string[]) => window.confirm(copy(language,
    `将调整以下入口：${[...new Set(names)].join("、")}，可能短暂中断这些入口上的连接。是否继续？`,
    `Update these entries: ${[...new Set(names)].join(", ")}. Connections on these instances may be briefly interrupted. Continue?`));
  const execute = async (operation: () => Promise<unknown>, changed = true) => {
    setBusy(true); setError(""); setNotice("");
    try {
      await operation();
      if (changed) {
        await load();
        setNotice(copy(language, "更改已提交，完成后会自动生效。", "Changes submitted. They will take effect when ready."));
      }
    } catch (reason) {
      setError(userError(language, reason));
      requestAnimationFrame(() => errorRef.current?.focus());
    } finally { setBusy(false); }
  };
  const configure = (event: FormEvent) => {
    event.preventDefault();
    if (!entry || !client.id || !landingNodeId || !confirm([entry.nodeName || entry.name])) return;
    void execute(() => api.configureClientLanding({ parentId: client.id!, serviceId, landingNodeId, mode: grantMode, enabled: true, revision: existing?.revision ?? 0, confirmSessionReset: true }));
  };
  const grantName = (grant: LandingClientGrant) => inbounds.find((value) => value.serviceId === grant.serviceId)?.nodeName || copy(language, "已移除入口", "Removed entry");
  return <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 pb-4">
    <div className="flex items-center gap-2">
      <Button disabled={busy} onClick={onClose} size="sm" variant="ghost"><ArrowLeftIcon aria-hidden="true" data-icon="inline-start" />{copy(language, "返回客户端", "Back to clients")}</Button>
      <h3 className="min-w-0 flex-1 truncate text-sm font-medium">{client.email}</h3>
      <Button aria-label={copy(language, "刷新落地授权", "Refresh landing grants")} disabled={busy || loading} onClick={() => void execute(() => load(), false)} size="icon-sm" variant="ghost"><RefreshCwIcon /></Button>
    </div>
    {error ? <Alert ref={errorRef} tabIndex={-1} variant="destructive"><AlertTitle>{copy(language, "未能完成操作", "Could not complete the operation")}</AlertTitle><AlertDescription>{error}</AlertDescription></Alert> : null}
    {notice ? <p aria-live="polite" className="text-sm text-muted-foreground">{notice}</p> : null}
    {loading ? <p aria-live="polite" className="flex items-center gap-2 text-sm text-muted-foreground"><Spinner />{copy(language, "正在读取…", "Loading…")}</p> : <>
      <FieldGroup>
        <Field data-disabled={busy || applying || !published}>
          <FieldLabel htmlFor="client-landing-publishing">{copy(language, "订阅提供方式", "Subscription publishing")}</FieldLabel>
          <SelectControl disabled={busy || applying || !published} id="client-landing-publishing" onValueChange={(value) => setMode(value as LandingPublishingMode)} options={choices} value={mode} />
          <FieldDescription>{copy(language, "原订阅地址和原节点保留。自选策略需使用 Mihomo / OpenClash；新增落地仅支持 TCP。", "Keep the current subscription URL and original nodes. Client strategies require Mihomo / OpenClash. Added landing paths support TCP only.")}</FieldDescription>
          {published && mode !== published.mode ? <Button disabled={busy || applying} onClick={() => {
            const affected = grants.filter((grant) => grant.enabled && grant.status !== "revoked").map(grantName);
            if (affected.length && !confirm(affected)) return;
            void execute(() => api.configureClientLandingMode({ ...published, mode, confirmSessionReset: true }));
          }} size="sm">{copy(language, "保存提供方式", "Save publishing mode")}</Button> : null}
        </Field>
      </FieldGroup>
      <Separator />
      <form onSubmit={configure}>
        <FieldGroup>
          <Field data-disabled={busy || applying}>
            <FieldLabel htmlFor="client-landing-entry">{copy(language, "入口节点", "Entry node")}</FieldLabel>
            <SelectControl disabled={busy || applying} id="client-landing-entry" onValueChange={(value) => { setServiceId(value); setLandingNodeId(""); }} options={entries.map((value) => ({ value: value.serviceId!, label: value.displayName || value.nodeName || value.name }))} placeholder={copy(language, "选择已接入的节点", "Choose an attached node")} required value={serviceId} />
          </Field>
          <Field data-disabled={busy || applying || !entry}>
            <FieldLabel htmlFor="client-landing-target">{copy(language, "落地机", "Landing server")}</FieldLabel>
            <SelectControl disabled={busy || applying || !entry} id="client-landing-target" onValueChange={(value) => { setLandingNodeId(value); setGrantMode(grants.find((grant) => grant.serviceId === serviceId && grant.landingNodeId === value)?.mode ?? "fixed"); }} options={(landing?.servers ?? []).filter((server) => server.nodeId !== entry?.nodeId && server.status !== "stopped").map((server) => ({ value: server.nodeId, label: server.name, disabled: server.status !== "ready" }))} placeholder={copy(language, "选择落地机", "Choose a landing server")} required value={landingNodeId} />
          </Field>
          <Field data-disabled={busy || applying}>
            <FieldLabel htmlFor="client-landing-access">{copy(language, "允许的使用方式", "Allowed modes")}</FieldLabel>
            <SelectControl disabled={busy || applying} id="client-landing-access" onValueChange={(value) => setGrantMode(value as LandingPublishingMode)} options={choices} value={grantMode} />
            <FieldDescription>{copy(language, "只授权当前客户端，不改变入口原来的默认出口。订阅只提供与上方提供方式相符的授权。", "Authorize this client only, without changing the entry's default exit. Published choices are limited by the publishing mode above.")}</FieldDescription>
          </Field>
          <Button disabled={busy || applying || !published || !entry || !landingNodeId || !client.enabled} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{existing?.status !== "revoked" && existing ? copy(language, "更新授权", "Update grant") : copy(language, "添加落地组合", "Add landing combination")}</Button>
        </FieldGroup>
      </form>
      {grants.filter((grant) => grant.status !== "revoked").length ? <ul className="flex flex-col gap-3" aria-label={copy(language, "已授权的落地", "Landing grants")}>
        {grants.filter((grant) => grant.status !== "revoked").map((grant) => <li className="flex flex-wrap items-center gap-2 rounded-lg border p-3" key={grant.id}>
          <div className="min-w-0 flex-1"><p className="break-words text-sm">{grantName(grant)} → {landing?.servers.find((server) => server.nodeId === grant.landingNodeId)?.name || copy(language, "已移除落地机", "Removed landing")}</p><p className="mt-1 text-xs text-muted-foreground">{choices.find((choice) => choice.value === grant.mode)?.label}</p></div>
          <Badge variant={grant.status === "failed" ? "destructive" : "secondary"}>{pendingGrant(grant) ? copy(language, "正在调整", "Updating") : grant.status === "failed" ? copy(language, "调整失败", "Failed") : grant.status === "paused" ? copy(language, "已停用", "Paused") : copy(language, "已授权", "Authorized")}</Badge>
          <Button disabled={busy || applying} onClick={() => {
            if (!confirm([grantName(grant)])) return;
            void execute(() => api.configureClientLanding({ parentId: grant.parentId, serviceId: grant.serviceId, landingNodeId: grant.landingNodeId, mode: grant.mode, enabled: false, revision: grant.revision, confirmSessionReset: true }));
          }} size="sm" variant="ghost">{copy(language, "撤销", "Revoke")}</Button>
        </li>)}
      </ul> : <Empty><EmptyHeader><EmptyTitle>{copy(language, "尚未授权落地", "No landing grants")}</EmptyTitle><EmptyDescription>{copy(language, "添加后，当前客户端即可获得相应的落地选项。", "Add a grant to make its landing choices available to this client.")}</EmptyDescription></EmptyHeader></Empty>}
    </>}
  </div>;
}
