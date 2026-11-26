import { useEffect, useId, useState } from "react";
import { ShieldAlertIcon } from "lucide-react";
import { api } from "../api";
import type { NodeDiagnosticCheck } from "../node-diagnostics-types";
import type { Language } from "../translations";
import { StateBadge, copy, formatDate, userError } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";

// Network settings are captured by the Agent; the form only confirms the target.
export function OSReinstallInspection({ nodeId, connected, canReinstall, language }: { nodeId: string; connected: boolean; canReinstall: boolean; language: Language }) {
  const id = useId();
  const [check, setCheck] = useState<NodeDiagnosticCheck | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const [task, setTask] = useState<Awaited<ReturnType<typeof api.osReinstallTask>>["task"]>(null);
  const [username, setUsername] = useState("");
  const [sshPort, setSSHPort] = useState("22");
  const [confirmedDisk, setConfirmedDisk] = useState("");
  const [confirmedErase, setConfirmedErase] = useState(false);

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const controller = new AbortController();
    async function refresh() {
      try {
        const value = await api.osReinstallInspection(nodeId, controller.signal);
        if (cancelled) return;
        setCheck(value.check);
        if (value.check?.state === "pending" || value.check?.state === "running") {
          timer = setTimeout(() => void refresh(), 1500);
        }
      } catch (readError) {
        if (!cancelled) setError(userError(language, readError));
      }
    }
    void refresh();
    return () => { cancelled = true; controller.abort(); if (timer) clearTimeout(timer); };
  }, [nodeId, language, revision]);

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const controller = new AbortController();
    async function refreshTask() {
      try {
        const value = await api.osReinstallTask(nodeId, controller.signal);
        if (cancelled) return;
        setTask(value.task);
        if (value.task && ["pending", "running", "backup_ready", "armed"].includes(value.task.state)) timer = setTimeout(() => void refreshTask(), 3000);
      } catch (readError) { if (!cancelled) setError(userError(language, readError)); }
    }
    void refreshTask();
    return () => { cancelled = true; controller.abort(); if (timer) clearTimeout(timer); };
  }, [nodeId, language, revision]);

  async function reinstall() {
    if (!check?.host?.osReinstall || !confirmedErase || confirmedDisk !== check.host.osReinstall.disk.path || busy) return;
    setBusy(true); setError("");
    try {
      const value = await api.startOSReinstall(nodeId, { inspectionId: check.id, confirmDisk: confirmedDisk, username: username.trim(), sshPort: Number(sshPort), confirmErase: true });
      setTask(value); setConfirmedErase(false); setRevision((v) => v + 1);
    } catch (startError) { setError(userError(language, startError)); }
    finally { setBusy(false); }
  }
  async function recheck() {
    if (busy || !task?.canRecheck) return;
    setBusy(true); setError("");
    try {
      await api.recheckOSReinstall(nodeId);
      await api.startOSReinstallInspection(nodeId);
      setCheck(null); setRevision((value) => value + 1);
    } catch (readError) { setError(userError(language, readError)); }
    finally { setBusy(false); }
  }
  async function inspect() {
    if (busy || !connected) return;
    setBusy(true); setError("");
    try {
      await api.startOSReinstallInspection(nodeId);
      setCheck(null);
      setRevision((value) => value + 1);
    } catch (startError) {
      setError(userError(language, startError));
    } finally {
      setBusy(false);
    }
  }
  const pending = check?.state === "pending" || check?.state === "running";
  const result = check?.host?.osReinstall;
  const supported = !!result && result.architecture === "amd64" && result.bootMode === "bios" && ["netplan_static_ipv4", "ifupdown_static_ipv4"].includes(result.networkCandidate ?? "") && !!result.disk.partitionTableId && (result.missingPrerequisites?.length ?? 0) === 0 && (result.limitations ?? []).every((item) => item === "network_configuration_requires_review");
  const failure = check?.host?.osReinstallError || (check?.state === "failed" ? check.error || "probe_failed" : "");
  const taskStatus = task ? {
    pending: copy(language, "等待执行", "Queued"), running: copy(language, "备份和准备中", "Preparing"),
    backup_ready: copy(language, "备份已确认", "Backup confirmed"), armed: copy(language, "安装中，等待接回", "Installing; waiting for reconnect"),
    succeeded: copy(language, "完成", "Completed"), failed: copy(language, "失败，需要检查", "Failed; review required"),
    unknown: copy(language, "状态未知，需要检查", "Unknown; review required"), cancelled: copy(language, "已取消", "Cancelled"),
  }[task.state] : "";
  const taskBadgeState = task ? ({
    pending: "queued",
    running: "applying",
    backup_ready: "applying",
    armed: "applying",
    succeeded: "succeeded",
    failed: "failed",
    unknown: "action_required",
    cancelled: "cancelled",
  }[task.state] ?? task.state) : null;
  return <section className="rounded-xl border p-4">
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <p className="text-sm font-medium">{copy(language, "系统重装", "OS reinstall")}</p>
          {taskBadgeState ? <StateBadge language={language} value={taskBadgeState} /> : null}
        </div>
        <p className="mt-1 text-xs text-muted-foreground">{copy(language, "检查 Debian 13 重装所需的磁盘与网络信息。", "Inspect disk and network information for Debian 13.")}</p>
      </div>
      <Button disabled={!connected || busy || pending} onClick={() => void inspect()} size="sm" type="button" variant="outline">{busy || pending ? <Spinner data-icon="inline-start" /> : null}{pending ? copy(language, "检查中", "Inspecting") : copy(language, "检查", "Inspect")}</Button>
    </div>
    {result ? <div className="mt-3 space-y-3 rounded-lg border bg-muted/20 p-3 text-xs"><p><span className="text-muted-foreground">{copy(language, "系统盘", "System disk")}</span> {result.disk.path} · {(result.disk.bytes / 1024 ** 3).toFixed(1)} GiB</p>
      {[result.network, ...(result.network.additional ?? [])].map((nic) => <dl key={nic.mac} className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1"><dt className="text-muted-foreground">{copy(language, "网卡", "Interface")}</dt><dd>{nic.interface} · {nic.mac}</dd><dt className="text-muted-foreground">IP</dt><dd className="break-all">{nic.addresses.map((item) => `${item.prefix}${item.dynamic ? copy(language, "（动态）", " (dynamic)") : ""}`).join(", ")}</dd><dt className="text-muted-foreground">{copy(language, "网关", "Gateway")}</dt><dd className="break-all">{(nic.gateways ?? []).map((item) => `${item.address || "—"}${item.onLink ? " · onlink" : ""}${item.dynamic ? copy(language, "（动态）", " (dynamic)") : ""}`).join(", ") || "—"}</dd></dl>)}
      <p className="break-all"><span className="text-muted-foreground">DNS</span> {result.network.dns.join(", ")}</p><p className="text-muted-foreground">{copy(language, "网络配置自动保存并在重装后恢复核验。", "Network settings are captured automatically, restored and verified after reinstall.")}</p>
    </div> : null}

    {result && check?.checkedAt ? <p className="mt-2 text-xs text-muted-foreground">{copy(language, `检查时间：${formatDate(language, check.checkedAt)}`, `Checked: ${formatDate(language, check.checkedAt)}`)}</p> : null}
    {failure ? <FieldError className="mt-3">{copy(language, "检查失败，请核对节点状态与系统配置。", "Inspection failed; review the node and system configuration.")} ({failure})</FieldError> : null}
    {error ? <FieldError className="mt-3" role="alert">{error}</FieldError> : null}
    {task ? <p className="mt-3 text-xs" role="status">{copy(language, "重装状态", "Reinstall status")}: {taskStatus}{task.error ? ` · ${task.error}` : ""}</p> : null}
    {task?.canRecheck ? <Button className="mt-2" type="button" size="sm" variant="outline" disabled={busy || !connected} onClick={() => void recheck()}>{copy(language, "重新检查", "Inspect again")}</Button> : null}
    {result && canReinstall && supported && (!task || task.state === "succeeded" || task.state === "cancelled") ? <div className="mt-4 border-t pt-4">
      <FieldGroup>
        <Alert variant="destructive">
          <ShieldAlertIcon aria-hidden="true" />
          <AlertTitle>{copy(language, "确认清空系统盘", "Confirm erasing system disk")}</AlertTitle>
          <AlertDescription>{copy(language, `重装 Debian 13 会清空系统盘 ${result.disk.path}。请确认商家控制台可用。`, `Installing Debian 13 erases ${result.disk.path}. Ensure provider console access is available.`)}</AlertDescription>
        </Alert>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field>
            <FieldLabel htmlFor={`${id}-ssh-user`}>{copy(language, "SSH 用户名", "SSH user")}</FieldLabel>
            <Input id={`${id}-ssh-user`} value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" />
          </Field>
          <Field>
            <FieldLabel htmlFor={`${id}-ssh-port`}>SSH {copy(language, "端口", "port")}</FieldLabel>
            <Input id={`${id}-ssh-port`} type="number" min={1} max={65535} value={sshPort} onChange={(e) => setSSHPort(e.target.value)} />
          </Field>
        </div>
        <Field>
          <FieldLabel htmlFor={`${id}-confirm-disk`}>{copy(language, "输入系统盘路径确认", "Type the system disk path")}</FieldLabel>
          <Input id={`${id}-confirm-disk`} value={confirmedDisk} onChange={(e) => setConfirmedDisk(e.target.value)} placeholder={result.disk.path} autoComplete="off" />
        </Field>
        <Field orientation="horizontal">
          <Checkbox id={`${id}-confirm-erase`} checked={confirmedErase} onCheckedChange={(value) => setConfirmedErase(value === true)} />
          <FieldLabel htmlFor={`${id}-confirm-erase`}>{copy(language, "确认清空系统盘并重装", "I confirm erasing the system disk")}</FieldLabel>
        </Field>
        <div className="flex flex-wrap gap-2">
          <Button type="button" variant="destructive" size="sm" disabled={!connected || busy || !username.trim() || confirmedDisk !== result.disk.path || !confirmedErase || Number(sshPort) < 1 || Number(sshPort) > 65535} onClick={() => void reinstall()}>{copy(language, "重装 Debian 13", "Reinstall Debian 13")}</Button>
        </div>
      </FieldGroup>
    </div> : null}
    {result && canReinstall && !supported ? <p className="mt-3 text-xs text-muted-foreground">{result.missingPrerequisites?.length ? copy(language, `缺少系统工具：${result.missingPrerequisites.join(", ")}`, `Missing system tools: ${result.missingPrerequisites.join(", ")}`) : copy(language, "当前配置尚不能完整恢复；请核对网络来源、DNS 依赖及主机环境。", "This configuration cannot yet be restored completely. Review network sources, DNS dependencies and the host environment.")}</p> : null}
    {result && !canReinstall ? <p className="mt-3 text-xs text-muted-foreground">{copy(language, "此节点暂不支持网页重装。", "This node does not yet support web reinstall.")}</p> : null}
  </section>;
}
