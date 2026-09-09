import { useState } from "react";
import type { Mutate } from "../App";
import { api } from "../api";
import type { ApplicationCommand, Service } from "../types";
import type { Language } from "../translations";
import { useApplicationCommandExecutor } from "../hooks/use-application-command-executor";
import { AlertDialog, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { copy, userError } from "./shared";

export function RealityRemoveDialog({ service, language, mutate, onClose }: {
  service: Service; language: Language; mutate: Mutate; onClose: () => void;
}) {
  const [command, setCommand] = useState<ApplicationCommand | null>(null);
  const [error, setError] = useState("");
  const { execute, running } = useApplicationCommandExecutor(service.id);
  const [refreshing, setRefreshing] = useState(false);
  const busy = running || refreshing;
  const succeeded = command?.state === "succeeded";
  const remove = async () => {
    setError("");
    try {
      const result = await execute(async () => {
        const current = await api.removeRealityCommand(service.id);
        if (!current.reconciliationRequired) return current;
        await api.retryTaskReconciliation(current.id);
        return api.applicationCommand(current.id);
      }, setCommand);
      if (result?.state === "succeeded") {
        setRefreshing(true);
        await mutate(async () => undefined, copy(language, "本机节点已移除，订阅主机已保留。", "Local node removed. Subscription controller retained."));
        onClose();
      } else if (result?.state === "failed") {
        setError(copy(language, "未能完成移除，请重试。", "Removal could not be completed. Please retry."));
      }
    } catch (cause) {
      setError(userError(language, cause));
    } finally {
      setRefreshing(false);
    }
  };
  return <AlertDialog open onOpenChange={(open) => { if (!open && !busy) onClose(); }}>
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>{copy(language, "移除本机节点？", "Remove local node?")}</AlertDialogTitle>
        <AlertDialogDescription>
          {copy(language, `将移除“${service.displayName || service.name}”及其访问入口。订阅地址、用户和其他节点保留；此节点上的现有连接会断开。`, `Remove “${service.displayName || service.name}” and its access points. Subscription URLs, users, and other nodes are kept. Existing connections to this node will disconnect.`)}
        </AlertDialogDescription>
      </AlertDialogHeader>
      {busy ? <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground"><Spinner />{copy(language, "正在移除节点…", "Removing node…")}</p> : null}
      {error ? <p role="alert" className="text-sm text-destructive">{error}</p> : null}
      {succeeded && !busy ? <p role="status" className="text-sm">{copy(language, "本机节点已移除。", "Local node removed.")}</p> : null}
      <AlertDialogFooter>
        <Button disabled={busy} onClick={onClose} variant="outline">{succeeded ? copy(language, "关闭", "Close") : copy(language, "取消", "Cancel")}</Button>
        {!succeeded ? <Button disabled={busy} onClick={() => void remove()} variant="destructive">{error ? copy(language, "重试", "Retry") : copy(language, "移除节点", "Remove node")}</Button> : null}
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>;
}
