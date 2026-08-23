import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { GaugeIcon, PencilIcon, RefreshCwIcon, ShieldAlertIcon } from "lucide-react";
import { api } from "../api";
import type { Application, ApplicationCommand, Service, ThreeXUIClientCommandInput } from "../types";
import type { Language } from "../translations";
import { copy } from "./shared";
import { bytesFromGB, dateInputValueInTimeZone, formatBytes, gigabytesFromBytes, InboundTrafficPlanFields } from "./TrafficPlanFields";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { FieldError } from "@/components/ui/field";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { useApplicationCommandExecutor } from "../hooks/use-application-command-executor";

type TrafficDraft = { quota: string; resetDays: string; nextResetAt: string };

export function ThreeXUIInboundTrafficSheet({ controller, service, siteTimezone, language, onClose }: { controller: Application | null; service: Service | null; siteTimezone?: string; language: Language; onClose: () => void }) {
  const [command, setCommand] = useState<ApplicationCommand | null>(null);
  const [editing, setEditing] = useState(false);
  const [quota, setQuota] = useState("");
  const [resetDays, setResetDays] = useState("0");
  const [nextResetAt, setNextResetAt] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const baseline = useRef<TrafficDraft>({ quota: "", resetDays: "0", nextResetAt: "" });
  const { execute, running } = useApplicationCommandExecutor(controller?.id);
  const inbound = command?.inbounds?.find((value) => value.serviceId === service?.id);
  const dirty = editing && (quota !== baseline.current.quota || resetDays !== baseline.current.resetDays);

  const run = useCallback(async (input: Omit<ThreeXUIClientCommandInput, "applicationId">) => {
    if (!controller) throw new Error("Subscription controller is unavailable");
    setError("");
    const next = await execute(
      () => api.createThreeXUIClientCommand({ applicationId: controller.id, ...input }),
      (value) => setCommand((current) => {
        if (!current) return value;
        return {
          ...value,
          inbounds: value.inboundsObserved ? value.inbounds : current.inbounds,
          inboundsObserved: value.inboundsObserved || current.inboundsObserved
        };
      })
    );
    if (next?.state === "failed") throw new Error(next.error || "The 3x-ui operation failed");
    return next;
  }, [controller?.id, execute]);

  useEffect(() => {
    if (!controller || !service) {
      setCommand(null); setEditing(false); setError(""); setNotice("");
      return;
    }
    let cancelled = false;
    void run({ action: "list_inbounds" }).catch((loadError) => {
      if (!cancelled) setError(readableError(language, loadError));
    });
    return () => { cancelled = true; };
  }, [controller?.id, language, run, service?.id]);

  useEffect(() => {
    if (!inbound) return;
    const next = {
      quota: gigabytesFromBytes(inbound.totalBytes),
      resetDays: String(inbound.resetDays || 0),
      nextResetAt: inbound.nextResetAt ? dateInputValueInTimeZone(inbound.nextResetAt, siteTimezone) : ""
    };
    baseline.current = next;
    setQuota(next.quota);
    setResetDays(next.resetDays);
    setNextResetAt(next.nextResetAt);
  }, [inbound?.id, inbound?.nextResetAt, inbound?.resetDays, inbound?.totalBytes, siteTimezone]);

  const restoreDraft = () => {
    setQuota(baseline.current.quota);
    setResetDays(baseline.current.resetDays);
    setNextResetAt(baseline.current.nextResetAt);
  };

  const discardEdit = () => {
    if (dirty && !window.confirm(copy(language, "放弃尚未保存的修改？", "Discard unsaved changes?"))) return false;
    restoreDraft();
    setEditing(false);
    setError("");
    return true;
  };

  const requestClose = () => {
    if (editing && !discardEdit()) return;
    onClose();
  };

  const retryLoad = () => {
    setNotice("");
    void run({ action: "list_inbounds" }).catch((loadError) => setError(readableError(language, loadError)));
  };

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!inbound) return;
    const totalBytes = bytesFromGB(quota);
    const renewalDays = Number(resetDays || 0);
    if (!Number.isFinite(totalBytes) || totalBytes < 0 || !Number.isInteger(renewalDays) || renewalDays < 0) {
      setError(copy(language, "请检查节点流量额度和续期天数。", "Check the node allowance and renewal days."));
      return;
    }
    try {
      await run({ action: "update_inbound", serviceId: service?.id, inboundId: inbound.id, inboundTotalBytes: totalBytes, inboundResetDays: renewalDays });
      setEditing(false);
      setNotice(copy(language, "节点套餐已保存。", "Node plan saved."));
    } catch (saveError) {
      setError(readableError(language, saveError));
    }
  };

  return <Sheet onOpenChange={(open) => { if (!open) requestClose(); }} open={Boolean(controller && service)}>
    <SheetContent className="w-full sm:max-w-lg">
      <SheetHeader>
        <SheetTitle>{copy(language, "VLESS 节点套餐", "VLESS node plan")}</SheetTitle>
        <SheetDescription>{copy(language, `单独管理“${service?.displayName || service?.name || ""}”的流量，不会占用或修改其他节点的套餐。`, `Manage traffic for “${service?.displayName || service?.name || ""}” independently without changing other nodes.`)}</SheetDescription>
      </SheetHeader>
      {running && !inbound ? <div className="flex flex-1 items-center justify-center gap-2 px-4 text-sm text-muted-foreground"><Spinner />{copy(language, "正在读取节点套餐…", "Loading node plan…")}</div> : null}
      {!running && !inbound && !error ? <div className="px-4"><Alert><AlertTitle>{copy(language, "暂时没有读到这个入站", "This inbound is not available yet")}</AlertTitle><AlertDescription><p>{copy(language, "请确认节点已连接到订阅主机，然后重试。", "Confirm the node is connected to the subscription controller, then retry.")}</p><Button className="mt-3" onClick={retryLoad} size="sm" variant="outline"><RefreshCwIcon data-icon="inline-start" />{copy(language, "重新读取", "Retry")}</Button></AlertDescription></Alert></div> : null}
      {inbound && !editing ? <div className="flex flex-1 flex-col gap-4 px-4">
        <div className="rounded-2xl border bg-muted/20 p-4">
          <div className="flex items-start gap-3"><span className="flex size-10 shrink-0 items-center justify-center rounded-full bg-primary/10 text-primary"><GaugeIcon aria-hidden="true" className="size-5" /></span><div className="min-w-0"><p className="font-medium">{inbound.displayName || inbound.nodeName || service?.displayName || inbound.name}</p><p className="mt-1 text-sm text-muted-foreground">{copy(language, "此节点单独计量", "Metered independently")}</p></div></div>
          <dl className="mt-5 grid grid-cols-2 gap-4 text-sm sm:grid-cols-3"><div><dt className="text-muted-foreground">{copy(language, "已用流量", "Used")}</dt><dd className="mt-1 font-medium tabular-nums">{formatBytes(inbound.usedBytes)}</dd></div><div><dt className="text-muted-foreground">{copy(language, "套餐总量", "Allowance")}</dt><dd className="mt-1 font-medium tabular-nums">{inbound.totalBytes ? formatBytes(inbound.totalBytes) : copy(language, "不限", "Unlimited")}</dd></div><div><dt className="text-muted-foreground">{copy(language, "下次续期", "Next renewal")}</dt><dd className="mt-1 font-medium tabular-nums">{inbound.resetDays && inbound.nextResetAt ? formatDate(inbound.nextResetAt, language, siteTimezone) : copy(language, "不自动续期", "No auto-renewal")}</dd></div></dl>
        </div>
        {inbound.planStatus === "resetting" ? <Alert aria-live="polite"><Spinner /><AlertTitle>{copy(language, "正在续期节点套餐", "Renewing the node plan")}</AlertTitle><AlertDescription>{copy(language, "Center 正在只清零这个 VLESS 入站的用量；完成前请不要重复修改。执行过程会记录在“活动”中。", "Center is clearing usage for this VLESS inbound only. Do not change it again until completion; progress is recorded in Activity.")}</AlertDescription></Alert> : null}
        {inbound.planStatus === "failed" ? <Alert variant="destructive"><ShieldAlertIcon aria-hidden="true" /><AlertTitle>{copy(language, "节点套餐续期失败", "Node plan renewal failed")}</AlertTitle><AlertDescription><p>{inbound.planError || copy(language, "Center 无法确认 3x-ui 已完成流量重置。", "Center could not confirm that 3x-ui completed the traffic reset.")}</p><Button className="mt-3" nativeButton={false} render={<a href="/activity" />} size="sm" variant="outline">{copy(language, "前往活动查看详情", "Open Activity for details")}</Button></AlertDescription></Alert> : null}
        {notice ? <p aria-live="polite" className="text-sm text-muted-foreground">{notice}</p> : null}
        <Button className="w-fit" disabled={inbound.planStatus === "resetting"} onClick={() => { setNotice(""); setEditing(true); }} size="sm"><PencilIcon data-icon="inline-start" />{copy(language, "修改节点套餐", "Edit node plan")}</Button>
      </div> : null}
      {inbound && editing ? <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><InboundTrafficPlanFields idPrefix="inbound-plan" language={language} nextResetAt={nextResetAt} onQuotaChange={setQuota} onResetDaysChange={(value) => { setResetDays(value); setNextResetAt(value === baseline.current.resetDays ? baseline.current.nextResetAt : ""); }} quota={quota} resetDays={resetDays} />{error ? <FieldError className="mt-4" role="alert">{error}</FieldError> : null}</div><SheetFooter><Button disabled={running} onClick={discardEdit} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={running || !dirty} type="submit">{running ? <Spinner data-icon="inline-start" /> : null}{copy(language, "保存套餐", "Save plan")}</Button></SheetFooter></form> : null}
      {!editing && error ? <div className="flex flex-col items-start gap-3 px-4"><FieldError role="alert">{error}</FieldError>{!inbound ? <Button disabled={running} onClick={retryLoad} size="sm" variant="outline">{running ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" />}{copy(language, "重新读取", "Retry")}</Button> : null}</div> : null}
      {!editing ? <SheetFooter><Button onClick={requestClose} variant="outline">{copy(language, "关闭", "Close")}</Button></SheetFooter> : null}
    </SheetContent>
  </Sheet>;
}

function readableError(language: Language, error: unknown) {
  return error instanceof Error && error.message ? error.message.replace(/^center:\s*/i, "") : copy(language, "操作失败，请稍后重试。", "Operation failed. Try again shortly.");
}

function formatDate(value: string, language: Language, timeZone?: string) {
  try {
    return new Intl.DateTimeFormat(language, { dateStyle: "medium", timeZone: timeZone || "UTC" }).format(new Date(value));
  } catch {
    return new Intl.DateTimeFormat(language, { dateStyle: "medium", timeZone: "UTC" }).format(new Date(value));
  }
}
