import { useEffect, useState } from "react";
import { RefreshCwIcon } from "lucide-react";
import { api } from "../api";
import type { Application } from "../types";
import type { SSHProtectionInventory } from "../node-ssh-protection-types";
import type { Language } from "../translations";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { FieldDescription } from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import { copy, formatDate } from "./shared";

type Result = { controller: string; name: string; runtimes: SSHProtectionInventory["runtimes"]; failed: boolean };

function formatReportTime(language: Language, value?: string) {
  if (!value) return "—";
  return Number.isFinite(Date.parse(value)) ? formatDate(language, value) : value;
}

export function NodeSSHProtection({ nodeId, connected, applications, language }: { nodeId: string; connected: boolean; applications: Application[]; language: Language }) {
  const [refresh, setRefresh] = useState(0);
  const [results, setResults] = useState<Result[] | null>(null);
  // Keep heartbeat-driven parent renders from restarting the request.
  const controllers = JSON.stringify(applications.filter(app => app.appKey === "vastora-official/meridian" && app.status === "running").map(app => ({ id: app.id, name: app.name })).sort((a, b) => a.id.localeCompare(b.id)));
  useEffect(() => {
    const abort = new AbortController();
    setResults(null);
    const sources = JSON.parse(controllers) as { id: string; name: string }[];
    void Promise.all(sources.map(async app => {
      try {
        const view = await api.nodeSSHProtection(app.id, abort.signal);
        if (!view || !Array.isArray(view.endpoints) || !Array.isArray(view.runtimes)) throw new Error("Missing inventory");
        const entries = new Set(view.endpoints.filter(endpoint => endpoint.nodeId === nodeId && !endpoint.retired).map(endpoint => endpoint.entryId));
        const runtimes = view.runtimes.filter(runtime => (runtime.role === "landing" && runtime.id === nodeId) || (runtime.role === "entry" && entries.has(runtime.id)));
        return { controller: app.id, name: app.name, runtimes, failed: false };
      } catch {
        return { controller: app.id, name: app.name, runtimes: [], failed: true };
      }
    })).then(value => { if (!abort.signal.aborted) setResults(value); });
    return () => abort.abort();
  }, [controllers, nodeId, refresh]);
  return <section className="rounded-xl border p-4">
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium">{copy(language, "SSH 保护策略", "SSH protection policy")}</p>
        <FieldDescription className="mt-1">{copy(language, "Glivela 代理出口的宿主 SSH 地址保护，只读展示；SSH 登录防护由节点安全基线单独管理。", "Read-only host SSH address protection for Glivela proxy egress. SSH login protection is managed separately by the node security baseline.")}</FieldDescription>
      </div>
      <Button type="button" size="sm" variant="outline" disabled={results === null} onClick={() => setRefresh(value => value + 1)}>
        {results === null ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" />}
        {copy(language, "刷新保护状态", "Refresh protection status")}
      </Button>
    </div>
    <div role="status" aria-live="polite" className="mt-3 flex flex-col gap-3 text-sm">
      {results === null ? <p className="text-xs text-muted-foreground">{copy(language, "正在读取保护状态…", "Loading protection status…")}</p> : results.length === 0 ? <p className="text-xs text-muted-foreground">{copy(language, "没有可读取的 Glivela 控制端，保护状态未知。", "No available Glivela controller; protection status is unknown.")}</p> : results.map(result => <div key={result.controller} className="space-y-2 rounded-lg border bg-muted/20 p-3">
        <p className="text-xs font-medium">{result.name}</p>
        {result.failed ? <p className="text-xs text-muted-foreground">{copy(language, "读取失败，保护状态未知，请刷新重试。", "Read failed; protection status is unknown. Refresh to retry.")}</p> : result.runtimes.length === 0 ? <p className="text-xs text-muted-foreground">{copy(language, "未找到此主机对应的运行节点，无法确认策略。", "No mapped runtime was found for this host; policy is unknown.")}</p> : result.runtimes.map(runtime => <div key={runtime.id} className="space-y-1 text-xs">
          <div className="flex flex-wrap items-center gap-2"><Badge variant="secondary">{runtime.role === "entry" ? copy(language, "入口", "Entry") : copy(language, "落地", "Landing")}</Badge><code className="break-all rounded bg-muted px-1.5 py-0.5 text-[11px]">{runtime.id}</code></div>
          <p className="leading-5">{typeof runtime.publicSSHConfigured !== "boolean" ? copy(language, "控制端未提供保护状态，请升级后重试。", "The controller did not provide protection status. Upgrade and retry.") : !runtime.publicSSHConfigured ? copy(language, "未配置完整宿主公网地址；代理出口 TCP 22 按默认策略阻断。", "Complete host public addresses are not configured; proxy egress TCP 22 is blocked by default policy.") : connected && runtime.runtimeReady === true ? copy(language, "保护地址已配置，控制端报告当前修订已应用；SSH 可用性仍需单独验证。", "Protection addresses are configured and the controller reports the current revision applied; SSH connectivity requires separate verification.") : copy(language, "保护地址已配置，但节点离线、修订待应用或上报已过期，当前生效状态未知。", "Protection addresses are configured, but the node is offline, pending application or its report is stale; current protection is unknown.")}</p>
          <FieldDescription>{copy(language, "修订", "Revision")}: {runtime.revision || "—"} · {copy(language, "最后上报", "Last report")}: {formatReportTime(language, runtime.reportedAt)}</FieldDescription>
        </div>)}
      </div>)}
    </div>
    <FieldDescription className="mt-3">{copy(language, "以上为本次读取的快照，不代表实时连通性或 SSH 登录验证结果。", "This is a snapshot, not proof of live connectivity or successful SSH login.")}</FieldDescription>
  </section>;
}
