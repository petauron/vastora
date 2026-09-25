import { useId, useState } from "react";
import type { IPQualityAssessment, IPQualityReport } from "../ip-quality-types";
import type { Language } from "../translations";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { copy } from "./shared";

export function assessmentLabel(language: Language, assessment?: IPQualityAssessment) {
  if (!assessment) return copy(language, "未检测", "Not checked");
  if (assessment.status === "ip_changed") return copy(language, "IP 已变化", "IP changed");
  if (assessment.status === "expired") return copy(language, "结果已过期", "Expired");
  return assessment.score == null ? copy(language, `暂评 ${assessment.min}～${assessment.max}`, `Provisional ${assessment.min}–${assessment.max}`) : `${assessment.score} / 100`;
}

export function assessmentType(language: Language, type: IPQualityAssessment["ipType"]) {
  const names = { residential: ["家宽", "Residential"], mobile: ["移动网络", "Mobile"], business: ["商业网络", "Business network"], hosting: ["机房", "Hosting"], unknown: ["类型待确认", "Type unconfirmed"] };
  return copy(language, names[type][0], names[type][1]);
}

export function assessmentAdvice(language: Language, value: IPQualityAssessment["advice"]) {
  return value === "direct" ? copy(language, "可直连", "Direct suitable") : value === "compare" ? copy(language, "建议比较落地", "Compare egress") : copy(language, "建议复测", "Recheck needed");
}

export function AssessmentBadge({ language, assessment }: { language: Language; assessment?: IPQualityAssessment }) {
  const grade = assessment?.grade ?? "unknown";
  const labels = { excellent: ["优秀", "Excellent"], premium: ["优质", "Premium"], good: ["良好", "Good"], fair: ["一般", "Fair"], poor: ["较差", "Poor"], unknown: ["待确认", "Unconfirmed"] };
  return <Badge variant="outline" className={`quality-grade quality-grade-${grade}`}>{assessmentLabel(language, assessment)} · {copy(language, labels[grade][0], labels[grade][1])}</Badge>;
}

export function AssessmentSummary({ language, assessment, report, checkedAt }: { language: Language; assessment?: IPQualityAssessment; report?: IPQualityReport; checkedAt?: string }) {
  const [expanded, setExpanded] = useState(false);
  const id = useId();
  if (!assessment) return null;
  const labels: Record<string, [string, string]> = { type: ["IP 类型", "IP type"], sources: ["来源风险", "Provider risk"], ippure: ["IPPure", "IPPure"], unlock: ["实际解锁", "Availability"] };
  const number = (value: number) => Number(value.toFixed(2));
  return <section aria-label={copy(language, "出口适用评分", "Exit suitability assessment")} className="flex flex-col gap-2">
    <div className="flex flex-wrap items-center gap-2 text-xs">
      <Button type="button" variant="ghost" size="sm" className="h-auto min-h-9 px-0" aria-expanded={expanded} aria-controls={id} onClick={() => setExpanded(!expanded)}><AssessmentBadge language={language} assessment={assessment} /></Button>
      <span>{assessmentType(language, assessment.ipType)}</span>
      <span>{assessmentAdvice(language, assessment.advice)}</span>
      {checkedAt ? <time className="text-muted-foreground" dateTime={checkedAt}>{new Date(checkedAt).toLocaleString(language, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })}</time> : null}
    </div>
    {expanded ? <div id={id} className="flex flex-col gap-2 rounded-lg border p-3 text-xs">
      <p className="font-medium">{copy(language, "Meridian 评分 v2 · 出口适用性", "Meridian score v2 · Exit suitability")}</p>
      {assessment.status === "conservative" ? <p>{copy(language, `仅缺 IPQS：该项按 0 / 10 分计算，显示 ${assessment.score} 分；完整区间为 ${assessment.min}～${assessment.max}。未推测 IPQS 原分。`, `Only IPQS is missing: its contribution is set to 0 / 10, showing ${assessment.score} points; the full range is ${assessment.min}–${assessment.max}. No IPQS value was inferred.`)}</p> : null}
      <Table className="text-xs"><TableHeader><TableRow><TableHead>{copy(language, "维度", "Dimension")}</TableHead><TableHead>{copy(language, "贡献 / 满分", "Contribution / Weight")}</TableHead><TableHead>{copy(language, "缺失项", "Missing")}</TableHead></TableRow></TableHeader><TableBody>{assessment.contributions.map((part) => <TableRow key={part.id}><TableCell>{labels[part.id] ? copy(language, ...labels[part.id]) : part.id}</TableCell><TableCell className="tabular-nums">{number(part.min)}{part.min !== part.max ? `～${number(part.max)}` : ""} / {part.weight}</TableCell><TableCell>{part.missing.join("、") || "—"}</TableCell></TableRow>)}</TableBody></Table>
      <p>{copy(language, "类型上限：家宽 100 · 移动 95 · 商业 89 · 机房 79", "Type caps: Residential 100 · Mobile 95 · Business 89 · Hosting 79")}</p>
      <p>{copy(language, "类型证据", "Type evidence")}: {assessment.typeEvidence.map((item) => `${item.source}: ${item.value}`).join(" · ") || "—"}</p>
      <p>{copy(language, "来源原分（越低风险越低）", "Original provider scores (lower risk is better)")}: {report?.scores.filter((item) => ["SCAMALYTICS", "IPQS", "AbuseIPDB"].includes(item.source)).map((item) => `${item.source} ${item.value}`).join(" · ") || "—"}</p>
      <p>IPPure: {report?.ippure?.status === "ok" ? `${report.ippure.riskScore} / 100` : report?.ippure?.status === "unsupported" ? copy(language, "IPv6 暂无评分", "IPv6 risk unavailable") : report?.ippure?.status === "ip_mismatch" ? copy(language, "返回 IP 不匹配", "Returned IP mismatch") : copy(language, "未取得有效结果", "No valid result")}</p>
      {report?.ippure ? <p className="break-all text-muted-foreground">{report.ippure.provider} · {report.ippure.address || "—"} · {new Date(report.ippure.checkedAt).toLocaleString(language)}</p> : null}
      {assessment.requiredFailed.length ? <p className="text-destructive">{copy(language, "必需服务未满足", "Required services unavailable")}: {assessment.requiredFailed.join("、")}</p> : null}
      {assessment.requiredUnknown.length ? <p>{copy(language, "必需服务待确认", "Required services unconfirmed")}: {assessment.requiredUnknown.join("、")}</p> : null}
      <p className="text-muted-foreground">{copy(language, "产品选机规则，不是行业标准或通过概率。缺 IPQS 的评分单独列示，比较提升时用候选下界减当前上界；其他暂评不参与排名。", "Product selection rules, not an industry standard or probability. Scores missing IPQS are listed separately; comparisons use the candidate lower bound minus the current upper bound. Other provisional results are excluded from ranking.")}</p>
    </div> : null}
  </section>;
}
