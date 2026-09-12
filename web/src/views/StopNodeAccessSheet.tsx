import { useState, type FormEvent } from "react";
import { UnplugIcon } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { api } from "../api";
import type { Mutate } from "../App";
import type { AgentView } from "../types";
import type { Language } from "../translations";
import { copy, userError } from "./shared";

export function StopNodeAccessSheet({ agent, language, mutate, onClose }: { agent: AgentView; language: Language; mutate: Mutate; onClose: () => void }) {
  const [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const nameMatches = confirmation.trim() === agent.name.trim();
  const invalidName = confirmation.length > 0 && !nameMatches;
  const stateChanged = agent.status !== "active" || agent.connected;
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy || !nameMatches || stateChanged) return;
    setBusy(true);
    setError("");
    try {
      await mutate(() => api.revokeAgentCredential(agent.id), copy(language, "已停止此节点接入，应用和数据已保留。", "Node access stopped. Apps and data were kept."));
      onClose();
    } catch (submitError) {
      setError(userError(language, submitError));
    } finally {
      setBusy(false);
    }
  };

  return <Sheet open onOpenChange={(open) => { if (!open && !busy) onClose(); }}>
    <SheetContent showCloseButton={!busy}>
      <SheetHeader>
        <SheetTitle>{copy(language, "停止节点接入", "Stop node access")}</SheetTitle>
        <SheetDescription>{copy(language, "无需节点在线，也无需先卸载应用。", "The node does not need to be online, and apps do not need to be uninstalled first.")}</SheetDescription>
      </SheetHeader>
      <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}>
        <div className="flex-1 overflow-y-auto px-4">
          <FieldGroup>
            <Alert>
              <UnplugIcon aria-hidden="true" />
              <AlertTitle className="break-words">{agent.name}</AlertTitle>
              <AlertDescription>{copy(language, "旧 Agent 和此前生成的重新接入命令将无法再接入 Center。不会卸载应用、删除记录或清理服务器数据，也不会关闭已有代理服务或私网连接。恢复管理时需重新生成接入命令。", "The old Agent and previously generated reconnect commands will no longer be accepted by Center. This does not uninstall apps, delete records, clean server data, or stop existing proxy services or private-network connections. Generate a new reconnect command to restore management.")}</AlertDescription>
            </Alert>
            <Field data-invalid={invalidName}>
              <FieldLabel htmlFor="stop-node-access-name">{copy(language, `输入“${agent.name}”确认`, `Type “${agent.name}” to confirm`)}</FieldLabel>
              <Input aria-invalid={invalidName} aria-describedby="stop-node-access-name-hint" autoCapitalize="none" autoComplete="off" autoCorrect="off" autoFocus disabled={busy} id="stop-node-access-name" onChange={(event) => setConfirmation(event.target.value)} spellCheck={false} value={confirmation} />
              <FieldDescription id="stop-node-access-name-hint">{copy(language, "使用上方的节点名称，不是订阅名称；空格和连字符不同。", "Use the node name above, not its subscription name. Spaces and hyphens are different.")}</FieldDescription>
              {invalidName ? <FieldError>{copy(language, "名称不一致，请核对后重试。", "The name does not match. Check it and try again.")}</FieldError> : null}
            </Field>
            {stateChanged ? <FieldError role="alert">{copy(language, "节点状态已变化，请关闭后重新操作。", "The node status changed. Close this panel and try again.")}</FieldError> : null}
            {error ? <FieldError role="alert">{error}</FieldError> : null}
          </FieldGroup>
        </div>
        <SheetFooter>
          <Button disabled={busy} onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button>
          <Button disabled={busy || !nameMatches || stateChanged} type="submit" variant="destructive">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "停止接入", "Stop access")}</Button>
        </SheetFooter>
      </form>
    </SheetContent>
  </Sheet>;
}
