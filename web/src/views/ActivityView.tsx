import { HistoryIcon } from "lucide-react";
import type { AgentView, Action } from "../types";
import type { Language } from "../translations";
import { PageHeading, StateBadge, copy, formatDate } from "./shared";
import { Card, CardContent } from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

export function ActivityView({ actions, agents, language }: { actions: Action[]; agents: AgentView[]; language: Language }) {
  const agentNames = new Map(agents.map((agent) => [agent.id, agent.name]));
  return <section className="flex flex-col gap-7"><PageHeading title={copy(language, "活动", "Activity")} description={copy(language, "安装、Gateway、Tunnel 和 DNS 同步过程都会记录在这里。", "Install, Gateway, Tunnel, and DNS synchronization progress is recorded here.")} />{actions.length === 0 ? <Empty className="border"><EmptyHeader><EmptyMedia variant="icon"><HistoryIcon /></EmptyMedia><EmptyTitle>{copy(language, "还没有活动记录", "No activity yet")}</EmptyTitle><EmptyDescription>{copy(language, "创建安装或发布任务后，进度会显示在这里。", "Progress appears here after an install or publication task is created.")}</EmptyDescription></EmptyHeader></Empty> : <Card><CardContent className="p-0"><Table><TableHeader><TableRow><TableHead>{copy(language, "状态", "Status")}</TableHead><TableHead>{copy(language, "操作", "Action")}</TableHead><TableHead>{copy(language, "节点", "Node")}</TableHead><TableHead>{copy(language, "详情", "Details")}</TableHead><TableHead className="text-right">{copy(language, "时间", "Time")}</TableHead></TableRow></TableHeader><TableBody>{actions.map((action) => <TableRow key={action.id}><TableCell><StateBadge value={action.event} /></TableCell><TableCell><p className="font-medium">{action.kind}</p><p className="mt-0.5 font-mono text-xs text-muted-foreground">r{action.revision}</p></TableCell><TableCell>{agentNames.get(action.agentId) ?? action.agentId}</TableCell><TableCell className="max-w-sm whitespace-normal text-muted-foreground">{action.message || "—"}</TableCell><TableCell className="text-right text-muted-foreground">{formatDate(language, action.createdAt)}</TableCell></TableRow>)}</TableBody></Table></CardContent></Card>}</section>;
}
