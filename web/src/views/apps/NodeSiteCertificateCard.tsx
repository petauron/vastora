import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { api } from "../../api";
import type { Mutate } from "../../App";
import type { NodeSiteCertificate } from "../../node-site-types";
import type { Language } from "../../translations";
import { copy, formatDate, userError } from "../shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";

const productionCA = "https://acme-v02.api.letsencrypt.org/directory";
const stagingCA = "https://acme-staging-v02.api.letsencrypt.org/directory";

export function NodeSiteCertificateCard({ applicationId, language, mutate, disabled }: {
  applicationId: string; language: Language; mutate: Mutate; disabled: boolean;
}) {
  const id = useId();
  const initialized = useRef(false);
  const [view, setView] = useState<NodeSiteCertificate | null>(null);
  const [hostname, setHostname] = useState("");
  const [port, setPort] = useState("24482");
  const [staging, setStaging] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [action, setAction] = useState<"rollback" | "cancel" | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const refresh = async () => {
      try {
        const next = await api.nodeSiteCertificate(applicationId, controller.signal);
        if (controller.signal.aborted) return;
        setView(next);
        if (!initialized.current) {
          initialized.current = true;
          setHostname(next.hostname);
          setPort(String(next.port || 24482));
          setStaging(next.caDirectory === stagingCA);
        }
      } catch (failure) {
        if (!controller.signal.aborted) setError(userError(language, failure));
      } finally {
        if (!controller.signal.aborted) timer = setTimeout(() => { void refresh(); }, 15000);
      }
    };
    void refresh();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [applicationId, language]);

  const run = async (operation: () => Promise<NodeSiteCertificate>) => {
    if (busy || disabled) return;
    setBusy(true); setError("");
    try {
      await mutate(async () => { setView(await operation()); }, undefined, { reportError: false });
      setConfirmed(false); setAction(null);
    } catch (failure) { setError(userError(language, failure)); }
    finally { setBusy(false); }
  };
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!confirmed) return;
    void run(() => api.configureNodeSiteCertificate(applicationId, { hostname: hostname.trim(), port: Number(port), caDirectory: staging ? stagingCA : productionCA }));
  };
  const active = view?.state === "issuing" || view?.state === "pending";
  const labels: Record<NodeSiteCertificate["state"], string> = {
    unconfigured: copy(language, "未启用", "Not enabled"), issuing: copy(language, "签发或续期中", "Issuing or renewing"),
    pending: copy(language, "等待节点生效", "Awaiting activation"), ready: copy(language, "节点已确认生效", "Activation confirmed"),
    action_required: copy(language, "需要处理", "Action required"), rolled_back: copy(language, "已回滚", "Rolled back"),
  };
  return <Card size="sm" aria-busy={busy}>
    <CardHeader><CardTitle>{copy(language, "主控证书", "Center-managed certificate")}</CardTitle><CardDescription>{copy(language, "统一签发和续期，为本站点提供独立的本机 TLS 后端。", "Central issuance and renewal with an independent local TLS backend.")}</CardDescription></CardHeader>
    <CardContent className="flex flex-col gap-3">
      {view ? <div className="flex flex-wrap items-center gap-2" role="status"><Badge variant="outline">{labels[view.state]}</Badge>{view.revision > 0 ? <span>{copy(language, `修订 ${view.appliedRevision} / ${view.revision}`, `Revision ${view.appliedRevision} / ${view.revision}`)}</span> : null}</div> : <p role="status"><Spinner />{copy(language, "正在读取证书状态…", "Loading certificate state…")}</p>}
      {view?.notAfter ? <p>{copy(language, "证书到期：", "Expires: ")}{formatDate(language, view.notAfter)}</p> : null}
      {view?.fingerprint ? <p className="break-all font-mono text-xs">SHA256: {view.fingerprint}</p> : null}
      {view?.error ? <Alert variant="destructive"><AlertTitle>{copy(language, "证书管理需要检查", "Review certificate management")}</AlertTitle><AlertDescription>{view.error}</AlertDescription></Alert> : null}
      <form onSubmit={submit}><FieldGroup>
        <Field><FieldLabel htmlFor={`${id}-hostname`}>{copy(language, "已安装站点域名", "Installed site hostname")}</FieldLabel><Input id={`${id}-hostname`} value={hostname} readOnly /><FieldDescription>{copy(language, "仅为管理员已绑定的精确域名申请，不共享通配符私钥。", "Only the exact administrator-bound hostname is eligible. No shared wildcard key.")}</FieldDescription></Field>
        <Field><FieldLabel htmlFor={`${id}-port`}>{copy(language, "本机 TLS 端口", "Loopback TLS port")}</FieldLabel><Input id={`${id}-port`} value={port} onChange={(event) => setPort(event.target.value)} type="number" min={1024} max={65535} required disabled={disabled || busy || active} /><FieldDescription>{copy(language, "监听 127.0.0.1，请选择未被占用的高位端口。", "Binds to 127.0.0.1. Choose an unused high port.")}</FieldDescription></Field>
        <Field orientation="horizontal"><Switch id={`${id}-staging`} checked={staging} onCheckedChange={setStaging} disabled={disabled || busy || active} /><FieldLabel htmlFor={`${id}-staging`}>{copy(language, "使用 ACME 测试环境", "Use ACME staging")}</FieldLabel></Field>
        {staging ? <FieldDescription>{copy(language, "测试证书不受浏览器信任，仅用于独立验收，不应用于正在承载代理的站点。", "Staging certificates are not browser-trusted. Use an isolated site, not an active proxy target.")}</FieldDescription> : null}
        <Field orientation="horizontal"><Checkbox id={`${id}-confirmed`} checked={confirmed} onCheckedChange={(value) => setConfirmed(value === true)} disabled={disabled || busy || active} /><FieldLabel htmlFor={`${id}-confirmed`}>{copy(language, "已核对恢复点，允许更新本站点证书", "Recovery point verified; allow updating this site's certificate")}</FieldLabel></Field>
        <Button type="submit" disabled={!view || !hostname || !confirmed || disabled || busy || active}>{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, view?.state === "unconfigured" ? "启用主控管理" : "保存证书配置", view?.state === "unconfigured" ? "Enable Center management" : "Save certificate settings")}</Button>
      </FieldGroup></form>
      {view && view.state !== "unconfigured" ? <div className="flex flex-wrap gap-2">
        <Button variant="outline" disabled={disabled || busy || active} onClick={() => void run(() => api.nodeSiteCertificateAction(applicationId, "inspect"))}>{copy(language, "核对节点证书", "Inspect node certificate")}</Button>
        <Button variant="outline" disabled={disabled || busy || active || view.appliedRevision === 0 || view.state === "rolled_back"} onClick={() => setAction("rollback")}>{copy(language, "回滚站点配置", "Roll back site configuration")}</Button>
        <Button variant="outline" disabled={disabled || busy || view.state === "rolled_back" || view.state === "action_required"} onClick={() => setAction("cancel")}>{copy(language, "暂停证书管理", "Pause certificate management")}</Button>
      </div> : null}
      {action ? <Alert><AlertTitle>{copy(language, action === "rollback" ? "确认恢复上一次站点配置？" : "确认暂停签发和续期？", action === "rollback" ? "Restore the previous site configuration?" : "Pause issuance and renewal?")}</AlertTitle><AlertDescription>{copy(language, action === "rollback" ? "恢复本站点的上次证书和监听配置，保留其他站点与网关路由。" : "当前站点继续使用已安装证书，但暂停后不会自动续期。", action === "rollback" ? "Restore this site's previous certificate and listeners while retaining other websites and gateway routes." : "The installed certificate stays active, but renewal is paused.")}</AlertDescription><div className="flex gap-2"><Button variant="outline" disabled={busy} onClick={() => setAction(null)}>{copy(language, "返回", "Back")}</Button><Button disabled={busy || disabled} onClick={() => void run(() => api.nodeSiteCertificateAction(applicationId, action))}>{copy(language, "确认", "Confirm")}</Button></div></Alert> : null}
      {error ? <FieldError role="alert">{error}</FieldError> : null}
    </CardContent>
  </Card>;
}
