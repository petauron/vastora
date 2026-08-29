import { useEffect, useState } from "react";
import { CircleArrowUpIcon, CircleCheckIcon, RotateCcwIcon, ShieldCheckIcon } from "lucide-react";
import { api } from "../api";
import type { CenterUpdateStatus } from "../types";
import type { Language } from "../translations";
import { CopyButton, copy, formatDate, userError } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { FieldError } from "@/components/ui/field";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";

const manualCommand = "curl -LsSf https://vastora.petauron.com/install.sh | sudo sh -s -- center";

export function CenterUpdateCard({ language, onRefresh, onStatusChange, status }: { language: Language; onRefresh: () => Promise<void>; onStatusChange: (status: CenterUpdateStatus) => void; status: CenterUpdateStatus }) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const running = status.state === "queued" || status.state === "applying";

  useEffect(() => {
    if (!running) return;
    let stopped = false;
    let timer = 0;
    const poll = async () => {
      try {
        const next = await api.centerUpdate();
        if (stopped) return;
        if (next.state === "succeeded") {
          await onRefresh();
          return;
        }
        onStatusChange(next);
      } catch {
        // Center restarts during a normal update. Keep the progress state and retry.
      }
      if (!stopped) timer = window.setTimeout(() => void poll(), 2000);
    };
    void poll();
    return () => { stopped = true; window.clearTimeout(timer); };
  }, [onRefresh, onStatusChange, running]);

  const refresh = async () => {
    setBusy(true); setError("");
    try { onStatusChange(await api.centerUpdate(true)); } catch (refreshError) { setError(userError(language, refreshError)); } finally { setBusy(false); }
  };
  const start = async () => {
    setBusy(true); setError("");
    try { onStatusChange(await api.startCenterUpdate()); setConfirming(false); } catch (updateError) { setError(userError(language, updateError)); } finally { setBusy(false); }
  };

  return <>
    <Card>
      <CardHeader><CardTitle className="flex items-center gap-2"><CircleArrowUpIcon />{copy(language, "Center 更新", "Center update")}</CardTitle><CardDescription>{copy(language, "自动检查官方完整版本，并沿用安装时的安全升级流程。", "Checks complete official releases and reuses the verified installation upgrade flow.")}</CardDescription><CardAction>{running ? <Spinner /> : status.updateAvailable ? <span className="rounded-full bg-primary/10 px-2.5 py-1 text-xs font-medium text-primary">{copy(language, "有新版本", "Update available")}</span> : <CircleCheckIcon className="size-5 text-success" />}</CardAction></CardHeader>
      <CardContent className="flex flex-col gap-4">
        <dl className="grid gap-4 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">{copy(language, "当前版本", "Current version")}</dt><dd className="mt-1 font-medium">{status.currentVersion}</dd></div><div><dt className="text-muted-foreground">{copy(language, "官方版本", "Official version")}</dt><dd className="mt-1 font-medium">{status.latestVersion || "—"}</dd></div></dl>
        {running ? <Alert><Spinner /><AlertTitle>{copy(language, `正在更新到 ${status.targetVersion || status.latestVersion}`, `Updating to ${status.targetVersion || status.latestVersion}`)}</AlertTitle><AlertDescription>{copy(language, "Center 会短暂重启，本页会自动重新连接。请不要关闭服务器或 Docker。", "Center briefly restarts and this page reconnects automatically. Do not stop the server or Docker.")}</AlertDescription></Alert> : null}
        {status.state === "succeeded" && !status.updateAvailable ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "更新完成", "Update complete")}</AlertTitle><AlertDescription>{copy(language, `Center 已安全更新到 ${status.currentVersion}。`, `Center was safely updated to ${status.currentVersion}.`)}</AlertDescription></Alert> : null}
        {status.state === "failed" ? <FieldError role="alert">{copy(language, "更新没有完成。系统保留了可诊断状态，请重试；若仍失败，请下载诊断报告。", "The update did not finish. Diagnostic state was preserved; retry, then download diagnostics if it still fails.")}</FieldError> : null}
        {status.error ? <FieldError role="alert">{copy(language, "暂时无法检查官方版本，请稍后重试。", "The official release cannot be checked right now. Try again shortly.")}</FieldError> : null}
        {status.updateAvailable && !status.automatic ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "当前安装需要先手动更新一次", "One manual update is required")}</AlertTitle><AlertDescription><p>{copy(language, "运行一次下面的官方命令后，后续版本即可在这里更新。", "Run the official command once; future releases can then be installed here.")}</p><div className="relative mt-3"><code className="block break-all rounded-lg bg-muted p-3 pr-12 text-xs leading-5">{manualCommand}</code><CopyButton className="absolute right-1.5 top-1.5" label={copy(language, "复制更新命令", "Copy update command")} language={language} size="icon-sm" value={manualCommand} /></div></AlertDescription></Alert> : null}
        {error ? <FieldError role="alert">{error}</FieldError> : null}
        {status.checkedAt ? <p className="text-xs text-muted-foreground">{copy(language, "上次检查", "Last checked")}：{formatDate(language, status.checkedAt)}</p> : null}
      </CardContent>
      <CardFooter className="justify-end gap-2"><Button disabled={busy || running} onClick={() => void refresh()} size="sm" variant="outline">{busy && !confirming ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}{copy(language, "检查更新", "Check again")}</Button>{status.updateAvailable && status.automatic ? <Button disabled={busy || running} onClick={() => setConfirming(true)} size="sm">{copy(language, status.state === "failed" ? "重试更新" : "更新 Center", status.state === "failed" ? "Retry update" : "Update Center")}</Button> : null}</CardFooter>
    </Card>
    <Sheet onOpenChange={(open) => { if (!open && !busy) setConfirming(false); }} open={confirming}><SheetContent><SheetHeader><SheetTitle>{copy(language, `更新到 ${status.latestVersion}`, `Update to ${status.latestVersion}`)}</SheetTitle><SheetDescription>{copy(language, "更新会保留配置与数据，并在迁移数据库前创建备份。Center 和同机 Agent 会按顺序更新。", "Configuration and data are preserved, with a backup before database migration. Center and a co-located Agent update in order.")}</SheetDescription></SheetHeader><div className="flex-1 px-4"><Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "预计短暂断开连接", "Expect a brief reconnect")}</AlertTitle><AlertDescription>{copy(language, "升级过程会校验下载摘要、等待健康检查；数据库一旦迁移不会自动降级。", "The upgrade verifies download integrity and waits for health checks. A migrated database is never downgraded automatically.")}</AlertDescription></Alert>{error ? <FieldError className="mt-4" role="alert">{error}</FieldError> : null}</div><SheetFooter><Button disabled={busy} onClick={() => setConfirming(false)} variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy} onClick={() => void start()}>{busy ? <Spinner data-icon="inline-start" /> : <CircleArrowUpIcon data-icon="inline-start" />}{copy(language, "开始更新", "Start update")}</Button></SheetFooter></SheetContent></Sheet>
  </>;
}
