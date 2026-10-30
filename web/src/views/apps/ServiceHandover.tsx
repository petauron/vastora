import { useState } from "react";
import type { AppData, Mutate } from "../../App";
import type { Application } from "../../types";
import type { Language } from "../../translations";
import { api } from "../../api";
import { copy, userError } from "../shared";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { FieldError } from "@/components/ui/field";

type Plan = Awaited<ReturnType<typeof api.previewServiceHandover>>;

export function ServiceHandover({ data, application, language, mutate, disabled }: {
  data: AppData; application: Application; language: Language; mutate: Mutate; disabled: boolean;
}) {
  const [sourceID, setSourceID] = useState("");
  const [targetID, setTargetID] = useState("");
  const [plan, setPlan] = useState<Plan | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState(false);
  const sources = data.services.filter((service) => service.applicationId !== application.id && service.status === "ready"
    && data.applications.some((app) => app.id === service.applicationId && app.nodeId === application.nodeId)
    && data.publications.some((pub) => pub.serviceId === service.id && pub.status !== "stopped"));
  const targets = data.services.filter((service) => service.applicationId === application.id && service.status === "ready"
    && !data.publications.some((pub) => pub.serviceId === service.id));
  const reset = () => { setPlan(null); setConfirmed(false); setError(""); setDone(false); };
  const preview = async () => {
    if (busy || disabled || !sourceID || !targetID) return;
    setBusy(true); setError(""); setPlan(null); setConfirmed(false);
    try { setPlan(await api.previewServiceHandover(sourceID, targetID)); }
    catch (failure) { setError(userError(language, failure)); }
    finally { setBusy(false); }
  };
  const apply = async () => {
    if (busy || disabled || !plan || !confirmed) return;
    setBusy(true); setError("");
    try {
      await mutate(async () => { await api.handoverService(plan.sourceServiceId, { targetServiceId: plan.targetServiceId, sourceSha256: plan.sourceSha256, targetSha256: plan.targetSha256, backupsConfirmed: true }); }, undefined, { reportError: false });
      setDone(true); setPlan(null); setConfirmed(false);
    } catch (failure) { setError(userError(language, failure)); setPlan(null); setConfirmed(false); }
    finally { setBusy(false); }
  };
  if (done) return <p role="status">{copy(language, "入口绑定已交接，仍需检查入口并使用订阅实测访问。", "Access binding transferred. Verify the access point and test the subscription before declaring recovery complete.")}</p>;
  if (!sources.length || !targets.length) return null;
  const fieldID = `handover-${application.id}`;
  return <section className="space-y-3 rounded-xl border p-3" aria-label={copy(language, "恢复原有入口", "Restore existing access")}>
    <p className="font-medium">{copy(language, "恢复原有入口", "Restore existing access")}</p>
    <p className="text-sm text-muted-foreground">{copy(language, "将本机原有入口交给已恢复的服务，保留域名及入口身份。先备份并核验新服务；不会重装旧应用或自动认定访问正常。", "Transfer this node's existing access to the restored service, retaining its hostname and identity. Back up and verify the new service first; this does not reinstall the old app or prove live access.")}</p>
    <label className="block space-y-1" htmlFor={`${fieldID}-source`}><span>{copy(language, "原有服务", "Original service")}</span><Select value={sourceID || null} disabled={busy || disabled} onValueChange={(value) => { setSourceID(value ?? ""); reset(); }} items={sources.map((service) => ({ value: service.id, label: `${service.displayName || service.name} · ${service.endpoint}` }))}><SelectTrigger id={`${fieldID}-source`} className="w-full"><SelectValue placeholder={copy(language, "请选择", "Select")} /></SelectTrigger><SelectContent><SelectGroup>{sources.map((service) => <SelectItem key={service.id} value={service.id}>{service.displayName || service.name} · {service.endpoint}</SelectItem>)}</SelectGroup></SelectContent></Select></label>
    <label className="block space-y-1" htmlFor={`${fieldID}-target`}><span>{copy(language, "恢复后的服务", "Restored service")}</span><Select value={targetID || null} disabled={busy || disabled} onValueChange={(value) => { setTargetID(value ?? ""); reset(); }} items={targets.map((service) => ({ value: service.id, label: `${service.name} · ${service.endpoint}` }))}><SelectTrigger id={`${fieldID}-target`} className="w-full"><SelectValue placeholder={copy(language, "请选择", "Select")} /></SelectTrigger><SelectContent><SelectGroup>{targets.map((service) => <SelectItem key={service.id} value={service.id}>{service.name} · {service.endpoint}</SelectItem>)}</SelectGroup></SelectContent></Select></label>
    <Button type="button" variant="outline" disabled={busy || disabled || !sourceID || !targetID} onClick={() => void preview()}>{copy(language, busy ? "处理中…" : "预览交接", busy ? "Working…" : "Preview handover")}</Button>
    {plan ? <div className="space-y-2"><p>{copy(language, `将保留并交接 ${plan.publications} 个入口。`, `${plan.publications} access points will be retained and transferred.`)}</p><label className="flex items-center gap-2"><Checkbox checked={confirmed} disabled={busy || disabled} onCheckedChange={setConfirmed} />{copy(language, "已备份并确认新服务正常", "Backups saved and restored service verified")}</label><Button type="button" disabled={busy || disabled || !confirmed} onClick={() => void apply()}>{copy(language, "确认交接入口", "Confirm access handover")}</Button></div> : null}
    {error ? <FieldError role="alert">{error}</FieldError> : null}
  </section>;
}
