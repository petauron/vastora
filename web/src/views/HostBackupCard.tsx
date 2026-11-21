import { useEffect, useState, type FormEvent } from "react";
import { api } from "../api";
import type { HostBackupSettings, HostBackupStatus } from "../host-backup-types";
import type { Language } from "../translations";
import { copy, formatDate, userError } from "./shared";
import { Button } from "@/components/ui/button";
import { Card, CardHeader, CardTitle, CardDescription, CardContent } from "@/components/ui/card";
import { Field, FieldGroup, FieldLabel, FieldDescription, FieldError } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

export function HostBackupCard({ language }: { language: Language }) {
  const [status, setStatus] = useState<HostBackupStatus | null>(null);
  const [settings, setSettings] = useState<HostBackupSettings | null>(null);
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [confirmRun, setConfirmRun] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    void api.hostBackup(controller.signal).then((value) => { setStatus(value); setSettings(value.settings); })
      .catch((reason) => { if (!controller.signal.aborted) setError(userError(language, reason)); });
    return () => controller.abort();
  }, [language]);
  async function refresh() {
    setBusy(true); setError("");
    try { setStatus(await api.hostBackup()); }
    catch (reason) { setError(userError(language, reason)); }
    finally { setBusy(false); }
  }
  async function save(event: FormEvent) {
    event.preventDefault();
    if (!settings || !status) return;
    if (!status.passwordSet && password !== confirmation) { setError(copy(language, "两次密码不一致。", "Passwords do not match.")); return; }
    setBusy(true); setError(""); setNotice("");
    try {
      const value = await api.configureHostBackup({ ...settings, ...(status.passwordSet ? {} : { password }) });
      setStatus(value); setSettings(value.settings); setPassword(""); setConfirmation("");
      setNotice(copy(language, "备份设置已保存。", "Backup settings saved."));
    } catch (reason) { setError(userError(language, reason)); }
    finally { setBusy(false); }
  }
  async function run() {
    setBusy(true); setError("");
    try { setStatus(await api.startHostBackup()); setConfirmRun(false); setNotice(copy(language, "已排队。执行时服务会短暂停止，页面可能暂时无法访问。恢复后刷新查看结果。", "Queued. Services and this page may briefly become unavailable. Refresh after recovery to see the result.")); }
    catch (reason) { setError(userError(language, reason)); }
    finally { setBusy(false); }
  }
  const dirty = settings && status && JSON.stringify(settings) !== JSON.stringify(status.settings);
  const stateLabels: Record<string, string> = { queued: copy(language, "等待执行", "Queued"), idle: copy(language, "尚未运行", "Not run"), running: copy(language, "执行中", "Running"), failed: copy(language, "失败", "Failed"), success: copy(language, "本地备份完成", "Local backup complete") };
  return <Card>
    <CardHeader><CardTitle>{copy(language, "Vastora 备份", "Vastora backups")}</CardTitle><CardDescription>{copy(language, "仅包含已核对的 Vastora 管理服务、数据及恢复配置。当前仅保存在本机，尚未配置异地上传。", "Covers inventoried Vastora-managed services, data and recovery configuration. Local storage only; offsite upload is not configured.")}</CardDescription></CardHeader>
    <CardContent className="flex flex-col gap-4">
      {status && !status.available ? <p className="text-sm text-muted-foreground">{copy(language, "宿主机备份服务尚未安装。需要先核对服务清单、安装 restic 和备份执行器。", "The host backup runner is not installed. Inventory services and install restic and the runner first.")}</p> : null}
      {status?.available && settings ? <form className="flex flex-col gap-4" onSubmit={(event) => void save(event)}>
        <FieldGroup>
          <Field orientation="horizontal"><FieldLabel htmlFor="host-backup-enabled">{copy(language, "定时备份", "Scheduled backups")}</FieldLabel><Switch id="host-backup-enabled" checked={settings.enabled} disabled={busy} onCheckedChange={(enabled) => setSettings({ ...settings, enabled })} /></Field>
          <Field><FieldLabel htmlFor="host-backup-directory">{copy(language, "备份目录", "Backup directory")}</FieldLabel><Input id="host-backup-directory" required value={settings.directory} disabled={busy} onChange={(event) => setSettings({ ...settings, directory: event.target.value })} /><FieldDescription>{copy(language, "宿主机绝对路径，不能位于被备份的数据目录内。更换目录不会删除旧备份。", "Absolute host path outside source directories. Changing it does not delete old backups.")}</FieldDescription></Field>
          <Field><FieldLabel htmlFor="host-backup-time">{copy(language, "执行时间（UTC）", "Run time (UTC)")}</FieldLabel><Input id="host-backup-time" type="time" required value={settings.timeUTC} disabled={busy} onChange={(event) => setSettings({ ...settings, timeUTC: event.target.value })} /></Field>
          <Field><FieldLabel htmlFor="host-backup-interval">{copy(language, "间隔天数", "Interval in days")}</FieldLabel><Input id="host-backup-interval" type="number" min={1} max={30} required disabled={busy} value={settings.intervalDays} onChange={(event) => setSettings({ ...settings, intervalDays: Number(event.target.value) })} /></Field>
          <Field><FieldLabel htmlFor="host-backup-retention">{copy(language, "保留份数", "Snapshots to keep")}</FieldLabel><Input id="host-backup-retention" type="number" min={1} max={90} required disabled={busy} value={settings.keepLast} onChange={(event) => setSettings({ ...settings, keepLast: Number(event.target.value) })} /><FieldDescription>{copy(language, "新备份完成且服务恢复后，才清理本机制生成的旧快照。", "Old snapshots are pruned only after a successful backup and service recovery.")}</FieldDescription></Field>
          {!status.passwordSet ? <>
            <Field><FieldLabel htmlFor="host-backup-password">{copy(language, "备份密码", "Backup password")}</FieldLabel><Input id="host-backup-password" type="password" autoComplete="new-password" required minLength={12} disabled={busy} value={password} onChange={(event) => setPassword(event.target.value)} /><FieldDescription>{copy(language, "请另存到密码管理器。服务器丢失后，恢复需要这个密码和异地保存的备份目录。", "Save this in a password manager. Recovery after host loss requires this password and an off-host repository copy.")}</FieldDescription></Field>
            <Field><FieldLabel htmlFor="host-backup-confirm">{copy(language, "确认密码", "Confirm password")}</FieldLabel><Input id="host-backup-confirm" type="password" autoComplete="new-password" required minLength={12} disabled={busy} value={confirmation} onChange={(event) => setConfirmation(event.target.value)} /></Field>
          </> : null}
        </FieldGroup>
        <div className="flex flex-wrap gap-2"><Button type="submit" disabled={busy}>{copy(language, "保存设置", "Save settings")}</Button><Button type="button" variant="outline" disabled={busy || !status.passwordSet || Boolean(dirty) || ["running", "queued"].includes(status.execution.state)} onClick={() => setConfirmRun(true)}>{copy(language, "立即备份", "Back up now")}</Button></div>
        {confirmRun ? <div className="flex flex-col gap-2"><p className="text-sm">{copy(language, "备份期间将暂停清单内的服务，包括 Center。现在执行？", "Inventoried services, including Center, will be stopped during the backup. Start now?")}</p><div className="flex gap-2"><Button type="button" disabled={busy} onClick={() => void run()}>{copy(language, "开始备份", "Start backup")}</Button><Button type="button" variant="outline" disabled={busy} onClick={() => setConfirmRun(false)}>{copy(language, "取消", "Cancel")}</Button></div></div> : null}
      </form> : null}
      <div role="status" aria-live="polite" aria-atomic="true" className="text-sm">{notice || (status ? `${copy(language, "最近状态", "Latest status")}: ${stateLabels[status.execution.state] ?? status.execution.state}` : copy(language, "读取备份状态…", "Loading backup status…"))}</div>
      {status?.coverage.containers.length ? <details><summary className="cursor-pointer text-sm">{copy(language, "Vastora 服务清单", "Backup coverage")}</summary><div className="mt-2 flex flex-col gap-2 text-sm"><p>{[...status.coverage.containers, ...status.coverage.units].join(" · ")}</p><ul className="list-inside list-disc">{status.coverage.sources.map((source) => <li key={source} className="break-all">{source}</li>)}</ul></div></details> : null}
      {status?.execution.message ? <p className="text-sm text-muted-foreground">{status.execution.message}</p> : null}
      {error ? <FieldError role="alert">{error}</FieldError> : null}
      {status?.snapshots.length ? <Table><TableHeader><TableRow><TableHead>{copy(language, "时间", "Time")}</TableHead><TableHead>{copy(language, "快照", "Snapshot")}</TableHead><TableHead>{copy(language, "数据大小", "Data size")}</TableHead></TableRow></TableHeader><TableBody>{[...status.snapshots].reverse().map((snapshot) => <TableRow key={snapshot.id}><TableCell>{formatDate(language, snapshot.time)}</TableCell><TableCell>{snapshot.id.slice(0, 8)}</TableCell><TableCell>{snapshot.bytes ? `${(snapshot.bytes / 1024 ** 3).toFixed(2)} GiB` : "—"}</TableCell></TableRow>)}</TableBody></Table> : null}
      <Button type="button" variant="outline" disabled={busy} onClick={() => void refresh()}>{copy(language, "刷新状态", "Refresh status")}</Button>
    </CardContent>
  </Card>;
}
