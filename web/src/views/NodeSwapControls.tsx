import { useEffect, useId, useState } from "react";
import { api } from "../api";
import type { Language } from "../translations";
import type { NodeSwap, NodeSwapTask, SwapTargetPolicy } from "../node-swap-types";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field";
import { copy, userError } from "./shared";

const formatMiB = (bytes: number) => `${Math.round(bytes / (1024 * 1024))} MiB`;

function describeTarget(language: Language, target: SwapTargetPolicy): string {
  if (!target.tierMiB || !target.swapfileBytes) {
    return copy(language, "无适用固定档位（保持只读）", "No applicable fixed tier (read-only)");
  }
  if (target.zramBytes && target.zramAlgorithm) {
    return copy(
      language,
      `${target.tierMiB} MiB 档位：${formatMiB(target.zramBytes)} ${target.zramAlgorithm.toUpperCase()} zram0（优先级 ${target.zramPriority}）+ ${formatMiB(target.swapfileBytes)} /swapfile（优先级 ${target.swapfilePriority}）+ swappiness=${target.swappiness}`,
      `${target.tierMiB} MiB tier: ${formatMiB(target.zramBytes)} ${target.zramAlgorithm.toUpperCase()} zram0 (pri ${target.zramPriority}) + ${formatMiB(target.swapfileBytes)} /swapfile (pri ${target.swapfilePriority}) + swappiness=${target.swappiness}`,
    );
  }
  return copy(
    language,
    `${target.tierMiB} MiB 档位：${formatMiB(target.swapfileBytes)} /swapfile（优先级 ${target.swapfilePriority}）+ swappiness=${target.swappiness}（无 zram）`,
    `${target.tierMiB} MiB tier: ${formatMiB(target.swapfileBytes)} /swapfile (pri ${target.swapfilePriority}) + swappiness=${target.swappiness} (no zram)`,
  );
}

export function NodeSwapControls({ nodeId, connected, language }: { nodeId: string; connected: boolean; language: Language }) {
  const id = useId();
  const [view, setView] = useState<NodeSwap | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const result = view?.result;
  const waiting = view?.state === "pending" || view?.state === "running";
  const active = busy || waiting;

  useEffect(() => {
    const controller = new AbortController();
    void api.nodeSwap(nodeId, controller.signal).then(setView).catch((cause) => {
      if (!controller.signal.aborted) setError(userError(language, cause));
    });
    return () => controller.abort();
  }, [nodeId, language]);

  useEffect(() => {
    if (!waiting) return;
    const controller = new AbortController();
    const timer = setInterval(() => {
      void api.nodeSwap(nodeId, controller.signal).then(setView).catch((cause) => {
        if (!controller.signal.aborted) setError(userError(language, cause));
      });
    }, 1500);
    return () => {
      controller.abort();
      clearInterval(timer);
    };
  }, [nodeId, language, waiting]);

  const queue = async (action: NodeSwapTask["action"]) => {
    if (active || !connected) return;
    setBusy(true);
    setError("");
    try {
      const payload: NodeSwapTask = action === "inspect" ? { action } : { action, revision: result?.revision };
      setView(await api.configureNodeSwap(nodeId, payload));
    } catch (cause) {
      setError(userError(language, cause));
      try {
        setView(await api.nodeSwap(nodeId));
      } catch {
        // Keep the original error if refresh also fails.
      }
    } finally {
      setBusy(false);
      setConfirmed(false);
    }
  };

  const state = result?.state ?? "unconfigured";
  const labels: Record<string, string> = {
    unconfigured: copy(language, "尚未检测", "Not inspected"),
    eligible: copy(language, "符合条件，可启用受管优化", "Eligible for managed optimization"),
    applied: copy(language, "已启用受管 zram / swap", "Managed zram / swap applied"),
    read_only: copy(language, "只读（不适用或已有非受管配置）", "Read-only (ineligible or unmanaged swap present)"),
    action_required: copy(language, "状态漂移，请核对节点", "State drifted; inspect host"),
  };

  if (!view) {
    return (
      <FieldSet>
        <FieldLegend>{copy(language, "主机 Zram 与 Swap 优化", "Host zram & swap optimization")}</FieldLegend>
        {error ? <FieldError role="alert">{error}</FieldError> : <FieldDescription role="status">{copy(language, "正在读取…", "Loading…")}</FieldDescription>}
        <Button type="button" variant="outline" onClick={() => { void api.nodeSwap(nodeId).then(setView).catch((cause) => setError(userError(language, cause))); }}>
          {copy(language, "重试读取状态", "Retry reading status")}
        </Button>
      </FieldSet>
    );
  }

  return (
    <FieldSet>
      <FieldLegend>{copy(language, "主机 Zram 与 Swap 优化", "Host zram & swap optimization")}</FieldLegend>
      <FieldDescription>
        {copy(
          language,
          "按 512 MiB / 1 GiB / 2 GiB 固定档位收敛 systemd KVM 主机的 zram 与 /swapfile；容器、大内存或已有非受管 Swap 的节点严格保持只读，不覆盖现有配置。",
          "Converges systemd KVM hosts onto fixed 512 MiB / 1 GiB / 2 GiB zram and /swapfile tiers. Containers, larger hosts and pre-existing unmanaged swap remain strictly read-only.",
        )}
      </FieldDescription>
      <FieldDescription role="status">
        {active ? copy(language, "正在等待节点执行…", "Waiting for the node…") : (labels[state] ?? state)}
      </FieldDescription>

      {result ? (
        <div className="rounded-lg border p-3 text-xs leading-5">
          <p>
            {copy(
              language,
              `物理内存 ${formatMiB(result.memoryTotalBytes)}（可用 ${formatMiB(result.memoryAvailableBytes)}）· 根分区剩余 ${formatMiB(result.diskFreeBytes)} · 环境 ${result.virtualization ?? "unknown"} / ${result.systemd ? "systemd" : "non-systemd"} · swappiness=${result.swappiness}`,
              `Memory ${formatMiB(result.memoryTotalBytes)} (${formatMiB(result.memoryAvailableBytes)} available) · Disk free ${formatMiB(result.diskFreeBytes)} · Env ${result.virtualization ?? "unknown"} / ${result.systemd ? "systemd" : "non-systemd"} · swappiness=${result.swappiness}`,
            )}
          </p>
          <p className="mt-1 text-muted-foreground">{describeTarget(language, result.target)}</p>
          {result.zram ? (
            <p className="mt-1">
              {copy(
                language,
                `当前 zram0：${formatMiB(result.zram.disksizeBytes)}（算法 ${result.zram.algorithm || "unknown"}，逻辑已用 ${formatMiB(result.zram.origDataBytes)}）`,
                `Active zram0: ${formatMiB(result.zram.disksizeBytes)} (${result.zram.algorithm || "unknown"}, logical ${formatMiB(result.zram.origDataBytes)})`,
              )}
            </p>
          ) : null}
          <p className="mt-1">
            {result.swaps.length > 0
              ? copy(language, "当前 Swap 设备：", "Active swap devices: ") + result.swaps.map((dev) => `${dev.name} (${formatMiB(dev.sizeBytes)}, used ${formatMiB(dev.usedBytes)}, pri ${dev.priority})`).join("; ")
              : copy(language, "当前无活动 Swap 设备", "No active swap devices")}
          </p>
        </div>
      ) : null}

      {result?.reason ? <FieldDescription>{result.reason}</FieldDescription> : null}

      {result?.canApply && result.revision ? (
        <FieldGroup>
          <Field orientation="horizontal">
            <Checkbox id={`${id}-apply`} checked={confirmed} onCheckedChange={(value) => setConfirmed(value === true)} />
            <FieldLabel htmlFor={`${id}-apply`}>
              {copy(language, "确认在此 KVM 节点启用固定档位受管 zram / swap 配置", "Confirm applying fixed-tier managed zram / swap on this KVM host")}
            </FieldLabel>
          </Field>
          <Button type="button" disabled={!connected || active || !confirmed} onClick={() => void queue("apply")}>
            {copy(language, "应用固定档位优化", "Apply fixed-tier optimization")}
          </Button>
        </FieldGroup>
      ) : null}

      {result?.canRestore && result.revision ? (
        <FieldGroup>
          <Field orientation="horizontal">
            <Checkbox id={`${id}-restore`} checked={confirmed} onCheckedChange={(value) => setConfirmed(value === true)} />
            <FieldLabel htmlFor={`${id}-restore`}>
              {copy(language, "确认核验内存余量并恢复应用前状态", "Confirm checking memory headroom and restoring pre-apply state")}
            </FieldLabel>
          </Field>
          <Button type="button" variant="outline" disabled={!connected || active || !confirmed} onClick={() => void queue("restore")}>
            {copy(language, "恢复应用前状态", "Restore pre-apply state")}
          </Button>
        </FieldGroup>
      ) : null}

      <Button type="button" variant="ghost" disabled={!connected || active} onClick={() => void queue("inspect")}>
        {copy(language, "读取节点实际状态", "Inspect actual host state")}
      </Button>
      {view.error ? <FieldError>{view.error}</FieldError> : null}
      {error ? <FieldError role="alert">{error}</FieldError> : null}
    </FieldSet>
  );
}
