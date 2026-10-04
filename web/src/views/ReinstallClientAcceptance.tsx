import { useEffect, useRef, useState } from "react";
import { api } from "../api";
import type { AgentReinstallPlan, AgentView } from "../types";
import type { ReinstallClientCheck } from "../reinstall-acceptance-types";
import type { Language } from "../translations";
import { Button } from "@/components/ui/button";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Field, FieldLabel } from "@/components/ui/field";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { copy, formatDate, userError } from "./shared";

const states: Record<string, [string, string]> = {
  pending: ["等待验证", "Queued"], running: ["正在验证", "Verifying"],
  verified: ["真实请求通过", "Real request verified"], succeeded: ["正在核对证据", "Checking evidence"],
  failed: ["验证失败", "Failed"], stale: ["结果已过期", "Expired"], needs_review: ["需核对", "Needs review"],
};

export function ReinstallClientAcceptance({ plan, language, revision }: { plan: AgentReinstallPlan; language: Language; revision: number }) {
  const [nodes, setNodes] = useState<AgentView[]>([]);
  const [checks, setChecks] = useState<ReinstallClientCheck[]>([]);
  const [verifier, setVerifier] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const generation = useRef(0);
  const requests = useRef<Record<string, string>>({});
  const operationId = plan.recovery?.id;
  useEffect(() => {
    const current = ++generation.current;
    setBusy(true); setError(""); setChecks([]);
    void (async () => {
      try {
        const [agents, receipts] = await Promise.all([api.agents(), api.agentReinstallClientChecks(plan.agentId)]);
        if (generation.current !== current) return;
        setNodes(agents.agents.filter((node) => node.id !== plan.agentId && node.connected && node.status === "active" && !node.credentialRevoked && node.capabilities.meridianAcceptance && !node.reinstall));
        setChecks(receipts);
      } catch (cause) { if (generation.current === current) setError(userError(language, cause)); }
      finally { if (generation.current === current) setBusy(false); }
    })();
    return () => { generation.current += 1; };
  }, [plan.agentId, plan.revision, operationId, revision, language]);
  const eligible = nodes.some((node) => node.id === verifier);
  const verify = async (applicationId: string) => {
    if (busy || !operationId || !eligible) return;
    const current = generation.current;
    const requestKey = `${plan.agentId}:${operationId}:${plan.revision}:${verifier}:${applicationId}`;
    setBusy(true); setError("");
    try {
      requests.current[requestKey] ??= crypto.randomUUID();
      await api.verifyAgentReinstallClients(plan.agentId, { requestId: requests.current[requestKey], operationId, planRevision: plan.revision, applicationId, verifierAgentId: verifier });
      const receipts = await api.agentReinstallClientChecks(plan.agentId);
      if (generation.current === current) { setChecks(receipts); delete requests.current[requestKey]; }
    } catch (cause) { if (generation.current === current) setError(userError(language, cause)); }
    finally { if (generation.current === current) setBusy(false); }
  };
  return <section className="flex flex-col gap-3 rounded-xl border p-4" aria-label={copy(language, "真实客户端验收", "Real client acceptance")}>
    <h3 className="text-sm font-medium">{copy(language, "真实客户端验收", "Real client acceptance")}</h3>
    <p className="text-xs text-muted-foreground">{copy(language, "由另一台在线节点使用原订阅身份发起请求，核对原生和固定落地出口。验证通过不代表全部恢复步骤完成。", "Another online node uses the original subscription identity to verify native and fixed exits. Passing does not complete every recovery requirement.")}</p>
    <Field>
      <FieldLabel htmlFor="recovery-verifier">{copy(language, "验证节点", "Verifier")}</FieldLabel>
      <Select value={verifier || null} onValueChange={(value) => setVerifier(value ?? "")} disabled={busy} items={nodes.map((node) => ({ value: node.id, label: node.name }))}>
        <SelectTrigger id="recovery-verifier"><SelectValue placeholder={copy(language, "选择另一台在线节点", "Select another online node")} /></SelectTrigger>
        <SelectContent><SelectGroup>{nodes.map((node) => <SelectItem key={node.id} value={node.id}>{node.name}</SelectItem>)}</SelectGroup></SelectContent>
      </Select>
    </Field>
    {!busy && nodes.length === 0 ? <p className="text-xs text-muted-foreground">{copy(language, "没有支持自动验收的在线节点。请先更新并接入验证节点。", "No online verifier supports automatic acceptance. Update and connect a verifier first.")}</p> : null}
    {plan.applications.filter((app) => app.recovery === "rebuild_configuration").map((app) => <div key={app.applicationId} className="flex items-center justify-between gap-3">
      <span className="text-sm">{app.name}</span>
      <Button variant="outline" disabled={busy || !eligible || app.preparation?.access?.state !== "applied"} onClick={() => void verify(app.applicationId)}>{copy(language, "验证客户端", "Verify clients")}</Button>
    </div>)}
    {checks.length ? <ul className="flex flex-col gap-2 text-xs" aria-live="polite">{checks.map((check) => <li key={check.commandId} className="flex flex-wrap justify-between gap-2">
      <span>{check.accountName} · {check.protocol} · {check.egressName || copy(language, "原生出口", "Native exit")}</span>
      <span>{copy(language, ...(states[check.state] ?? states.needs_review))}{check.checkedAt ? ` · ${formatDate(language, check.checkedAt)}` : ""}</span>
    </li>)}</ul> : null}
    <p className="text-xs text-muted-foreground">{copy(language, "使用下方“刷新状态”读取结果；刷新不会再次发起请求。", "Use Refresh status below to read results; refreshing never starts another request.")}</p>
    {error ? <Alert variant="destructive"><AlertDescription>{error}</AlertDescription></Alert> : null}
  </section>;
}
