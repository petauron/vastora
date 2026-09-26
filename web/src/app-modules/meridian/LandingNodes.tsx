import { Fragment, useId, useState } from "react";
import { ChevronDownIcon, PlusIcon, SlidersHorizontalIcon } from "lucide-react";
import { api } from "@/api";
import type { LandingView } from "@/landing-types";
import type { Language } from "@/translations";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { TableCell, TableRow } from "@/components/ui/table";
import { regionName } from "@/lib/regions";
import { cn } from "@/lib/utils";
import { assessmentLabel } from "@/views/IPAssessment";
import { IPQualityButton, useIPQuality } from "@/views/IPQuality";
import { IPQualityComparison } from "@/views/IPQualityComparison";
import { useLanding } from "@/views/LandingControls";
import { RegionFlag } from "@/views/RegionFlag";
import { copy, userError } from "@/views/shared";
import { LandingEgressIP } from "./EgressIP";
import { NodeLocation } from "./NodeLocation";

type LandingState = NonNullable<ReturnType<typeof useLanding>>;
type Server = LandingView["servers"][number];
const statusLabels = { ready: ["可用", "Available"], pending: ["等待中", "Pending"], applying: ["正在配置", "Applying"], failed: ["配置失败", "Failed"], stopped: ["已停止", "Stopped"], offline: ["离线", "Offline"], draining: ["正在移除", "Removing"] } as const;

function selectedRegions(state: LandingState, nodeIds: string[]) {
  return Object.fromEntries(nodeIds.flatMap((id) => {
    const code = state.regions[id] || state.view?.landingRegionCodes?.[id];
    return code ? [[id, code]] : [];
  }));
}

export function LandingTableRows({ language, search, onSubscriptions }: { language: Language; search: string; onSubscriptions?: () => void }) {
  const state = useLanding();
  const quality = useIPQuality();
  const [adding, setAdding] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);
  const id = useId();
  const servers = state?.view?.servers.filter((server) => !search || [server.name, server.nodeId, state.regions[server.nodeId] ? regionName(state.regions[server.nodeId], [language]) : ""].some((value) => value.toLocaleLowerCase().includes(search))) ?? [];
  return <>
    <TableRow className="block bg-muted/30 hover:bg-muted/30 lg:table-row"><TableCell colSpan={5} className="block lg:table-cell">
      <div className="flex flex-wrap items-center justify-between gap-2"><h3 className="text-xs font-medium">{copy(language, "落地机", "Landing nodes")} <span className="ml-1 text-muted-foreground">{servers.length}</span></h3><Button variant="ghost" size="sm" aria-expanded={adding} aria-controls={`${id}-add`} onClick={() => setAdding(!adding)}><PlusIcon data-icon="inline-start" />{copy(language, "添加落地机", "Add landing node")}</Button></div>
    </TableCell></TableRow>
    {adding && state ? <TableRow className="block hover:bg-transparent lg:table-row"><TableCell colSpan={5} className="block whitespace-normal lg:table-cell"><div id={`${id}-add`} className="max-w-xl py-3"><AddLandingNode state={state} language={language} onAdded={() => setAdding(false)} /></div></TableCell></TableRow> : null}
    {state ? <LandingPoolNotice state={state} language={language} /> : null}
    {state?.failed || !state?.view ? <TableRow className="block lg:table-row"><TableCell colSpan={5} className="block text-xs text-muted-foreground lg:table-cell">{state?.failed ? copy(language, "落地机读取失败", "Unable to load landing nodes") : copy(language, "正在读取落地机…", "Loading landing nodes…")}</TableCell></TableRow> : servers.map((server) => {
      const open = expanded === server.nodeId;
      const panelId = `${id}-${server.nodeId}`;
      return <Fragment key={server.nodeId}>
        <TableRow data-landing-node-id={server.nodeId} className="grid grid-cols-2 gap-x-4 gap-y-3 py-3 lg:table-row lg:py-0">
          <TableCell className="col-span-2 min-w-0 p-0 whitespace-normal lg:px-2 lg:py-3">
            <div className="flex items-center gap-2"><RegionFlag code={state.regions[server.nodeId]} language={language} /><span className="min-w-0 break-words font-medium">{server.name}</span></div>
            <p className="mt-1 text-xs text-muted-foreground"><NodeLocation nodeId={server.nodeId} regionCode={state.regions[server.nodeId]} language={language} /> · {server.egressIp?.includes(":") ? "IPv6" : "IPv4"}</p>
          </TableCell>
          <TableCell className="col-span-2 min-w-0 p-0 whitespace-normal lg:px-2 lg:py-3"><IPQualityButton nodeId={server.nodeId} name={server.name} language={language} /></TableCell>
          <TableCell className="min-w-0 p-0 whitespace-normal lg:px-2 lg:py-3"><span className="inline-flex items-center gap-2"><span aria-hidden="true" className={cn("apps-status-dot", server.status === "ready" ? "bg-latency-fast" : ["failed", "offline"].includes(server.status) ? "bg-destructive" : "bg-muted-foreground")} />{copy(language, ...statusLabels[server.status])}</span></TableCell>
          <TableCell className="min-w-0 p-0 whitespace-normal lg:px-2 lg:py-3"><LandingSubscriptions server={server} language={language} /></TableCell>
          <TableCell className="col-span-2 p-0 lg:px-2 lg:py-3"><div className="flex justify-end"><Button variant="ghost" size="icon-sm" className="max-lg:min-h-11 max-lg:min-w-11" aria-label={copy(language, `${server.name} 落地设置`, `${server.name} landing settings`)} aria-expanded={open} aria-controls={panelId} onClick={() => setExpanded(open ? null : server.nodeId)}>{open ? <ChevronDownIcon aria-hidden="true" /> : <SlidersHorizontalIcon aria-hidden="true" />}</Button></div></TableCell>
        </TableRow>
        {open ? <TableRow className="block bg-muted/20 hover:bg-muted/20 lg:table-row"><TableCell colSpan={5} className="block whitespace-normal lg:table-cell"><div id={panelId} className="grid gap-6 py-4 md:grid-cols-2">
          <LandingEgressIP key={`${server.nodeId}:${server.egressRevision}`} server={server} language={language} disabled={state.busy || state.failed || !["ready", "failed"].includes(server.status)} save={(address) => state.change((signal) => api.setLandingEgress(server.nodeId, server.egressRevision ?? 0, address, signal))} />
          <div className="flex flex-col items-start gap-3 text-xs"><h4 className="font-medium">{copy(language, "订阅路由", "Subscription routes")}</h4><p className="text-muted-foreground">{copy(language, `${server.eligibleEntries} 个入口 · ${server.readyCombinations} 就绪 · ${server.failedCombinations} 失败 · ${server.withheldCombinations} 暂缓`, `${server.eligibleEntries} entries · ${server.readyCombinations} ready · ${server.failedCombinations} failed · ${server.withheldCombinations} withheld`)}</p>
            {onSubscriptions ? <Button size="sm" variant="outline" onClick={onSubscriptions}>{copy(language, "配置账号路由", "Configure account routes")}</Button> : null}
            <p className="text-muted-foreground">{copy(language, "添加落地机后，在账号路由中选择才会加入对应订阅。", "Select this landing node in account routes to include it in that subscription.")}</p>
            <Button variant="ghost" size="sm" disabled={state.busy || state.failed || server.status === "draining"} aria-label={copy(language, `移除 ${server.name}`, `Remove ${server.name}`)} onClick={() => {
              const view = state.view;
              if (!view) return;
              const nodeIds = view.nodeIds.filter((nodeId) => nodeId !== server.nodeId);
              void state.change((signal) => api.selectLanding(nodeIds, view.revision, selectedRegions(state, nodeIds), signal));
            }}>{copy(language, "移除此落地机", "Remove landing node")}</Button>
            {server.status === "draining" ? <p role="status" className="text-muted-foreground">{copy(language, "正在停止发布并清理会话、路由和授权。", "Stopping publication and draining sessions, routes and grants.")}</p> : null}
          </div>
        </div></TableCell></TableRow> : null}
      </Fragment>;
    })}
    {state?.view && !state.failed && !servers.length ? <TableRow className="block lg:table-row"><TableCell colSpan={5} className="block py-5 text-xs text-muted-foreground lg:table-cell">{search ? copy(language, "没有匹配的落地机", "No matching landing nodes") : copy(language, "尚未添加落地机", "No landing nodes configured")}</TableCell></TableRow> : null}
    <TableRow className="block hover:bg-transparent lg:table-row"><TableCell colSpan={5} className="block whitespace-normal lg:table-cell"><div className="py-2"><IPQualityComparison language={language} nodes={quality?.agents.map((agent) => ({ id: agent.id, name: agent.name })) ?? []} /></div></TableCell></TableRow>
  </>;
}

function LandingSubscriptions({ server, language }: { server: Server; language: Language }) {
  const empty = !server.readyCombinations && !server.failedCombinations && !server.withheldCombinations;
  return <div className="flex flex-col gap-1 text-xs"><span className={server.readyCombinations ? "text-latency-fast" : "text-muted-foreground"}>{empty ? copy(language, "未加入订阅", "Not in subscriptions") : copy(language, `${server.readyCombinations} 个组合就绪`, `${server.readyCombinations} routes ready`)}</span>{server.failedCombinations > 0 ? <span className="text-destructive">{copy(language, `${server.failedCombinations} 个失败`, `${server.failedCombinations} failed`)}</span> : null}{server.withheldCombinations > 0 ? <span className="text-muted-foreground">{copy(language, `${server.withheldCombinations} 个暂缓`, `${server.withheldCombinations} withheld`)}</span> : null}</div>;
}

function AddLandingNode({ state, language, onAdded }: { state: LandingState; language: Language; onAdded: () => void }) {
  const quality = useIPQuality();
  const [candidateID, setCandidateID] = useState("");
  const id = useId();
  const { view, busy, failed } = state;
  const candidates = view?.candidates.filter((candidate) => !view.nodeIds.includes(candidate.nodeId)) ?? [];
  const candidate = candidates.find((item) => item.nodeId === candidateID);
  const disabled = !view || busy || failed;
  const regionReady = Boolean(candidate && state.regions[candidate.nodeId]);
  return <form onSubmit={async (event) => {
    event.preventDefault();
    if (disabled || !view || !candidate || !regionReady || view.nodeIds.length >= 16) return;
    const nodeIds = [...view.nodeIds, candidate.nodeId];
    if (await state.change((signal) => api.selectLanding(nodeIds, view.revision, selectedRegions(state, nodeIds), signal))) onAdded();
  }}><FieldGroup><Field><FieldLabel htmlFor={id}>{copy(language, "选择落地节点", "Choose a landing node")}</FieldLabel><div className="flex gap-2">
    <Select items={candidates.map((item) => ({ value: item.nodeId, label: item.name }))} value={candidate?.nodeId ?? null} disabled={disabled || !candidates.length || (view?.nodeIds.length ?? 0) >= 16} onValueChange={(value) => setCandidateID(value ?? "")}>
      <SelectTrigger id={id} className="min-w-0 flex-1"><SelectValue placeholder={copy(language, "选择节点", "Choose a node")} /></SelectTrigger><SelectContent className="apps-workspace"><SelectGroup>{candidates.map((item) => <SelectItem key={item.nodeId} value={item.nodeId}><RegionFlag code={state.regions[item.nodeId]} language={language} />{item.name} · {assessmentLabel(language, quality?.checks.find((check) => check.agentId === item.nodeId)?.assessment)}</SelectItem>)}</SelectGroup></SelectContent>
    </Select><Button type="submit" size="sm" disabled={disabled || !candidate || !regionReady || (view?.nodeIds.length ?? 0) >= 16}>{copy(language, "添加", "Add")}</Button></div>
    <FieldDescription>{candidate && !regionReady ? copy(language, "正在识别落地区域，完成后才能添加。", "Detecting the landing region before it can be added.") : (view?.nodeIds.length ?? 0) >= 16 ? copy(language, "最多可配置 16 台落地机。", "Up to 16 landing nodes.") : copy(language, "仅显示在线且已接入私网的节点。", "Only online nodes on the managed private network are listed.")}</FieldDescription>
  </Field></FieldGroup></form>;
}

function LandingPoolNotice({ state, language }: { state: LandingState; language: Language }) {
  const { view, busy, failed } = state;
  const missing = view?.nodeIds.filter((id) => !view.landingRegionCodes?.[id]) ?? [];
  if (state.changeError == null && !missing.length) return null;
  return <TableRow className="block lg:table-row"><TableCell colSpan={5} className="block whitespace-normal lg:table-cell"><div className="flex flex-col gap-3 py-2">
    {state.changeError != null ? <Alert variant="destructive"><AlertTitle>{copy(language, "落地设置未更新", "Landing settings were not updated")}</AlertTitle><AlertDescription>{userError(language, state.changeError)}</AlertDescription></Alert> : null}
    {view && missing.length > 0 ? <Alert><AlertTitle>{copy(language, "落地地区信息未同步", "Landing regions need syncing")}</AlertTitle><AlertDescription><Button type="button" variant="outline" size="sm" disabled={busy || failed || !missing.every((id) => state.regions[id])} onClick={() => { void state.change((signal) => api.selectLanding(view.nodeIds, view.revision, selectedRegions(state, view.nodeIds), signal)); }}>{copy(language, "同步地区并修复订阅", "Sync regions and repair subscription")}</Button></AlertDescription></Alert> : null}
  </div></TableCell></TableRow>;
}
