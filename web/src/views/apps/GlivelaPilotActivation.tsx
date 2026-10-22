import { useState, type FormEvent } from "react";
import { api } from "../../api";
import type { Mutate } from "../../App";
import type { MeridianEndpoint } from "../../meridian-types";
import type { Language } from "../../translations";
import { copy, userError } from "../shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Checkbox } from "@/components/ui/checkbox";
import { Textarea } from "@/components/ui/textarea";
import { Spinner } from "@/components/ui/spinner";

// This is an operator entry point for the existing one-node transaction, not
// another migration orchestrator. Never synthesize timestamps or final counters.
export function GlivelaPilotActivation({ endpoint, language, mutate, disabled }: {
  endpoint: MeridianEndpoint; language: Language; mutate: Mutate; disabled: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [evidence, setEvidence] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [accepted, setAccepted] = useState(false);
  const [error, setError] = useState("");
  const [resumeConfirmed, setResumeConfirmed] = useState(false);
  const [resumeQueued, setResumeQueued] = useState(false);
  const resume = async () => {
    if (!resumeConfirmed || busy || accepted || disabled || resumeQueued) return;
    setBusy(true);
    setError("");
    try {
      await mutate(async () => {
        const result = await api.recoverMeridianEndpoint(endpoint.id);
        if (!result.queued) throw new Error("Recovery was not queued; inspect the current operation.");
        setResumeQueued(true);
        setEvidence("");
        setConfirmed(false);
      }, undefined, { reportError: false });
    } catch (failure) {
      setError(userError(language, failure));
    } finally {
      setBusy(false);
    }
  };
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!confirmed || busy || accepted || disabled) return;
    setError("");
    let input: Record<string, unknown>;
    try {
      const envelope = JSON.parse(evidence);
      if (!envelope || envelope.endpointId !== endpoint.id || !envelope.activation ||
          typeof envelope.activation !== "object" || Array.isArray(envelope.activation) ||
          envelope.activation.drainedAndStopped !== true) throw new Error();
      input = envelope.activation;
    } catch {
      setError(copy(language, "证据 JSON 必须包含当前入口的 endpointId 和 activation，且旧运行时已排空停止。", "Evidence JSON must contain this entry's endpointId and activation, with the old runtime drained and stopped."));
      return;
    }
    setBusy(true);
    try {
      await mutate(async () => {
        const result = await api.activateGlivelaPilot(endpoint.id, input);
        if (result.state !== "awaiting-native-receipt") throw new Error("Unexpected activation state; verify the server before retrying.");
        setAccepted(true);
        setEvidence("");
      }, undefined, { reportError: false });
    } catch (failure) {
      setError(userError(language, failure));
    } finally {
      setBusy(false);
    }
  };
  if (!open) return <Button disabled={disabled} variant="outline" size="sm" onClick={() => setOpen(true)}>{copy(language, "接管此入口到 Glivela", "Take over this entry with Glivela")}</Button>;
  return <form onSubmit={(event) => void submit(event)} className="flex flex-col gap-4">
    <Alert><AlertTitle>{copy(language, "仅接管当前入口", "Take over only this entry")} · {endpoint.displayName}</AlertTitle><AlertDescription>
      {copy(language, "保留原订阅地址、身份、配置、唯一账本和历史。此按钮不停止服务或采集计数；先核验独立备份，再排空旧运行时并采集最终计数、停止旧服务，30 秒内提交。提交后禁止旧侧操作本入口。", "Keep the subscription URL, identity, configuration, sole ledger and history. This button does not stop services or collect counters. Verify an independent backup, drain and sample the old runtime, stop it, then submit within 30 seconds. Activation fences legacy writes to this entry.")}
    </AlertDescription></Alert>
    {!accepted && endpoint.status === "ready" && endpoint.appliedRevision > 0 && endpoint.desiredRevision === endpoint.appliedRevision ? <FieldGroup>
      <FieldDescription>{copy(language, "接管未提交且旧服务已恢复运行时，可重试当前配置的真实健康探测。先确认原执行已停止且新运行时未启动；此操作不接管、不清理资源，也不补写流量。", "If takeover was not committed and the old service is running again, resume real health checks for its current configuration. Confirm the previous execution stopped and the new runtime is inactive. This does not take over, clean resources, or reconcile traffic.")}</FieldDescription>
      <Field orientation="horizontal"><Checkbox id="glivela-resume-confirm" checked={resumeConfirmed} onCheckedChange={(value) => setResumeConfirmed(value === true)} disabled={busy || disabled || resumeQueued} /><FieldLabel htmlFor="glivela-resume-confirm">{copy(language, "确认仍由 Center 管理，原执行已停止，新运行时未启动", "Confirm Center still owns this entry, the prior execution stopped, and the new runtime is inactive")}</FieldLabel></Field>
      <Button type="button" variant="outline" disabled={!resumeConfirmed || busy || disabled || resumeQueued} onClick={() => void resume()}>{copy(language, "恢复当前入口健康探测", "Resume this entry's health checks")}</Button>
    </FieldGroup> : null}
    {resumeQueued ? <Alert role="status"><AlertTitle>{copy(language, "恢复任务已排队，尚未确认线路恢复", "Recovery queued; route recovery is not yet verified")}</AlertTitle><AlertDescription>{copy(language, "请在活动中核验任务，并复测订阅和双跳。再次接管前重新采集最终计数，不要复用旧证据。", "Verify the operation in Activity and retest subscriptions and double hops. Capture fresh final counters before another takeover; do not reuse old evidence.")}</AlertDescription></Alert> : null}
    {accepted ? <Alert role="status"><AlertTitle>{copy(language, "接管已激活，等待原生回执", "Takeover activated; awaiting native receipt")}</AlertTitle><AlertDescription>{copy(language, "这不代表代理已恢复。请启动已核验的新运行时，并验证订阅、线路和流量连续性；不要重新启动旧运行时或重复提交。", "This does not mean proxy recovery. Start the verified native runtime and verify subscription, routes and accounting continuity. Do not restart the old runtime or resubmit.")}</AlertDescription></Alert> : <FieldGroup>
      <Field data-invalid={Boolean(error)}><FieldLabel htmlFor="glivela-pilot-evidence">{copy(language, "接管证据 JSON", "Takeover evidence JSON")}</FieldLabel>
        <Textarea id="glivela-pilot-evidence" value={evidence} onChange={(event) => setEvidence(event.target.value)} required disabled={busy || disabled} aria-invalid={Boolean(error)} autoComplete="off" spellCheck={false} rows={8} maxLength={131072} />
        <FieldDescription>{copy(language, "仅包含 endpointId 和 activation；不要粘贴私钥、密码或订阅链接。证据仅留在本次表单内存，不写入浏览器存储。服务器再次校验摘要、身份、计数和任务屏障。", "Include only endpointId and activation; never paste private keys, passwords or subscription links. Evidence stays in this form's memory, not browser storage. The server revalidates hashes, identity, counters and task barriers.")}</FieldDescription>
      </Field>
      <Field orientation="horizontal"><Checkbox id="glivela-pilot-confirm" checked={confirmed} onCheckedChange={(value) => setConfirmed(value === true)} disabled={busy || disabled} /><FieldLabel htmlFor="glivela-pilot-confirm">{copy(language, "已核验备份、最终计数及旧运行时停止，仅接管上述入口", "Backup, final counters and stopped old runtime verified; take over only the entry above")}</FieldLabel></Field>
      {error ? <FieldError role="alert">{error}</FieldError> : null}
      <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={busy} onClick={() => { setOpen(false); setEvidence(""); setConfirmed(false); setError(""); }}>{copy(language, "取消", "Cancel")}</Button><Button type="submit" disabled={!confirmed || !evidence.trim() || busy || disabled}>{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "确认并激活接管", "Confirm and activate takeover")}</Button></div>
    </FieldGroup>}
  </form>;
}
