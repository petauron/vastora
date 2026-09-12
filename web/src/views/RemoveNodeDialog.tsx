import { useState, type FormEvent } from "react";
import { AlertDialog, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { api } from "../api";
import type { Mutate } from "../App";
import type { AgentView } from "../types";
import type { Language } from "../translations";
import { copy, userError } from "./shared";

export function RemoveNodeDialog({ agent, language, mutate, onClose }: {
  agent: AgentView; language: Language; mutate: Mutate; onClose: () => void;
}) {
  const [confirmation, setConfirmation] = useState("");
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const existing = Boolean(agent.removal);
  const pending = agent.removal?.state === "pending";
  const nameMatches = existing || confirmation.trim() === agent.name.trim();
  const invalidName = !existing && touched && !nameMatches;
  const failureMessage = agent.removal?.reason === "controller_unavailable"
    ? copy(language, "订阅主机暂时离线，恢复后请重试。", "The subscription host is offline. Retry when it is back online.")
    : agent.removal?.reason === "shared_service"
      ? copy(language, "此节点仍提供共享服务，请调整相关节点的配置后重试。", "This node still provides a shared service. Update the dependent nodes before retrying.")
      : copy(language, "移除尚未完成，请重试。已完成的清理会保留。", "Removal did not finish. Retry to continue the remaining cleanup.");
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy || pending || agent.connected || !nameMatches) return;
    setBusy(true); setError("");
    try {
      await mutate(() => api.removeOfflineAgent(agent.id, existing ? agent.name : confirmation.trim()), copy(language, "正在移除节点，完成后将从列表中消失。", "Removing the node. It will disappear from the list when cleanup finishes."));
      onClose();
    } catch (cause) { setError(userError(language, cause)); }
    finally { setBusy(false); }
  };
  return <AlertDialog open onOpenChange={(open) => { if (!open && !busy) onClose(); }}>
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>{copy(language, "永久移除节点", "Permanently remove node")}</AlertDialogTitle>
        <AlertDialogDescription>{copy(language, `将从 Vastora 移除“${agent.name}”，清理应用安装记录、等待任务、订阅节点和专属入口。此操作无法撤销。离线服务器上的程序和数据不会被删除。`, `Remove “${agent.name}” from Vastora, including its app records, pending tasks, subscription nodes and dedicated access entries. This cannot be undone. Programs and data on the offline server will not be deleted.`)}</AlertDialogDescription>
      </AlertDialogHeader>
      <form className="flex flex-col gap-4" onSubmit={(event) => void submit(event)}>
        <FieldGroup>
          {!existing ? <Field data-invalid={invalidName} data-disabled={busy}>
            <FieldLabel htmlFor="remove-node-name">{copy(language, `输入“${agent.name}”确认`, `Type “${agent.name}” to confirm`)}</FieldLabel>
            <Input aria-describedby={invalidName ? "remove-node-name-error" : undefined} aria-invalid={invalidName} autoCapitalize="none" autoComplete="off" autoCorrect="off" autoFocus disabled={busy} id="remove-node-name" onBlur={() => setTouched(true)} onChange={(event) => setConfirmation(event.target.value)} spellCheck={false} value={confirmation} />
            {invalidName ? <FieldError id="remove-node-name-error">{copy(language, "节点名称不一致。", "The node name does not match.")}</FieldError> : null}
          </Field> : null}
          {pending ? <p role="status" className="flex items-center gap-2 text-sm"><Spinner />{copy(language, "正在清理关联记录，可以关闭此窗口。", "Cleaning up related records. You can close this window.")}</p> : null}
          {agent.removal?.state === "failed" ? <FieldError role="alert">{failureMessage}</FieldError> : null}
          {agent.connected ? <FieldError role="alert">{copy(language, "节点已上线，请关闭后重新检查。", "The node is online. Close this window and check its status.")}</FieldError> : null}
          {error ? <FieldError role="alert">{error}</FieldError> : null}
        </FieldGroup>
        <AlertDialogFooter>
          <Button disabled={busy} onClick={onClose} type="button" variant="outline">{existing ? copy(language, "关闭", "Close") : copy(language, "取消", "Cancel")}</Button>
          {!pending ? <Button disabled={busy || agent.connected || !nameMatches} type="submit" variant="destructive">{busy ? <Spinner data-icon="inline-start" /> : null}{existing ? copy(language, "重试", "Retry") : copy(language, "永久移除", "Permanently remove")}</Button> : null}
        </AlertDialogFooter>
      </form>
    </AlertDialogContent>
  </AlertDialog>;
}
