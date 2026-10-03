import { useEffect, useState } from "react";
import { api } from "../api";
import type { NodeEgress, EgressPolicy } from "../node-egress-types";
import type { ApplicationCommand } from "../types";
import type { Language } from "../translations";
import { useApplicationCommandExecutor } from "../hooks/use-application-command-executor";
import { SelectControl } from "@/components/SelectControl";
import { Button } from "@/components/ui/button";
import { FieldDescription, FieldError, FieldLegend, FieldSet } from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import { copy, userError } from "./shared";

export function NodeEgressControls({ nodeId, language }: { nodeId: string; language: Language }) {
  const [saved, setSaved] = useState<NodeEgress | null>(null);
  const [draft, setDraft] = useState<EgressPolicy>("auto");
  const [command, setCommand] = useState<ApplicationCommand | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [reload, setReload] = useState(0);
  const { execute } = useApplicationCommandExecutor(nodeId);
  const options = [
    { value: "auto", label: copy(language, "自动", "Automatic") },
    { value: "ipv4_only", label: copy(language, "仅 IPv4", "IPv4 only") },
    { value: "ipv6_only", label: copy(language, "仅 IPv6", "IPv6 only") },
  ];
  useEffect(() => {
    let cancelled = false;
    setError("");
    void api.nodeEgress(nodeId).then(async (view) => {
      if (cancelled) return;
      setSaved(view); setDraft(view.policy);
      if (view.commandId && ["pending", "applying", "running", "failed"].includes(view.state)) {
        const current = await api.applicationCommand(view.commandId);
        if (cancelled) return;
        setCommand(current);
        if (current.state === "pending" || current.state === "running") {
          const result = await execute(async () => current, (next) => { if (!cancelled) setCommand(next); });
          if (!cancelled && result) setReload((value) => value + 1);
        }
      }
    }).catch((cause) => { if (!cancelled) setError(userError(language, cause)); });
    return () => { cancelled = true; };
  }, [nodeId, language, reload, execute]);
  const active = busy || saved?.state === "pending" || saved?.state === "applying" || command?.state === "pending" || command?.state === "running";
  const apply = async () => {
    if (!saved || active || !saved.available || command?.reconciliationRequired) return;
    setBusy(true); setError("");
    try {
      const result = await execute(async () => {
        const view = await api.configureNodeEgress(nodeId, draft, saved.revision);
        setSaved(view);
        if (!view.commandId) throw new Error(copy(language, "未取得应用任务，请刷新状态。", "No apply task returned. Refresh the status."));
        return api.applicationCommand(view.commandId);
      }, setCommand);
      if (result) setReload((value) => value + 1);
    } catch (cause) {
      setError(userError(language, cause));
      // The request may have queued work before monitoring failed. Read its
      // durable state; never retry a mutation to recover the display.
      try {
        const view = await api.nodeEgress(nodeId);
        setSaved(view); setDraft(view.policy);
        if (view.commandId) setCommand(await api.applicationCommand(view.commandId));
      } catch { /* Keep the error and allow an explicit status refresh. */ }
    }
    finally { setBusy(false); }
  };
  return <FieldSet>
    <FieldLegend>{copy(language, "出口 IP 策略", "Egress IP policy")}</FieldLegend>
    <FieldDescription>{copy(language, "仅控制本节点 Meridian 原生直连。订阅入口不变，远程落地仍由落地端控制。", "Controls this node’s native Meridian traffic. Subscription entry stays unchanged; remote landing servers control their own egress.")}</FieldDescription>
    {!saved && !error ? <FieldDescription role="status">{copy(language, "正在读取…", "Loading…")}</FieldDescription> : null}
    {saved ? <>
      <SelectControl aria-label={copy(language, "出口 IP 策略", "Egress IP policy")} disabled={active || !saved.available || command?.reconciliationRequired} options={options} value={draft} onValueChange={(value) => setDraft(value as EgressPolicy)} />
      {draft !== "auto" ? <FieldDescription>{copy(language, "目标不支持所选地址族时连接失败，不会切换到另一地址族。", "Targets without the selected address family will fail; there is no fallback to the other family.")}</FieldDescription> : null}
      {saved.available ? <FieldDescription>{copy(language, "应用会重载代理，现有连接可能短暂中断。首次应用将此节点代理核心更新至 26.9.30；返回自动模式不会降级核心。", "Applying reloads the proxy and may interrupt connections. First use updates this node’s core to 26.9.30; Automatic does not downgrade it.")}</FieldDescription> : null}
      <FieldDescription role="status">{active ? copy(language, "正在应用并验证真实客户端请求…", "Applying and verifying real client requests…") : saved.state === "failed" ? copy(language, "未确认生效", "Application unconfirmed") : saved.verified ? copy(language, "最近验证通过", "Last verification passed") : copy(language, "尚无出口验证记录", "No egress verification recorded")}</FieldDescription>
      {saved.verified ? <FieldDescription>{copy(language, "最近确认：", "Last confirmed: ")}{options.find((item) => item.value === saved.appliedPolicy)?.label} · {saved.verified.exits.join(" / ")} · {new Date(saved.verified.checkedAt).toLocaleString()}</FieldDescription> : null}
      {saved.error ? <FieldError>{saved.error}</FieldError> : null}
      {command?.reconciliationRequired ? <FieldError>{copy(language, "任务结果需要核对，请先在活动页面处理。", "Resolve this task’s uncertain outcome in Activity before applying again.")}</FieldError> : null}
      <div className="flex gap-2">
        <Button type="button" variant="outline" disabled={active || !saved.available || command?.reconciliationRequired || draft === saved.policy && saved.state !== "failed"} onClick={() => void apply()}>{active ? <Spinner data-icon="inline-start" /> : null}{copy(language, "应用并验证", "Apply and verify")}</Button>
        <Button type="button" variant="ghost" disabled={busy} onClick={() => setReload((value) => value + 1)}>{copy(language, "刷新状态", "Refresh")}</Button>
      </div>
    </> : null}
    {error ? <FieldError role="alert">{error}</FieldError> : null}
    {!saved && error ? <Button type="button" variant="outline" onClick={() => setReload((value) => value + 1)}>{copy(language, "重试", "Retry")}</Button> : null}
  </FieldSet>;
}
