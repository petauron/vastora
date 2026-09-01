import { useEffect, useRef, useState } from "react";
import { CircleArrowUpIcon, CircleCheckIcon, RotateCcwIcon, ShieldCheckIcon } from "lucide-react";
import { api } from "../api";
import type { CenterUpdateStatus } from "../types";
import type { Language } from "../translations";
import { copy, formatDate, userError } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { FieldError } from "@/components/ui/field";
import { Progress, ProgressLabel } from "@/components/ui/progress";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";

const reloadPage = () => window.location.reload();
const updateStages: Readonly<Record<string, readonly [string, string]>> = {
  queued: ["等待主机更新服务", "Waiting for the host update service"],
  downloading: ["正在下载安装元数据", "Downloading release metadata"],
  verifying: ["正在校验不可变版本", "Verifying the immutable release"],
  installing: ["正在安装已验证版本", "Installing the verified release"],
  validating: ["正在检查现有安装", "Validating the existing installation"],
  pulling: ["正在下载 Center 镜像", "Downloading the Center image"],
  agent: ["正在更新同机 Agent", "Updating the co-located Agent"],
  restarting: ["正在重启 Center", "Restarting Center"],
  health: ["正在等待健康检查", "Waiting for health checks"],
  reconciling: ["正在完成启动协调", "Finishing startup reconciliation"],
  finalizing: ["正在完成最终检查", "Finishing final checks"],
};

export function CenterUpdateCard({ language, onRefresh, onReload = reloadPage, onStatusChange, status }: { language: Language; onRefresh: () => Promise<void>; onReload?: () => void; onStatusChange: (status: CenterUpdateStatus) => void; status: CenterUpdateStatus }) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const reloadStarted = useRef(false);
  const running = status.state === "queued" || status.state === "applying";
  const stageCopy = updateStages[status.phase || (status.state === "queued" ? "queued" : "installing")] || updateStages.installing;
  const updateStage = copy(language, stageCopy[0], stageCopy[1]);

  useEffect(() => {
    if (!running) return;
    let stopped = false;
    let timer = 0;
    const poll = async () => {
      try {
        const next = await api.centerUpdate();
        if (stopped) return;
        if (next.state === "succeeded") {
          if (reloadStarted.current) return;
          reloadStarted.current = true;
          try { await onRefresh(); } finally { onReload(); }
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
  }, [onRefresh, onReload, onStatusChange, running]);

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
      <CardHeader><CardTitle className="flex items-center gap-2"><CircleArrowUpIcon />{copy(language, "Center 更新", "Center update")}</CardTitle><CardDescription>{copy(language, "从当前安装配置的发布源检查完整版本，并沿用安全升级流程。", "Checks complete releases from this installation's configured source and reuses the verified upgrade flow.")}</CardDescription><CardAction>{running ? <Spinner /> : status.updateAvailable ? <span className="rounded-full bg-primary/10 px-2.5 py-1 text-xs font-medium text-primary">{copy(language, "有新版本", "Update available")}</span> : <CircleCheckIcon className="size-5 text-success" />}</CardAction></CardHeader>
      <CardContent className="flex flex-col gap-4">
        <dl className="grid gap-4 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">{copy(language, "当前版本", "Current version")}</dt><dd className="mt-1 font-medium">{status.currentVersion}</dd></div><div><dt className="text-muted-foreground">{copy(language, "可用版本", "Available version")}</dt><dd className="mt-1 font-medium">{status.latestVersion || "—"}</dd></div></dl>
        {running ? <Alert><Spinner /><AlertTitle>{copy(language, `正在更新到 ${status.targetVersion || status.latestVersion}`, `Updating to ${status.targetVersion || status.latestVersion}`)}</AlertTitle><AlertDescription className="flex flex-col gap-3"><span>{copy(language, "Center 会短暂重启，本页会自动重新连接。请不要关闭服务器或 Docker。", "Center briefly restarts and this page reconnects automatically. Do not stop the server or Docker.")}</span><Progress value={status.progress ?? null}><ProgressLabel>{updateStage}</ProgressLabel><span aria-hidden="true" className="ml-auto text-xs text-muted-foreground tabular-nums">{status.progress !== undefined ? `${status.progress}%` : copy(language, "进行中", "In progress")}</span></Progress></AlertDescription></Alert> : null}
        {status.state === "succeeded" && !status.updateAvailable ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "更新完成", "Update complete")}</AlertTitle><AlertDescription>{copy(language, `Center 已安全更新到 ${status.currentVersion}。`, `Center was safely updated to ${status.currentVersion}.`)}</AlertDescription></Alert> : null}
        {status.state === "failed" ? <FieldError role="alert">{copy(language, "更新没有完成。系统保留了可诊断状态，请重试；若仍失败，请下载诊断报告。", "The update did not finish. Diagnostic state was preserved; retry, then download diagnostics if it still fails.")}</FieldError> : null}
        {!status.releaseCheckAvailable ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "发布检查未配置", "Release checking is not configured")}</AlertTitle><AlertDescription>{copy(language, "当前打包没有提供发布元数据与不可变安装源。Center 不会连接任何默认外部服务；请按该打包方的升级说明操作。", "This package did not provide release metadata and an immutable installer source. Center will not contact a default external service; follow the package maintainer's upgrade instructions.")}</AlertDescription></Alert> : status.error ? <FieldError role="alert">{copy(language, "暂时无法检查配置的发布源，请稍后重试。", "The configured release source cannot be checked right now. Try again shortly.")}</FieldError> : null}
        {status.updateAvailable && !status.automatic ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "主机更新服务未安装", "The host update service is not installed")}</AlertTitle><AlertDescription>{copy(language, "请按当前打包方的安装说明更新一次并启用主机更新服务，之后即可在这里更新。", "Follow this package maintainer's installation instructions to update once and enable the host updater. Future releases can then be installed here.")}</AlertDescription></Alert> : null}
        {error ? <FieldError role="alert">{error}</FieldError> : null}
        {status.checkedAt ? <p className="text-xs text-muted-foreground">{copy(language, "上次检查", "Last checked")}：{formatDate(language, status.checkedAt)}</p> : null}
      </CardContent>
      <CardFooter className="justify-end gap-2"><Button disabled={busy || running || !status.releaseCheckAvailable} onClick={() => void refresh()} size="sm" variant="outline">{busy && !confirming ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}{copy(language, "检查更新", "Check again")}</Button>{status.updateAvailable && status.automatic ? <Button disabled={busy || running} onClick={() => setConfirming(true)} size="sm">{copy(language, status.state === "failed" ? "重试更新" : "更新 Center", status.state === "failed" ? "Retry update" : "Update Center")}</Button> : null}</CardFooter>
    </Card>
    <Sheet onOpenChange={(open) => { if (!open && !busy) setConfirming(false); }} open={confirming}><SheetContent><SheetHeader><SheetTitle>{copy(language, `更新到 ${status.latestVersion}`, `Update to ${status.latestVersion}`)}</SheetTitle><SheetDescription>{copy(language, "更新会保留配置与数据，并在迁移数据库前创建备份。Center 和同机 Agent 会按顺序更新。", "Configuration and data are preserved, with a backup before database migration. Center and a co-located Agent update in order.")}</SheetDescription></SheetHeader><div className="flex-1 px-4"><Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "预计短暂断开连接", "Expect a brief reconnect")}</AlertTitle><AlertDescription>{copy(language, "升级过程会校验下载摘要、等待健康检查；数据库一旦迁移不会自动降级。", "The upgrade verifies download integrity and waits for health checks. A migrated database is never downgraded automatically.")}</AlertDescription></Alert>{error ? <FieldError className="mt-4" role="alert">{error}</FieldError> : null}</div><SheetFooter><Button disabled={busy} onClick={() => setConfirming(false)} variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy} onClick={() => void start()}>{busy ? <Spinner data-icon="inline-start" /> : <CircleArrowUpIcon data-icon="inline-start" />}{copy(language, "开始更新", "Start update")}</Button></SheetFooter></SheetContent></Sheet>
  </>;
}
