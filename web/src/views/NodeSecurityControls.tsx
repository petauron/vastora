import { useEffect, useId, useState } from "react";
import { api } from "../api";
import type { Language } from "../translations";
import type { NodeSecurity, SecurityPolicy, SecurityRule, SecurityTask } from "../node-security-types";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { copy, userError } from "./shared";

const emptyPolicy: SecurityPolicy = { publicInterfaces: [], managementSources: [], sshPorts: [], publicRules: [] };
const split = (raw: string) => raw.split(/[\s,]+/).filter(Boolean);

export function NodeSecurityControls({ nodeId, language }: { nodeId: string; language: Language }) {
  const id = useId();
  const [view, setView] = useState<NodeSecurity | null>(null);
  const [interfaces, setInterfaces] = useState("");
  const [sources, setSources] = useState("");
  const [ports, setPorts] = useState("");
  const [services, setServices] = useState("");
  const [reviewed, setReviewed] = useState(false);
  const [businessChecked, setBusinessChecked] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const result = view?.result;
  const waiting = view?.state === "pending" || view?.state === "running";
  const active = busy || waiting;
  const policy: SecurityPolicy = { publicInterfaces: split(interfaces), managementSources: split(sources), sshPorts: split(ports).map(Number), publicRules: split(services).map((raw) => { const [port, protocol] = raw.split("/"); return { port: Number(port), protocol } as SecurityRule; }) };
  const previewMatches = result?.state === "preview" && JSON.stringify(policy) === JSON.stringify(result.policy);
  useEffect(() => {
    const controller = new AbortController();
    void api.nodeSecurity(nodeId, controller.signal).then(setView).catch((cause) => { if (!controller.signal.aborted) setError(userError(language, cause)); });
    return () => controller.abort();
  }, [nodeId, language]);
  useEffect(() => {
    if (!waiting) return;
    const controller = new AbortController();
    const timer = setInterval(() => { void api.nodeSecurity(nodeId, controller.signal).then(setView).catch((cause) => { if (!controller.signal.aborted) setError(userError(language, cause)); }); }, 1500);
    return () => { controller.abort(); clearInterval(timer); };
  }, [nodeId, language, waiting]);
  const queue = async (action: SecurityTask["action"], address?: string) => {
    if (active) return;
    setBusy(true); setError("");
    try { setView(await api.configureNodeSecurity(nodeId, { action, policy: action === "preview" || action === "apply" ? policy : emptyPolicy, revision: result?.revision, address })); }
    catch (cause) {
      setError(userError(language, cause));
      // Re-read durable state; never retry a firewall mutation after a lost response.
      try { setView(await api.nodeSecurity(nodeId)); } catch { /* Keep the original error. */ }
    } finally { setBusy(false); setReviewed(false); setBusinessChecked(false); }
  };
  const state = result?.state ?? "unconfigured";
  const labels: Record<string, string> = {
    unconfigured: copy(language, "尚未配置", "Not configured"), preview: copy(language, "仅预览，未应用", "Preview only; not applied"),
    pending_confirmation: copy(language, "待验证，超时将自动回滚", "Awaiting verification; automatic rollback armed"), verified: copy(language, "最近防护验证通过", "Last protection verification passed"),
    rolled_back: copy(language, "已撤销本次防护", "Baseline rolled back"), action_required: copy(language, "需要核对节点实际状态", "Host verification required"),
  };
  if (!view) return <FieldSet><FieldLegend>{copy(language, "公网安全基线", "Public-node security baseline")}</FieldLegend>{error ? <FieldError role="alert">{error}</FieldError> : <FieldDescription role="status">{copy(language, "正在读取…", "Loading…")}</FieldDescription>}<Button type="button" variant="outline" onClick={() => { void api.nodeSecurity(nodeId).then(setView).catch((cause) => setError(userError(language, cause))); }}>{copy(language, "重试读取状态", "Retry reading status")}</Button></FieldSet>;
  return <FieldSet>
    <FieldLegend>{copy(language, "公网安全基线", "Public-node security baseline")}</FieldLegend>
    <FieldDescription>{copy(language, "逐台显式启用。仅保护批准的公网网卡与 SSH；不重置 Docker、私网或落地防火墙。请先准备独立恢复通道。", "Opt in per node. Protects approved public interfaces and SSH without resetting Docker, private networking or landing firewalls. Prepare an independent recovery path first.")}</FieldDescription>
    <FieldDescription>{copy(language, "草稿限制：代理与落地节点暂不允许启用，动态 UDP 回包尚未完成独立验收。", "Draft limitation: proxy and landing nodes are refused until dynamic UDP return traffic has independent acceptance.")}</FieldDescription>
    <FieldDescription role="status">{active ? copy(language, "正在等待节点执行…", "Waiting for the node…") : labels[state]}</FieldDescription>
    {result?.deadline ? <FieldDescription>{copy(language, "验证/回滚截止时间：", "Verification/rollback deadline: ")}{new Date(result.deadline).toLocaleString()}</FieldDescription> : null}
    {!result?.revision || state === "rolled_back" || state === "preview" && !result?.supported ? <Button type="button" variant="outline" disabled={active} onClick={() => void queue("prepare")}>{copy(language, "准备缺少的系统依赖（不启用防护）", "Prepare missing system packages (protection stays off)")}</Button> : null}
    {state === "unconfigured" || state === "preview" || state === "rolled_back" ? <FieldGroup>
      <Field><FieldLabel htmlFor={`${id}-interfaces`}>{copy(language, "公网网卡（包含 NAT/IPv6）", "Public interfaces (including NAT/IPv6)")}</FieldLabel><Input id={`${id}-interfaces`} disabled={active} value={interfaces} onChange={(e) => { setInterfaces(e.target.value); setReviewed(false); }} placeholder="eth0" /></Field>
      <Field><FieldLabel htmlFor={`${id}-sources`}>{copy(language, "批准的 SSH 管理来源 CIDR", "Approved SSH management CIDRs")}</FieldLabel><Input id={`${id}-sources`} disabled={active} value={sources} onChange={(e) => { setSources(e.target.value); setReviewed(false); }} placeholder="203.0.113.10/32, 2001:db8::10/128" /><FieldDescription>{copy(language, "填写实际来源；不能默认豁免整个私网。", "Use actual management sources, not a blanket private-network exemption.")}</FieldDescription></Field>
      <Field><FieldLabel htmlFor={`${id}-ports`}>{copy(language, "实际 SSH 端口", "Actual SSH ports")}</FieldLabel><Input id={`${id}-ports`} disabled={active} value={ports} onChange={(e) => { setPorts(e.target.value); setReviewed(false); }} placeholder="22" /></Field>
      <Field><FieldLabel htmlFor={`${id}-services`}>{copy(language, "批准的公网服务端口", "Approved public service ports")}</FieldLabel><Input id={`${id}-services`} disabled={active} value={services} onChange={(e) => { setServices(e.target.value); setReviewed(false); }} placeholder="80/tcp, 443/tcp, 443/udp, 41641/udp" /><FieldDescription>{copy(language, "必须核对实际 Tailscale UDP 端口及现有代理入口，不要照抄示例。", "Verify the actual Tailscale UDP port and existing proxy entry points; do not blindly copy examples.")}</FieldDescription></Field>
      <Button type="button" variant="outline" disabled={active} onClick={() => void queue("preview")}>{copy(language, "检测并预览", "Inspect and preview")}</Button>
      {previewMatches && result?.supported ? <>
        <FieldDescription>{copy(language, "应用前先用批准来源建立全新 SSH 连接，执行下方命令验证管理通道。没有这份本机凭据不会修改防火墙。", "Before applying, open a NEW SSH connection from an approved source and run this command. The firewall is not changed without this local receipt.")}</FieldDescription>
        <code className="break-all">sudo --preserve-env=SSH_CONNECTION /usr/local/bin/vastora agent security-verify-ssh {result.revision}</code>
        <Field orientation="horizontal"><Checkbox id={`${id}-reviewed`} checked={reviewed} onCheckedChange={(value) => setReviewed(value === true)} /><FieldLabel htmlFor={`${id}-reviewed`}>{copy(language, "已核对网卡、SSH、私网与代理端口，并准备独立恢复通道", "I reviewed interfaces, SSH, private/proxy ports and prepared independent recovery")}</FieldLabel></Field>
        <Button type="button" variant="destructive" disabled={active || !reviewed} onClick={() => void queue("apply")}>{copy(language, "应用，5 分钟内验证确认", "Apply with a 5-minute confirmation window")}</Button>
      </> : null}
    </FieldGroup> : null}
    {result?.listeners?.length ? <details><summary>{copy(language, "节点实际监听（只读）", "Actual listeners (read-only)")}</summary><pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all">{result.listeners.join("\n")}</pre></details> : null}
    {result?.reasons?.map((reason) => <FieldError key={reason}>{reason}</FieldError>)}
    {state === "pending_confirmation" && result?.revision ? <FieldGroup>
      <FieldDescription>{copy(language, "打开一条全新 SSH 连接（禁止复用旧连接），在节点中执行下方命令，再核对私网和代理链路，最后点击确认。原 SSH 会话仍连通不算验证。", "Open a NEW non-multiplexed SSH connection, run this command on the node, verify private/proxy connectivity, then confirm. The old SSH session does not count.")}</FieldDescription>
      <code className="break-all">sudo --preserve-env=SSH_CONNECTION /usr/local/bin/vastora agent security-verify-ssh {result.revision}</code>
      <Field orientation="horizontal"><Checkbox id={`${id}-business`} checked={businessChecked} onCheckedChange={(value) => setBusinessChecked(value === true)} /><FieldLabel htmlFor={`${id}-business`}>{copy(language, "已通过新 SSH、私网和真实代理请求验证", "Verified new SSH, private networking and real proxy requests")}</FieldLabel></Field>
      <Button type="button" disabled={active || !businessChecked} onClick={() => void queue("confirm")}>{copy(language, "确认此修订，取消自动回滚", "Confirm this revision and cancel rollback")}</Button>
    </FieldGroup> : null}
    {state === "verified" || state === "pending_confirmation" ? <FieldGroup><Field orientation="horizontal"><Checkbox id={`${id}-revert`} checked={reviewed} onCheckedChange={(value) => setReviewed(value === true)} /><FieldLabel htmlFor={`${id}-revert`}>{copy(language, "确认撤销本功能防护，保留原有规则", "Revert this baseline while preserving previous rules")}</FieldLabel></Field><Button type="button" variant="outline" disabled={active || !reviewed} onClick={() => void queue("disable")}>{copy(language, "撤销本功能的防护配置", "Revert this managed baseline")}</Button></FieldGroup> : null}
    {result?.banned?.map((address) => <div key={address} className="flex flex-wrap items-center gap-2"><code>{address}</code><Button type="button" variant="outline" disabled={active || state !== "verified"} onClick={() => void queue("unban", address)}>{copy(language, "解除 SSH 封禁", "Unban SSH")}</Button></div>)}
    <Button type="button" variant="ghost" disabled={active} onClick={() => void queue("inspect")}>{copy(language, "重新读取节点实际状态", "Inspect actual host state")}</Button>
    {view?.error ? <FieldError>{view.error}</FieldError> : null}
    {error ? <FieldError role="alert">{error}</FieldError> : null}
  </FieldSet>;
}
