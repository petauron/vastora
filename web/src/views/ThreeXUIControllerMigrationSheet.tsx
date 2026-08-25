import { useEffect, useState, type FormEvent } from "react";
import { ArrowRightLeftIcon, CheckCircle2Icon, RotateCcwIcon, ShieldAlertIcon } from "lucide-react";
import { api } from "../api";
import type { AppData, Mutate } from "../App";
import type { Application, ThreeXUIControllerMigration } from "../types";
import type { Language } from "../translations";
import { copy, userError } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { SelectControl } from "@/components/SelectControl";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";

export function ThreeXUIControllerMigrationSheet({ application, data, language, mutate, onClose }: { application: Application | null; data: AppData; language: Language; mutate: Mutate; onClose: () => void }) {
  const candidates = application ? data.applications.filter((value) => value.siteId === application.siteId && value.appKey === application.appKey && value.role === "worker" && value.status === "running" && value.nodeSyncStatus === "ready" && data.agents.some((agent) => agent.id === value.nodeId && agent.connected)) : [];
  const [targetID, setTargetID] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [allowStale, setAllowStale] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [migration, setMigration] = useState<ThreeXUIControllerMigration | null>(null);
  const sourceAgent = application ? data.agents.find((value) => value.id === application.nodeId) : undefined;
  const sourceOnline = Boolean(sourceAgent?.connected);
  const selectedTarget = candidates.find((value) => value.id === targetID);
  const selectedAgent = selectedTarget ? data.agents.find((value) => value.id === selectedTarget.nodeId) : undefined;
  const active = migration && migration.state !== "ready" && migration.state !== "failed";

  useEffect(() => {
    if (!application) return;
    const existing = data.threeXUIControllerMigrations.find((value) => value.sourceApplicationId === application.id && value.state !== "ready") ?? null;
    setTargetID(candidates[0]?.id ?? "");
    setConfirmed(false);
    setAllowStale(false);
    setError("");
    setMigration(existing);
  }, [application?.id]);

  useEffect(() => {
    if (!active) return;
    let cancelled = false;
    let timer: number | undefined;
    const poll = async () => {
      try {
        const next = await api.threeXUIControllerMigration(migration.id);
        if (cancelled) return;
        setMigration(next);
        if (next.state === "ready") await mutate(async () => undefined, copy(language, "订阅主机迁移完成，原来的域名和订阅地址保持不变。", "Subscription host moved. Existing domains and subscription URLs were preserved."));
      } catch (pollError) {
        if (!cancelled) setError(userError(language, pollError));
      } finally {
        if (!cancelled) timer = window.setTimeout(() => void poll(), 1500);
      }
    };
    timer = window.setTimeout(() => void poll(), 1500);
    return () => { cancelled = true; if (timer !== undefined) window.clearTimeout(timer); };
  }, [active, migration?.id, language, mutate]);

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!application || !targetID) return;
    setBusy(true); setError("");
    try {
      let created: ThreeXUIControllerMigration | undefined;
      await mutate(async () => { created = await api.migrateThreeXUIController(application.id, targetID, allowStale); });
      if (created) setMigration(created);
    } catch (submitError) {
      setError(userError(language, submitError));
    } finally {
      setBusy(false);
    }
  };

  const retryCleanup = async () => {
    if (!migration) return;
    setBusy(true); setError("");
    try {
      setMigration(await api.retryThreeXUIControllerMigrationCleanup(migration.id));
    } catch (retryError) {
      setError(userError(language, retryError));
    } finally {
      setBusy(false);
    }
  };

  const steps = [
    { id: "backup", zh: "保存最新配置", en: "Save current configuration" },
    { id: "restore", zh: "恢复到新主机", en: "Restore on the new host" },
    { id: "complete", zh: "清理旧主机并同步节点", en: "Clean up the old host and sync nodes" }
  ];
  const progress = migration?.step === "backup" ? 0 : migration?.step === "restore" ? 1 : migration?.step === "cleanup" || migration?.step === "switch" ? 2 : migration?.step === "complete" ? 3 : 0;
  const cleanupBlocked = migration?.state === "switching" && migration.step === "cleanup" && Boolean(migration.lastError);
  const syncBlocked = migration?.state === "switching" && migration.step === "switch" && Boolean(migration.lastError);

  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(application)}><SheetContent className="sm:max-w-xl"><SheetHeader><SheetTitle>{copy(language, "迁移订阅主机", "Move subscription host")}</SheetTitle><SheetDescription>{copy(language, "把当前位置唯一的面板、客户端和订阅迁移到另一台已接入的 VLESS 节点。", "Move this location's panel, clients, and subscription to another connected VLESS node.")}</SheetDescription></SheetHeader>{migration ? <div aria-live="polite" className="flex flex-1 flex-col gap-4 overflow-y-auto px-4"><Alert variant={migration.state === "failed" || cleanupBlocked || syncBlocked ? "destructive" : "default"}>{migration.state === "ready" ? <CheckCircle2Icon /> : migration.state === "failed" || cleanupBlocked || syncBlocked ? <ShieldAlertIcon /> : <Spinner />}<AlertTitle>{migration.state === "ready" ? copy(language, "迁移完成", "Migration complete") : migration.state === "failed" ? copy(language, "迁移没有完成", "Migration did not complete") : cleanupBlocked ? copy(language, "新主机已接管，旧主机清理待重试", "New host is active; old-host cleanup needs a retry") : syncBlocked ? copy(language, "主机已切换，部分 VLESS 节点待重试", "Host switched; some VLESS nodes need a retry") : copy(language, "正在安全迁移", "Moving safely")}</AlertTitle><AlertDescription>{migration.state === "ready" ? copy(language, "新主机已接管面板和订阅；原主机已作为普通 VLESS 节点重新接入。", "The new host serves the panel and subscription. The previous host has reconnected as a regular VLESS node.") : migration.state === "failed" || cleanupBlocked || syncBlocked ? migration.lastError : copy(language, "请保持目标节点在线。页面可关闭，后台任务仍会继续。", "Keep the target node online. You may close this sheet; the task continues in the background.")}</AlertDescription></Alert><ol className="flex flex-col gap-2">{steps.map((step, index) => { const complete = progress > index; const current = migration.state !== "failed" && progress === index; return <li className="flex items-center gap-3 rounded-xl border p-3" key={step.id}>{complete ? <CheckCircle2Icon className="size-5 text-emerald-500" /> : current ? <Spinner className="size-5" /> : <span className="flex size-5 items-center justify-center rounded-full border text-xs text-muted-foreground">{index + 1}</span>}<span className={complete || current ? "font-medium" : "text-muted-foreground"}>{copy(language, step.zh, step.en)}</span></li>; })}</ol>{migration.backup?.updatedAt ? <p className="text-xs text-muted-foreground">{copy(language, `恢复点：${new Date(migration.backup.updatedAt).toLocaleString()}`, `Restore point: ${new Date(migration.backup.updatedAt).toLocaleString()}`)}</p> : null}{error ? <FieldError role="alert">{error}</FieldError> : null}</div> : <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><div className="flex-1 overflow-y-auto px-4"><FieldGroup><Alert><ArrowRightLeftIcon /><AlertTitle>{copy(language, "现有入口配置会保留", "Existing access settings are preserved")}</AlertTitle><AlertDescription>{copy(language, "Vastora 会迁移管理域名、订阅域名、客户端和已有入站。迁移期间会有短暂中断；如果原主机同时承担入口节点，请先切换入口。", "Vastora moves the panel domain, subscription domain, clients, and existing inbounds. A brief interruption is expected. If the current host is also the access gateway, move that gateway first.")}</AlertDescription></Alert><Field><FieldLabel htmlFor="three-x-ui-migration-target">{copy(language, "新的订阅主机", "New subscription host")}</FieldLabel><SelectControl id="three-x-ui-migration-target" onValueChange={setTargetID} options={[{ value: "", label: copy(language, "没有可迁移的节点", "No eligible node"), disabled: true }, ...candidates.map((candidate) => ({ value: candidate.id, label: data.agents.find((agent) => agent.id === candidate.nodeId)?.name ?? candidate.nodeId }))]} required value={targetID} /><FieldDescription>{selectedAgent ? copy(language, `${selectedAgent.name} 已在线并接入当前订阅主机。`, `${selectedAgent.name} is online and connected to the current subscription host.`) : copy(language, "目标必须是同一位置内已连接的 VLESS 节点。", "The target must be a connected VLESS node in the same location.")}</FieldDescription></Field>{!sourceOnline ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "原主机当前离线", "Current host is offline")}</AlertTitle><AlertDescription>{copy(language, "只能使用 Center 中最后一次恢复点；恢复点之后的修改可能丢失。", "Only the latest Center restore point can be used; changes after that restore point may be lost.")}</AlertDescription></Alert> : null}{!sourceOnline ? <Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="three-x-ui-migration-stale">{copy(language, "接受使用最后一次恢复点", "Use the latest restore point")}</FieldLabel><FieldDescription>{copy(language, "仅在原主机无法恢复时启用。", "Use only when the current host cannot be recovered.")}</FieldDescription></div><Switch checked={allowStale} id="three-x-ui-migration-stale" onCheckedChange={setAllowStale} /></Field> : null}<Field orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="three-x-ui-migration-confirm">{copy(language, "确认迁移", "Confirm migration")}</FieldLabel><FieldDescription>{copy(language, "开始后不要在 3x-ui 中修改配置，直到迁移完成。", "Do not change 3x-ui configuration until the migration completes.")}</FieldDescription></div><Switch checked={confirmed} id="three-x-ui-migration-confirm" onCheckedChange={setConfirmed} /></Field>{candidates.length === 0 ? <FieldError>{copy(language, "还没有在线且已接入的 VLESS 节点可作为新主机。", "No online, connected VLESS node is available as the new host.")}</FieldError> : null}{error ? <FieldError role="alert">{error}</FieldError> : null}</FieldGroup></div><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !targetID || !confirmed || !sourceOnline && !allowStale} type="submit">{busy ? <Spinner data-icon="inline-start" /> : <ArrowRightLeftIcon data-icon="inline-start" />}{copy(language, "开始迁移", "Start migration")}</Button></SheetFooter></form>}<SheetFooter>{cleanupBlocked ? <Button disabled={busy} onClick={() => void retryCleanup()} variant="outline">{busy ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}{copy(language, "重试旧主机清理", "Retry old-host cleanup")}</Button> : null}{migration?.state === "failed" ? <Button onClick={() => { setMigration(null); setConfirmed(false); setError(""); }} variant="outline"><RotateCcwIcon data-icon="inline-start" />{copy(language, "重新开始", "Start again")}</Button> : null}{migration ? <Button onClick={onClose}>{copy(language, migration.state === "ready" ? "完成" : "关闭", migration.state === "ready" ? "Done" : "Close")}</Button> : null}</SheetFooter></SheetContent></Sheet>;
}
