import { useEffect, useId, useState } from "react";
import { api } from "../api";
import type { IPQualityPreferences, IPQualityResponse } from "../ip-quality-types";
import type { Language } from "../translations";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Field, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { AssessmentBadge, AssessmentSummary } from "./IPAssessment";
import { copy } from "./shared";
import { unlockLabel } from "./ipQualityModel";

const defaultPreferences: IPQualityPreferences = { requiredServices: ["ChatGPT", "Netflix", "DisneyPlus"], targetRegion: "" };
const serviceOptions = ["ChatGPT", "Netflix", "DisneyPlus", "Youtube", "AmazonPrimeVideo", "TikTok", "Reddit"];
const serviceName = (name: string) => name === "DisneyPlus" ? "Disney+" : name === "Youtube" ? "YouTube" : name === "AmazonPrimeVideo" ? "Prime Video" : name;

type Props = {
  language: Language;
  nodeId?: string;
  nodes?: { id: string; name: string }[];
  allowedNodeIds?: string[];
  onSelect?: (nodeId: string) => void;
};

export function IPQualityComparison({ language, ...props }: Props) {
  const [open, setOpen] = useState(false);
  const id = useId();
  return <section className="flex min-w-0 flex-col gap-2">
    <Button type="button" size="sm" variant="outline" className="self-start" aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)}>{copy(language, "比较落地质量", "Compare egress quality")}</Button>
    {open ? <div id={id}><ComparisonPanel language={language} {...props} /></div> : null}
  </section>;
}

function ComparisonPanel({ language, nodeId, nodes = [], allowedNodeIds, onSelect }: Props) {
  const [selectedNode, setSelectedNode] = useState("");
  const currentID = nodeId ?? selectedNode;
  const [preferences, setPreferences] = useState(defaultPreferences);
  const [required, setRequired] = useState(defaultPreferences.requiredServices);
  const [region, setRegion] = useState("");
  const [response, setResponse] = useState<{ key: string; value: IPQualityResponse } | null>(null);
  const [error, setError] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const id = useId();
  const requiredKey = preferences.requiredServices.join(",");
  const targetRegion = preferences.targetRegion;
  const key = `${currentID}|${requiredKey}|${targetRegion}`;
  useEffect(() => {
    const controller = new AbortController();
    const timeout = setTimeout(() => { setError(true); controller.abort(); }, 15000);
    setError(false);
    void api.ipQuality(controller.signal, { requiredServices: requiredKey ? requiredKey.split(",") : [], targetRegion }, currentID).then((value) => {
      if (!controller.signal.aborted) setResponse({ key, value });
    }).catch(() => {
      if (!controller.signal.aborted) setError(true);
    }).finally(() => clearTimeout(timeout));
    return () => { clearTimeout(timeout); controller.abort(); };
  }, [currentID, requiredKey, targetRegion, key, refresh]);
  useEffect(() => {
    const timer = setInterval(() => { if (!document.hidden) setRefresh((value) => value + 1); }, 15000);
    return () => clearInterval(timer);
  }, []);
  const data = response?.key === key && !error ? response.value : null;
  const current = data?.checks.find((value) => value.agentId === currentID);
  const candidates = data?.comparisons.filter((value) => !allowedNodeIds || allowedNodeIds.includes(value.nodeId)) ?? [];
  const formal = candidates.filter((value) => value.assessment.status === "complete");
  const conservative = candidates.filter((value) => value.assessment.status === "conservative");
  const provisional = candidates.filter((value) => value.assessment.status !== "complete" && value.assessment.status !== "conservative");
  const reasons: Record<string, [string, string]> = {
    connection_unverified: ["连接待验证", "Connection unverified"], recheck: ["需复测后比较", "Recheck before comparing"],
    requirements_not_met: ["必需服务未满足", "Required services unavailable"], unlocks_improved: ["推荐 · 改善解锁", "Recommended · Unlock improvement"],
    score_improved: ["推荐 · 提升至少 10 分", "Recommended · At least 10 points better"], no_clear_improvement: ["暂无明显提升", "No clear improvement"],
  };
  return <div className="flex min-w-0 flex-col gap-3 rounded-lg border p-3">
    {!nodeId ? <Field><FieldLabel htmlFor={`${id}-node`}>{copy(language, "当前入口", "Current entry")}</FieldLabel><Select value={selectedNode || null} onValueChange={(value) => setSelectedNode(value ?? "")} items={nodes.map((node) => ({ value: node.id, label: node.name }))}><SelectTrigger id={`${id}-node`}><SelectValue placeholder={copy(language, "选择入口进行比较", "Choose an entry to compare")} /></SelectTrigger><SelectContent><SelectGroup>{nodes.map((node) => <SelectItem value={node.id} key={node.id}>{node.name}</SelectItem>)}</SelectGroup></SelectContent></Select></Field> : null}
    <form className="flex flex-wrap items-end gap-3" onSubmit={(event) => { event.preventDefault(); setPreferences({ requiredServices: required, targetRegion: region }); }}>
      <FieldSet className="min-w-0 flex-1"><FieldLegend variant="label">{copy(language, "必需服务", "Required services")}</FieldLegend><div className="flex flex-wrap gap-x-4 gap-y-2">{serviceOptions.map((name) => <Field key={name} orientation="horizontal" className="w-auto"><Checkbox id={`${id}-${name}`} checked={required.includes(name)} onCheckedChange={(checked) => setRequired((values) => serviceOptions.filter((item) => item === name ? checked : values.includes(item)))} /><FieldLabel className="text-xs" htmlFor={`${id}-${name}`}>{serviceName(name)}</FieldLabel></Field>)}</div></FieldSet>
      <Field className="w-28"><FieldLabel className="text-xs" htmlFor={`${id}-region`}>{copy(language, "国家（可选）", "Country (optional)")}</FieldLabel><Input id={`${id}-region`} placeholder="US" value={region} maxLength={2} pattern="[A-Z]{2}|^$" onChange={(event) => setRegion(event.target.value.toUpperCase())} /></Field>
      <Button type="submit" size="sm">{copy(language, "应用条件", "Apply filters")}</Button>
    </form>
    {current ? <AssessmentSummary assessment={current.assessment} report={current.report} checkedAt={current.checkedAt} language={language} /> : null}
    {error ? <p role="alert" className="text-xs text-destructive">{copy(language, "比较数据读取失败", "Could not load comparison")} <Button type="button" size="sm" variant="ghost" onClick={() => setRefresh((value) => value + 1)}>{copy(language, "重试", "Retry")}</Button></p> : !data ? <p role="status" className="text-xs text-muted-foreground">{copy(language, "正在读取评分…", "Loading assessments…")}</p> : null}
    {currentID && data ? <>
      {[{ title: copy(language, "正式评分", "Formal ranking"), values: formal }, { title: copy(language, "缺 IPQS", "IPQS unavailable"), values: conservative }, { title: copy(language, "暂评 / 待复测", "Provisional / Recheck"), values: provisional }].filter((group) => group.values.length > 0).map((group) => <section key={group.title} className="min-w-0"><h4 className="mb-1 text-xs font-medium">{group.title}</h4><Table className="text-xs"><TableHeader><TableRow><TableHead>{copy(language, "落地", "Egress")}</TableHead><TableHead>{copy(language, "评分 / 提升", "Score / Change")}</TableHead><TableHead>{copy(language, "解锁与地区", "Unlocks / Regions")}</TableHead><TableHead>{copy(language, "建议", "Advice")}</TableHead>{onSelect ? <TableHead className="sr-only">{copy(language, "选择", "Select")}</TableHead> : null}</TableRow></TableHeader><TableBody>{group.values.map((item) => <TableRow key={item.nodeId}>
        <TableCell className="max-w-40 truncate" title={item.name}>{item.name}</TableCell><TableCell><AssessmentBadge language={language} assessment={item.assessment} />{item.delta != null ? <span className="ml-2 tabular-nums" title={item.assessment.status === "conservative" || current?.assessment?.status === "conservative" ? copy(language, "候选最低分减当前最高分", "Candidate minimum minus current maximum") : undefined}>{item.delta > 0 ? "+" : ""}{item.delta}{item.assessment.status === "conservative" || current?.assessment?.status === "conservative" ? copy(language, "（下界）", " (lower bound)") : null}</span> : null}</TableCell>
        <TableCell><div className="flex flex-col gap-1">{preferences.requiredServices.map((name) => { const service = item.services.find((value) => value.name === name); return <span key={name}>{serviceName(name)} {unlockLabel(language, service?.status)} · {service?.regionCode || "—"}</span>; })}</div></TableCell>
        <TableCell className={item.recommended ? "text-latency-fast" : "text-muted-foreground"}>{reasons[item.reason] ? copy(language, ...reasons[item.reason]) : item.reason}{item.recommended && !item.connectionVerified ? <> · {copy(language, "连接待验证", "Connection pending verification")}</> : null}</TableCell>
        {onSelect ? <TableCell><Button type="button" size="sm" variant="outline" onClick={() => onSelect(item.nodeId)}>{copy(language, "选择", "Select")}</Button></TableCell> : null}
      </TableRow>)}</TableBody></Table></section>)}
      {!candidates.some((item) => item.recommended) ? <p className="text-xs text-muted-foreground">{copy(language, "暂无明显更好的落地", "No clearly better egress")}</p> : null}
      <p className="text-xs text-muted-foreground">{copy(language, "请同时查看网络表现。选择仅填入表单，不会切换路由。", "Also review network performance. Selection only fills the form; it does not switch routes.")}</p>
    </> : null}
  </div>;
}
