import { AppWindowIcon, ArrowRightIcon, CircleCheckIcon, CircleArrowUpIcon, ServerIcon } from "lucide-react";
import { actionKind, groupActions, visibleActionMessage } from "./activityPresentation";
import type { AppData, Mutate, Screen } from "../App";
import type { Language } from "../translations";
import { PageHeading, StateBadge, copy, formatDate } from "./shared";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Alert, AlertTitle, AlertDescription } from "@/components/ui/alert";
import { publicationNeedsAttention } from "./installed-apps-model";
import { HomeTaskSummary } from "./HomeTaskSummary";
import { LocationsCard } from "./SiteManagement";

export function HomeView({ data, language, onNavigate, mutate }: { data: AppData; language: Language; onNavigate: (screen: Screen) => void; mutate: Mutate }) {
  const activeAgents = data.agents.filter((agent) => agent.status === "active");
  const connected = activeAgents.filter((agent) => agent.connected && !agent.credentialRevoked).length;
  const readyEntries = data.publications.filter((publication) => publication.status === "ready" && !publicationNeedsAttention(publication)).length;
  const entryIssues = data.publications.filter((publication) => publication.status !== "stopped" && publicationNeedsAttention(publication)).length;
  const appIssues = data.applications.filter((app) => ["failed", "degraded"].includes(app.status)).length;
  const needsNetwork = activeAgents.filter((agent) => !agent.networkProfile).length;
  const recentActions = groupActions(data.actions).slice(0, 5).map((group) => group.actions[0]);
  const attention = [
    connected < activeAgents.length ? copy(language, `${activeAgents.length - connected} 台节点未连接`, `${activeAgents.length - connected} nodes disconnected`) : "",
    appIssues ? copy(language, `${appIssues} 个应用异常`, `${appIssues} app issues`) : "",
    entryIssues ? copy(language, `${entryIssues} 个访问入口异常`, `${entryIssues} access issues`) : "",
  ].filter(Boolean);
  return <section className="mac-overview flex min-w-0 flex-col gap-5">
    <PageHeading title={copy(language, "系统概览", "Overview")} description={copy(language, "先看运行状态，再处理需要关注的事项。", "Check current status and anything that needs attention.")} />
    {attention.length ? <Alert><AlertTitle>{attention.join(" · ")}</AlertTitle><AlertDescription><Button onClick={() => onNavigate(connected < activeAgents.length ? "nodes" : "apps")} size="sm" variant="outline">{copy(language, "查看运行状态", "View status")}</Button></AlertDescription></Alert> : null}
    <HomeTaskSummary data={data} language={language} onNavigate={onNavigate} />
    <div className="mac-overview-summary">
      {[{ screen: "nodes" as Screen, icon: ServerIcon, value: `${connected}/${activeAgents.length}`, label: copy(language, "节点在线", "Nodes online") }, { screen: "apps" as Screen, icon: AppWindowIcon, value: data.applications.filter((app) => app.status === "running").length, label: copy(language, "应用运行中", "Apps running") }, { screen: "apps" as Screen, icon: CircleCheckIcon, value: readyEntries, label: copy(language, "访问入口就绪", "Access points ready") }].map(({ screen, icon: Icon, value, label }) => <Button className="h-auto justify-between p-4" key={label} variant="outline" onClick={() => onNavigate(screen)}><span className="flex items-center gap-3"><Icon data-icon="inline-start" /><span className="text-left"><strong className="block text-xl tabular-nums">{value}</strong><span className="text-xs text-muted-foreground">{label}</span></span></span><ArrowRightIcon data-icon="inline-end" /></Button>)}
    </div>
    {data.centerUpdate.updateAvailable ? <Card size="sm"><CardHeader><CardTitle className="flex items-center gap-2"><CircleArrowUpIcon />{copy(language, "管理中心有新版本", "Management center update available")}</CardTitle><CardDescription>{data.centerUpdate.latestVersion}</CardDescription><CardAction><Button onClick={() => { onNavigate("settings"); window.history.replaceState({}, "", "/settings#updates"); window.dispatchEvent(new HashChangeEvent("hashchange")); }} size="sm" variant="outline">{copy(language, "查看更新", "View update")}</Button></CardAction></CardHeader></Card> : null}
    <SetupGuide activeAgents={activeAgents.length} language={language} needsNetwork={needsNetwork} onNavigate={onNavigate} runningApps={data.applications.filter((app) => app.status === "running").length} />
    <Card size="sm"><CardHeader><CardTitle>{copy(language, "最近活动", "Recent activity")}</CardTitle><CardAction><Button onClick={() => onNavigate("activity")} size="sm" variant="ghost">{copy(language, "查看全部", "View all")}<ArrowRightIcon data-icon="inline-end" /></Button></CardAction></CardHeader><CardContent className="flex flex-col gap-2">
      {recentActions.length ? recentActions.map((action) => <Button className="h-auto min-w-0 justify-start gap-3 whitespace-normal py-3 text-left" key={action.id} onClick={() => onNavigate("activity")} variant="ghost"><StateBadge language={language} value={action.currentState || action.event} /><span className="min-w-0 flex-1"><span className="block text-sm font-medium">{data.agents.find((agent) => agent.id === action.agentId)?.name ?? copy(language, "未知节点", "Unknown node")} · {actionKind(language, action.kind)}</span><span className="mt-1 block text-xs text-muted-foreground">{visibleActionMessage(language, action)}</span></span><time className="hidden shrink-0 text-xs text-muted-foreground sm:block">{formatDate(language, action.createdAt)}</time></Button>) : <p className="py-3 text-sm text-muted-foreground">{copy(language, "暂无活动。添加节点或安装应用后，进度会显示在这里。", "No activity yet. Node and app operations will appear here.")}</p>}
    </CardContent></Card>
    <LocationsCard data={data} language={language} mutate={mutate} />
  </section>;
}

function SetupGuide({ activeAgents, language, needsNetwork, onNavigate, runningApps }: { activeAgents: number; language: Language; needsNetwork: number; onNavigate: (screen: Screen) => void; runningApps: number }) {
  const steps = [
    { done: activeAgents > 0, title: copy(language, "添加第一台节点", "Add your first node"), description: copy(language, "在 Linux 设备运行 Center 生成的一条命令。", "Run one command generated by Center on a Linux device."), screen: "nodes" as Screen, action: copy(language, "添加节点", "Add node") },
    { done: activeAgents > 0 && needsNetwork === 0, title: copy(language, "确认节点网络", "Confirm node networking"), description: copy(language, "使用 Agent 自动发现的建议地址，不需要手动判断网卡。", "Use the addresses suggested by Agent; no interface knowledge is required."), screen: "network" as Screen, action: copy(language, "确认网络", "Confirm network") },
    { done: runningApps > 0, title: copy(language, "安装第一个应用", "Install your first app"), description: copy(language, "选择应用和节点，其他选项都有安全默认值。", "Choose an app and node; the remaining options have safe defaults."), screen: "apps" as Screen, action: copy(language, "打开应用商店", "Open app store") }
  ];
  const current = steps.findIndex((step) => !step.done);
  if (current === -1) return null;
  const completed = steps.filter((step) => step.done).length;
  return <Card className="overflow-hidden"><CardHeader><CardTitle>{copy(language, "完成首次设置", "Finish setup")}</CardTitle><CardDescription>{copy(language, "一次只完成当前步骤，Vastora 会自动显示下一步。", "Complete one step at a time; Vastora reveals what comes next automatically.")}</CardDescription><CardAction><span className="text-sm font-medium text-muted-foreground">{completed}/{steps.length}</span></CardAction></CardHeader><CardContent><ol className="flex flex-col">{steps.map((step, index) => <li aria-current={index === current ? "step" : undefined} className="grid min-h-16 grid-cols-[20px_minmax(0,1fr)] items-start gap-x-3 border-b py-3 last:border-b-0 sm:flex" key={step.title}>{step.done ? <CircleCheckIcon aria-hidden="true" className="mt-0.5 size-5 shrink-0 text-success" /> : <span aria-hidden="true" className={`grid size-5 shrink-0 place-items-center rounded-full border text-[11px] font-semibold ${index === current ? "border-primary-control bg-primary-control text-primary-foreground" : "text-muted-foreground"}`}>{index + 1}</span>}<div className="min-w-0 flex-1"><p className="text-sm font-medium">{step.title}</p><p className="mt-1 text-xs leading-5 text-muted-foreground">{step.description}</p></div>{index === current ? <Button className="col-start-2 mt-2 w-full sm:mt-0 sm:w-auto sm:shrink-0" onClick={() => onNavigate(step.screen)} size="sm">{step.action}<ArrowRightIcon data-icon="inline-end" /></Button> : null}</li>)}</ol></CardContent></Card>;
}
