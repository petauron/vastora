import { useEffect, useState } from "react";
import { ArrowRightIcon, CheckCircle2Icon, CircleAlertIcon, CloudIcon, Globe2Icon, RefreshCwIcon, ShieldAlertIcon } from "lucide-react";
import { api } from "../api";
import type { CloudflareZone, SystemDomain } from "../types";
import type { Language } from "../translations";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { SelectControl } from "@/components/SelectControl";
import { copy, userError } from "./shared";

export function SystemDomainSettings({ domain, language }: { domain: SystemDomain; language: Language }) {
  const [open, setOpen] = useState(false);
  const blocked = !domain.builtinHeadscale || domain.activePublications > 0 || domain.pendingCleanup > 0;
  return <>
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2"><Globe2Icon aria-hidden="true" />{copy(language, "Vastora 域名", "Vastora domain")}</CardTitle>
        <CardDescription>{copy(language, "Center、Headscale 和应用入口使用同一域名空间。", "Center, Headscale, and app access points share one domain namespace.")}</CardDescription>
        <CardAction><Button disabled={blocked} onClick={() => setOpen(true)} size="sm" variant="outline">{copy(language, "切换域名", "Switch domain")}</Button></CardAction>
      </CardHeader>
      <CardContent className="grid gap-4 text-sm sm:grid-cols-3">
        <DomainValue label={copy(language, "域名空间", "Namespace")} value={domain.namespace || "—"} />
        <DomainValue label="Center" value={domain.centerUrl} />
        <DomainValue label="Headscale" value={domain.headscaleUrl || "—"} />
        {blocked ? <p className="sm:col-span-3 text-sm text-amber-600 dark:text-amber-400">{!domain.builtinHeadscale ? copy(language, "外部 Headscale 暂不支持自动迁移域名。", "Automatic domain migration is not available with external Headscale.") : domain.activePublications > 0 ? copy(language, `请先停止 ${domain.activePublications} 个访问入口。`, `Stop ${domain.activePublications} access point(s) first.`) : copy(language, `请等待 ${domain.pendingCleanup} 个入口完成清理。`, `Wait for ${domain.pendingCleanup} access point cleanup(s).`)}</p> : null}
        {domain.aliases.length > 0 ? <p className="sm:col-span-3 text-xs text-muted-foreground">{copy(language, `仍保留 ${domain.aliases.length} 个旧系统地址作为迁移过渡。`, `${domain.aliases.length} previous system address(es) remain available during transition.`)}</p> : null}
      </CardContent>
    </Card>
    <DomainSwitchSheet domain={domain} language={language} onClose={() => setOpen(false)} open={open} />
  </>;
}

function DomainValue({ label, value }: { label: string; value: string }) {
  return <div className="min-w-0"><p className="text-muted-foreground">{label}</p><p className="mt-1 truncate font-medium" title={value}>{value}</p></div>;
}

function DomainSwitchSheet({ domain, language, onClose, open }: { domain: SystemDomain; language: Language; onClose: () => void; open: boolean }) {
  const [zones, setZones] = useState<CloudflareZone[]>([]);
  const [zoneID, setZoneID] = useState("");
  const [loadingZones, setLoadingZones] = useState(false);
  const [zoneError, setZoneError] = useState("");
  const [reloadZones, setReloadZones] = useState(0);
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const zone = zones.find((candidate) => candidate.id === zoneID) ?? null;
  const namespace = zone ? `vastora.${zone.name}` : "";

  useEffect(() => {
    if (!open) return;
    let active = true;
    setLoadingZones(true);
    setZoneError("");
    setZones([]);
    setZoneID("");
    setConfirmed(false);
    setError("");
    void api.cloudflareZones().then(({ zones: available }) => {
      if (!active) return;
      const alternatives = available.filter((candidate) => candidate.name !== domain.cloudflareZone);
      setZones(alternatives);
      setZoneID(alternatives[0]?.id ?? "");
    }).catch((loadError) => {
      if (active) setZoneError(loadError instanceof Error ? loadError.message : "Request failed");
    }).finally(() => {
      if (active) setLoadingZones(false);
    });
    return () => { active = false; };
  }, [domain.cloudflareZone, open, reloadZones]);

  const resetAndClose = () => {
    if (busy) return;
    setZones([]); setZoneID(""); setConfirmed(false); setError(""); setZoneError(""); onClose();
  };
  const submit = async () => {
    if (!zone || !confirmed) return;
    setBusy(true); setError("");
    try {
      const result = await api.switchSystemDomain(zone.id);
      window.location.assign(new URL("/settings", result.centerUrl));
    } catch (switchError) {
      setError(userError(language, switchError));
      setBusy(false);
    }
  };
  return <Sheet onOpenChange={(next) => { if (!next) resetAndClose(); }} open={open}>
    <SheetContent className="sm:max-w-xl">
      <SheetHeader><SheetTitle>{copy(language, "切换 Vastora 域名", "Switch Vastora domain")}</SheetTitle><SheetDescription>{copy(language, "一次迁移 Center、Headscale 和默认应用域名空间。", "Move Center, Headscale, and the default app namespace together.")}</SheetDescription></SheetHeader>
      <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 pb-4">
        <Alert><ShieldAlertIcon aria-hidden="true" /><AlertTitle>{copy(language, "旧地址不会立即失效", "Previous addresses remain available")}</AlertTitle><AlertDescription>{copy(language, "Vastora 会先创建数据库快照，再启用新域名。在线节点会在下一次心跳验证新地址后自动切换；离线节点仍可通过旧地址恢复。应用访问入口需在切换后重新创建。", "Vastora creates a database snapshot before enabling the new domain. Online nodes verify and switch to the new address on their next heartbeat; offline nodes can still recover through the previous address. Recreate app access points after the switch.")}</AlertDescription></Alert>
        <div className="rounded-2xl border bg-background p-4 shadow-xs sm:p-5">
          <div className="flex items-start gap-3"><span className="grid size-11 shrink-0 place-items-center rounded-xl bg-orange-500/10 text-orange-600 dark:text-orange-400"><CloudIcon aria-hidden="true" className="size-6" /></span><div><div className="flex items-center gap-2 font-medium">{copy(language, "使用现有 Cloudflare 授权", "Use the saved Cloudflare authorization")}<CheckCircle2Icon aria-label={copy(language, "已连接", "Connected")} className="size-4 text-emerald-500" /></div><p className="mt-1 text-sm text-muted-foreground">{copy(language, `当前连接 ${domain.cloudflareZone || "Cloudflare"}，切换域名无需重新登录。`, `Connected to ${domain.cloudflareZone || "Cloudflare"}. Switching domains does not require signing in again.`)}</p></div></div>
          {loadingZones ? <div className="mt-4 flex items-center gap-2 border-t pt-4 text-sm text-muted-foreground"><Spinner />{copy(language, "正在读取可用域名…", "Loading available domains…")}</div> : null}
          {!loadingZones && zoneError ? <Alert className="mt-4" variant="destructive"><CircleAlertIcon /><AlertTitle>{copy(language, "无法读取已有授权", "Could not use the saved authorization")}</AlertTitle><AlertDescription><p>{userError(language, zoneError)}</p><div className="mt-3 flex flex-wrap gap-2"><Button onClick={() => setReloadZones((value) => value + 1)} size="sm" type="button" variant="outline"><RefreshCwIcon data-icon="inline-start" />{copy(language, "重试", "Retry")}</Button><Button onClick={() => window.location.assign("/network")} size="sm" type="button" variant="outline">{copy(language, "管理 Cloudflare 连接", "Manage Cloudflare connection")}</Button></div></AlertDescription></Alert> : null}
          {!loadingZones && !zoneError && zones.length === 0 ? <Alert className="mt-4"><CircleAlertIcon /><AlertTitle>{copy(language, "没有其他可用域名", "No other domains are available")}</AlertTitle><AlertDescription>{copy(language, "当前授权中只有正在使用的域名。新域名加入这个 Cloudflare 账号后，点击重试即可。", "The saved authorization only exposes the domain already in use. Add the new domain to this Cloudflare account, then retry.")}<Button className="mt-3" onClick={() => setReloadZones((value) => value + 1)} size="sm" type="button" variant="outline"><RefreshCwIcon data-icon="inline-start" />{copy(language, "重新读取", "Reload")}</Button></AlertDescription></Alert> : null}
          {!loadingZones && zones.length > 0 ? <Field className="mt-4 border-t pt-4"><FieldLabel htmlFor="system-domain-zone">{copy(language, "选择新域名", "Choose the new domain")}</FieldLabel><SelectControl disabled={busy} id="system-domain-zone" onValueChange={(value) => { setZoneID(value); setConfirmed(false); }} options={zones.map((candidate) => ({ value: candidate.id, label: `${candidate.name}${candidate.accountName ? ` — ${candidate.accountName}` : ""}` }))} value={zoneID} /><FieldDescription>{copy(language, "这里只显示当前授权可以管理的域名，并已隐藏正在使用的域名。", "Only domains available to the saved authorization are shown; the current domain is hidden.")}</FieldDescription></Field> : null}
        </div>
        {zone ? <div className="rounded-2xl border bg-muted/20 p-4"><div className="flex items-center gap-2 font-medium"><CheckCircle2Icon aria-hidden="true" className="size-5 text-emerald-500" />{copy(language, "迁移预览", "Migration preview")}</div><div className="mt-4 grid gap-3 text-sm"><Preview oldValue={domain.centerUrl} value={`https://center.${namespace}`} /><Preview oldValue={domain.headscaleUrl} value={`https://headscale.${namespace}`} /><Preview oldValue={domain.namespace} value={namespace} /></div></div> : null}
        {busy ? <Alert><Spinner /><AlertTitle>{copy(language, "正在安全切换域名", "Switching domain safely")}</AlertTitle><AlertDescription>{copy(language, "正在依次完成备份、DNS、证书和网关更新。证书签发可能需要几分钟，请不要关闭页面。", "Completing backup, DNS, certificate, and gateway updates in order. Certificate issuance can take a few minutes; keep this page open.")}</AlertDescription></Alert> : null}
        {zone ? <Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="confirm-domain-switch">{copy(language, "确认迁移全部系统域名", "Confirm the system-domain migration")}</FieldLabel><FieldDescription>{copy(language, "新建入口将使用新域名空间；旧应用入口不会自动复制。", "New access points use the new namespace; old app access points are not copied automatically.")}</FieldDescription></div><Switch checked={confirmed} disabled={busy} id="confirm-domain-switch" onCheckedChange={setConfirmed} /></Field> : null}
        {error ? <FieldError role="alert">{error}</FieldError> : null}
      </div>
      <SheetFooter><Button disabled={busy} onClick={resetAndClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !zone || !confirmed} onClick={() => void submit()} type="button">{busy ? <Spinner data-icon="inline-start" /> : <ArrowRightIcon data-icon="inline-start" />}{copy(language, "切换域名", "Switch domain")}</Button></SheetFooter>
    </SheetContent>
  </Sheet>;
}

function Preview({ oldValue, value }: { oldValue: string; value: string }) {
  return <div className="grid min-w-0 gap-1 sm:grid-cols-[1fr_auto_1fr] sm:items-center"><span className="truncate text-muted-foreground" title={oldValue}>{oldValue || "—"}</span><ArrowRightIcon aria-hidden="true" className="hidden size-4 text-muted-foreground sm:block" /><span className="truncate font-medium" title={value}>{value}</span></div>;
}
