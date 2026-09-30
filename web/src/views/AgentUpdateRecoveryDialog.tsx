import { useRef, useState, type FormEvent } from "react";
import { AlertDialog, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { api } from "../api";
import type { Mutate } from "../App";
import type { AgentView } from "../types";
import type { Language } from "../translations";
import { copy, userError } from "./shared";

export function AgentUpdateRecoveryDialog({ agent, failedUpdateId, targetVersion, language, mutate, onClose }: {
  agent: AgentView; failedUpdateId: string; targetVersion: string; language: Language; mutate: Mutate; onClose: () => void;
}) {
  const [stopped, setStopped] = useState(false);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submitting = useRef(false);
  const current = agent.update?.id === failedUpdateId && agent.update.state === "failed";
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (submitting.current || !current || !agent.connected || !stopped || !note.trim()) return;
    submitting.current = true;
    setBusy(true); setError("");
    try {
      await mutate(() => api.recoverAgentUpdate(agent.id, failedUpdateId, note.trim()), copy(language, "正式版更新已排队，可在节点列表查看进度。", "The release update is queued. View progress in the node list."));
      onClose();
    } catch (cause) { setError(userError(language, cause)); }
    finally { submitting.current = false; setBusy(false); }
  };
  return <AlertDialog open onOpenChange={(open) => { if (!open && !busy) onClose(); }}>
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>{copy(language, `恢复更新 · ${agent.name}`, `Resume update · ${agent.name}`)}</AlertDialogTitle>
        <AlertDialogDescription>{copy(language, `确认后更新到 ${targetVersion}。旧失败记录保留；节点会短暂离线并自动重新连接。`, `Update to ${targetVersion} after confirmation. The failed record is retained; the node briefly disconnects and reconnects automatically.`)}</AlertDialogDescription>
      </AlertDialogHeader>
      <form className="flex flex-col gap-4" onSubmit={(event) => void submit(event)}>
        <FieldGroup>
          {agent.update?.lastError ? <p className="break-words text-xs text-muted-foreground">{agent.update.lastError}</p> : null}
          <Field orientation="horizontal" data-disabled={busy}>
            <Checkbox id="update-recovery-stopped" checked={stopped} disabled={busy} onCheckedChange={setStopped} />
            <FieldLabel htmlFor="update-recovery-stopped">{copy(language, "已确认旧更新停止，并核对节点状态", "I verified the old updater stopped and inspected the node")}</FieldLabel>
          </Field>
          <Field data-disabled={busy}>
            <FieldLabel htmlFor="update-recovery-note">{copy(language, "核对说明", "Verification notes")}</FieldLabel>
            <Textarea disabled={busy} id="update-recovery-note" maxLength={1024} onChange={(event) => setNote(event.target.value)} required value={note} />
          </Field>
          {!current ? <FieldError role="alert">{copy(language, "任务状态已变化，请关闭后重新检查。", "The task changed. Close this dialog and check again.")}</FieldError> : null}
          {!agent.connected ? <FieldError role="alert">{copy(language, "节点离线，重新连接后才能更新。", "The node must reconnect before updating.")}</FieldError> : null}
          {error ? <FieldError role="alert">{error}</FieldError> : null}
        </FieldGroup>
        <AlertDialogFooter>
          <Button disabled={busy} onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button>
          <Button disabled={busy || !current || !agent.connected || !stopped || !note.trim()} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "确认并更新", "Confirm and update")}</Button>
        </AlertDialogFooter>
      </form>
    </AlertDialogContent>
  </AlertDialog>;
}
