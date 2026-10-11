import { useEffect, useState } from "react";
import { api } from "../api";
import type { ExecutionView } from "../execution-types";
import type { Language } from "../translations";
import { ExecutionDetail } from "./ExecutionSettings";
import { copy, userError } from "./shared";
import { Button } from "@/components/ui/button";
import { FieldError } from "@/components/ui/field";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";

// Read the exact latest attempt; opening recovery never queues a mutation.
// Reuse Activity's evidence-gated operator decisions instead of the retired
// reconciliation retry path or a client-side success/failure inference.
export function TaskRecoverySheet({ taskId, agentId, name, language, onClose, onSaved }: {
  taskId: string; agentId: string; name: string; language: Language; onClose: () => void; onSaved: () => void;
}) {
  const [execution, setExecution] = useState<ExecutionView | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    const request = new AbortController();
    void api.taskExecution(taskId, request.signal).then((value) => {
      if (request.signal.aborted) return;
      if (value.taskId !== taskId || value.agentId !== agentId || value.kind !== "application.apply") {
        setError(copy(language, "执行记录与当前应用任务不匹配，请在活动中核对。", "The execution does not match this app task. Review it in Activity."));
        return;
      }
      setExecution(value);
    }).catch((cause) => { if (!request.signal.aborted) setError(userError(language, cause)); });
    return () => request.abort();
  }, [taskId, agentId, language]);
  if (execution) return <ExecutionDetail key={execution.id} execution={execution} name={name} language={language} onClose={onClose} onSaved={onSaved} />;
  return <Sheet open onOpenChange={(open) => { if (!open) onClose(); }}><SheetContent>
    <SheetHeader><SheetTitle>{copy(language, "核对应用任务", "Review app task")}</SheetTitle><SheetDescription>{name}</SheetDescription></SheetHeader>
    {error ? <FieldError className="px-4" role="alert">{error}</FieldError> : <p className="flex items-center gap-2 px-4" role="status"><Spinner />{copy(language, "正在读取最新执行记录", "Loading the latest execution")}</p>}
    <SheetFooter><Button variant="outline" onClick={onClose}>{copy(language, "返回", "Back")}</Button></SheetFooter>
  </SheetContent></Sheet>;
}
