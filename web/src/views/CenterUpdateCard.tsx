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
const shortDigest = (digest?: string) => digest ? `sha256:${digest.slice(0, 12)}` : "—";
const updateStages: Readonly<Record<string, readonly [string, string]>> = {
  queued: ["等待主机更新服务", "Waiting for the host update service"],
  downloading: ["正在下载安装元数据", "Downloading release metadata"],
  verifying: ["正在校验不可变版本", "Verifying the immutable release"],
  installing: ["正在安装已验证版本", "Installing the verified release"],
  validating: ["正在检查现有安装", "Validating the existing installation"],
  pulling: ["正在下载 Center 镜像", "Downloading the Center image"],
  agent: ["正在更新节点 Agent", "Updating node Agents"],
  restarting: ["正在重启 Center", "Restarting Center"],
  health: ["正在等待健康检查", "Waiting for health checks"],
  reconciling: ["正在完成启动协调", "Finishing startup reconciliation"],
  finalizing: ["正在完成最终检查", "Finishing final checks"],
};

const updateIsRunning = (status: CenterUpdateStatus) => status.state === "queued" || status.state === "applying";

export function CenterUpdateCard({ language, onRefresh, onReload = reloadPage, onStatusChange, status }: { language: Language; onRefresh: () => Promise<void>; onReload?: () => void; onStatusChange: (status: CenterUpdateStatus) => void; status: CenterUpdateStatus }) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const reloadStarted = useRef(false);
  const running = updateIsRunning(status);
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
        onStatusChange(next);
        if (running && next.state === "succeeded") {
          if (reloadStarted.current) return;
          reloadStarted.current = true;
          try { await onRefresh(); } finally { onReload(); }
          return;
        }
        if (!updateIsRunning(next)) return;
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
      <CardHeader><CardTitle className="flex items-center gap-2"><CircleArrowUpIcon />{copy(language, "Center 更新", "Center update")}</CardTitle><CardDescription>{copy(language, "从当前安装配置的发布源检查完整版本，并沿用安全升级流程。", "Checks complete releases from this installation's configured source and reuses the verified upgrade flow.")}</CardDescription><CardAction>{running ? <Spinner /> : status.imageMismatch ? <span className="rounded-full bg-destructive/10 px-2.5 py-1 text-xs font-medium text-destructive">{copy(language, "镜像待修复", "Image repair needed")}</span> : status.updateAvailable ? <span className="rounded-full bg-primary/10 px-2.5 py-1 text-xs font-medium text-primary">{copy(language, "有新版本", "Update available")}</span> : <CircleCheckIcon className="size-5 text-success" />}</CardAction></CardHeader>
      <CardContent className="flex flex-col gap-4">
        <dl className="grid gap-4 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">{copy(language, "当前版本", "Current version")}</dt><dd className="mt-1 flex flex-wrap items-center gap-2 font-medium"><span>{status.currentVersion}</span><code className="rounded-md bg-muted px-1.5 py-0.5 text-xs font-normal text-muted-foreground" title={status.currentImageDigest ? `sha256:${status.currentImageDigest}` : undefined}>{shortDigest(status.currentImageDigest)}</code></dd></div><div><dt className="text-muted-foreground">{copy(language, "可用版本", "Available version")}</dt><dd className="mt-1 flex flex-wrap items-center gap-2 font-medium"><span>{status.latestVersion || "—"}</span><code className="rounded-md bg-muted px-1.5 py-0.5 text-xs font-normal text-muted-foreground" title={status.latestImageDigest ? `sha256:${status.latestImageDigest}` : undefined}>{shortDigest(status.latestImageDigest)}</code></dd></div></dl>
        {status.imageMismatch ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "版本相同，但镜像不一致", "Version matches, but the image does not")}</AlertTitle><AlertDescription>{copy(language, "当前安装的镜像不是该版本正式发布的镜像。可直接执行修复更新，无需等待新版本。", "The installed image is not the image officially published for this version. Run a repair update without waiting for another version.")}</AlertDescription></Alert> : null}
        {running ? <Alert><Spinner /><AlertTitle>{copy(language, `正在更新到 ${status.targetVersion || status.latestVersion}`, `Updating to ${status.targetVersion || status.latestVersion}`)}</AlertTitle><AlertDescription className="flex flex-col gap-3"><span>{copy(language, "Center 会短暂重启，本页会自动重新连接。请不要关闭服务器或 Docker。", "Center briefly restarts and this page reconnects automatically. Do not stop the server or Docker.")}</span><Progress value={status.progress ?? null}><ProgressLabel>{updateStage}</ProgressLabel><span aria-hidden="true" className="ml-auto text-xs text-muted-foreground tabular-nums">{status.progress !== undefined ? `${status.progress}%` : copy(language, "进行中", "In progress")}</span></Progress></AlertDescription></Alert> : null}
        {status.state === "succeeded" && !status.updateAvailable ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "Center 更新完成", "Center update complete")}</AlertTitle><AlertDescription className="flex flex-col gap-3"><span>{copy(language, `Center 已安全更新到 ${status.currentVersion}。请在下方 Agent 更新中选择节点并单独推送。`, `Center was safely updated to ${status.currentVersion}. Choose targets and push Agent updates separately below.`)}</span></AlertDescription></Alert> : null}
        {status.state === "failed" ? <FieldError role="alert">{copy(language, "更新没有完成。系统保留了可诊断状态，请重试；若仍失败，请下载诊断报告。", "The update did not finish. Diagnostic state was preserved; retry, then download diagnostics if it still fails.")}</FieldError> : null}
        {!status.releaseCheckAvailable ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "发布检查未配置", "Release checking is not configured")}</AlertTitle><AlertDescription>{copy(language, "当前打包没有提供发布元数据与不可变安装源。Center 不会连接任何默认外部服务；请按该打包方的升级说明操作。", "This package did not provide release metadata and an immutable installer source. Center will not contact a default external service; follow the package maintainer's upgrade instructions.")}</AlertDescription></Alert> : status.error ? <FieldError role="alert">{copy(language, "暂时无法检查配置的发布源，请稍后重试。", "The configured release source cannot be checked right now. Try again shortly.")}</FieldError> : null}
        {status.updateAvailable && !status.automatic ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "主机更新服务未安装", "The host update service is not installed")}</AlertTitle><AlertDescription>{copy(language, "请按当前打包方的安装说明更新一次并启用主机更新服务，之后即可在这里更新。", "Follow this package maintainer's installation instructions to update once and enable the host updater. Future releases can then be installed here.")}</AlertDescription></Alert> : null}
        {error ? <FieldError role="alert">{error}</FieldError> : null}
        {status.checkedAt ? <p className="text-xs text-muted-foreground">{copy(language, "上次检查", "Last checked")}：{formatDate(language, status.checkedAt)}</p> : null}
      </CardContent>
      <CardFooter className="justify-end gap-2"><Button disabled={busy || running || !status.releaseCheckAvailable} onClick={() => void refresh()} size="sm" variant="outline">{busy && !confirming ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}{copy(language, "检查更新", "Check again")}</Button>{status.updateAvailable && status.automatic ? <Button disabled={busy || running} onClick={() => setConfirming(true)} size="sm">{copy(language, status.state === "failed" ? "重试更新" : status.imageMismatch ? "修复镜像" : "更新 Center", status.state === "failed" ? "Retry update" : status.imageMismatch ? "Repair image" : "Update Center")}</Button> : null}</CardFooter>
    </Card>
    <Sheet onOpenChange={(open) => { if (!open && !busy) setConfirming(false); }} open={confirming}><SheetContent><SheetHeader><SheetTitle>{copy(language, status.imageMismatch ? `修复 ${status.latestVersion} 镜像` : `更新到 ${status.latestVersion}`, status.imageMismatch ? `Repair the ${status.latestVersion} image` : `Update to ${status.latestVersion}`)}</SheetTitle><SheetDescription>{copy(language, "仅更新 Center，保留配置与数据并自动备份。同机及其他 Agent 均需单独选择推送；更新前的业务继续运行，旧 Agent 的新管理任务可能等待版本同步。", "Updates Center only, preserving configuration and data with a backup. Select and push co-located and other Agents separately. Existing services continue; new management tasks may require Agent version synchronization.")}</SheetDescription></SheetHeader><div className="flex-1 px-4"><Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "预计短暂断开连接", "Expect a brief reconnect")}</AlertTitle><AlertDescription>{copy(language, "更新期间会短暂断开连接，完成后自动恢复。请勿关闭服务器。更新后的数据不支持自动恢复到旧版本。", "Connections briefly disconnect and recover automatically. Keep the server running. Updated data cannot automatically revert to an older version.")}</AlertDescription></Alert>{error ? <FieldError className="mt-4" role="alert">{error}</FieldError> : null}</div><SheetFooter><Button disabled={busy} onClick={() => setConfirming(false)} variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy} onClick={() => void start()}>{busy ? <Spinner data-icon="inline-start" /> : <CircleArrowUpIcon data-icon="inline-start" />}{copy(language, status.imageMismatch ? "开始修复" : "开始更新", status.imageMismatch ? "Start repair" : "Start update")}</Button></SheetFooter></SheetContent></Sheet>
  </>;
}
