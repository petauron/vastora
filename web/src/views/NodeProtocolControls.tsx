import { useEffect, useState } from "react";
import { api } from "../api";
import type { ApplicationCommand, NodeProtocols } from "../types";
import type { Language } from "../translations";
import { useApplicationCommandExecutor } from "../hooks/use-application-command-executor";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import { copy, userError } from "./shared";

export function NodeProtocolControls({ serviceId, language, onUpdated }: { serviceId: string; language: Language; onUpdated: () => Promise<void> }) {
  const [saved, setSaved] = useState<NodeProtocols | null>(null);
  const [draft, setDraft] = useState({ vless: true, hy2: false });
  const [command, setCommand] = useState<ApplicationCommand | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const { execute } = useApplicationCommandExecutor(serviceId);
  useEffect(() => {
    let cancelled = false;
    setSaved(null);
    setCommand(null);
    setError("");
    void api.nodeProtocols(serviceId).then(async (value) => {
      if (cancelled) return;
      setSaved(value);
      setDraft({ vless: value.vless, hy2: value.hy2 });
      if (value.commandId && value.state === "failed") {
        const failed = await api.applicationCommand(value.commandId);
        if (!cancelled) {
          setCommand(failed);
          setError(copy(language, "协议更新未完成，请重试。", "Protocols could not be updated. Try again."));
        }
      }
      if (value.commandId && (value.state === "pending" || value.state === "running")) {
        await execute(() => api.applicationCommand(value.commandId!), (next) => { if (!cancelled) setCommand(next); });
        if (!cancelled) setReload((current) => current + 1);
      }
    }).catch((cause) => { if (!cancelled) { setSaved(null); setCommand(null); setError(userError(language, cause)); } });
    return () => { cancelled = true; };
  }, [serviceId, language, execute, reload]);
  const active = busy || command?.state === "pending" || command?.state === "running" || saved?.state === "pending" || saved?.state === "running";
  const empty = !draft.vless && !draft.hy2;
  const changed = saved && (saved.vless !== draft.vless || saved.hy2 !== draft.hy2);
  const save = async () => {
    if (!saved || active || empty) return;
    setBusy(true);
    setError("");
    try {
      const result = await execute(async () => {
        if (command?.reconciliationRequired) {
          await api.retryTaskReconciliation(command.id);
          return api.applicationCommand(command.id);
        }
        return api.configureNodeProtocols(serviceId, draft);
      }, setCommand);
      if (result?.state === "succeeded") {
        setSaved({ ...draft, state: "succeeded" });
        await onUpdated();
      } else if (result?.state === "failed") {
        setError(copy(language, "协议更新未完成，请重试。", "Protocols could not be updated. Try again."));
      }
    } catch (cause) { setError(userError(language, cause)); }
    finally { setBusy(false); }
  };
  return <FieldSet disabled={active}>
    <FieldLegend>{copy(language, "节点协议", "Node protocols")}</FieldLegend>
    <FieldDescription>{copy(language, "默认使用 VLESS。可添加 HY2，订阅地址不变。", "VLESS is enabled by default. Add HY2 without changing your subscription URL.")}</FieldDescription>
    {saved ? <FieldGroup>
      {(["vless", "hy2"] as const).map((protocol) => <Field key={protocol} orientation="horizontal" data-invalid={empty} data-disabled={active}>
        <Checkbox id={`${serviceId}-${protocol}`} checked={draft[protocol]} disabled={active || command?.reconciliationRequired} aria-invalid={empty} onCheckedChange={(checked) => setDraft((current) => ({ ...current, [protocol]: checked }))} />
        <FieldLabel htmlFor={`${serviceId}-${protocol}`}>{protocol.toUpperCase()}</FieldLabel>
      </Field>)}
      {draft.hy2 ? <FieldDescription>{copy(language, "HY2 需要放行节点的 UDP 443。TLS 证书由系统自动申请并续期。", "HY2 needs UDP 443 allowed on the node. TLS certificates are requested and renewed automatically.")}</FieldDescription> : null}
      {draft.hy2 ? <FieldDescription>{copy(language, "HY2 使用客户端套餐；原 VLESS 节点套餐仍只计算 VLESS 流量。", "HY2 uses client quotas. The existing VLESS node plan counts VLESS traffic only.")}</FieldDescription> : null}
      {empty ? <FieldError>{copy(language, "至少选择一种协议。", "Select at least one protocol.")}</FieldError> : null}
      <Button className="w-fit" disabled={active || empty || (!changed && saved.state !== "failed" && command?.state !== "failed")} onClick={() => void save()} type="button" variant="outline">
        {active ? <Spinner data-icon="inline-start" /> : null}{active ? copy(language, "正在更新协议…", "Updating protocols…") : copy(language, "保存协议", "Save protocols")}
      </Button>
    </FieldGroup> : !error ? <FieldDescription role="status">{copy(language, "正在读取…", "Loading…")}</FieldDescription> : null}
    {error ? <FieldError role="alert">{error}</FieldError> : null}
    {!saved && error ? <Button className="w-fit" type="button" variant="outline" onClick={() => setReload((current) => current + 1)}>{copy(language, "重试", "Retry")}</Button> : null}
  </FieldSet>;
}
