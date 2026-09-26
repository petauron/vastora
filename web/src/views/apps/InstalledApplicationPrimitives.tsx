import { ShieldAlertIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { cn } from "@/lib/utils";
import type { Application, Publication, Service } from "@/types";
import type { Language } from "@/translations";
import { operationLabel } from "../appAccess";
import { catalogInstallBlocked, copy, StateBadge } from "../shared";
import { publicationNeedsAttention, serviceNeedsAttention, threeXUIAppKey, type InstalledAppInstance } from "../installed-apps-model";

export function ApplicationUpdate({ instance, language, onUpgrade }: { instance: InstalledAppInstance; language: Language; onUpgrade: (application: Application) => void }) {
  const { application, app, agent, activeChange } = instance;
  if (!application.updateAvailable || activeChange) return null;
  const legacy = application.appKey === threeXUIAppKey && application.role === "master" && Boolean(application.controllerApplicationId) && application.id !== application.controllerApplicationId;
  const disabled = !agent?.connected || legacy || catalogInstallBlocked(app) || ["pending", "deploying"].includes(application.status);
  const name = agent?.name ?? application.nodeId;
  return <Button aria-label={copy(language, `更新 ${name} 的应用`, `Update application on ${name}`)} className="max-md:min-h-11" disabled={disabled} onClick={() => onUpgrade(application)} size="sm" variant="outline" title={copy(language, `更新到 v${application.availableVersion}`, `Update to v${application.availableVersion}`)}>
    {application.status === "failed" ? copy(language, "重试更新", "Retry update") : copy(language, "更新", "Update")}
  </Button>;
}

export function ApplicationPrimaryStatus({ instance, language }: { instance: InstalledAppInstance; language: Language }) {
  const { application, activeChange } = instance;
  if (activeChange) return <Badge role="status" variant={activeChange.reconciliationRequired ? "destructive" : "outline"}>
    {activeChange.reconciliationRequired ? <ShieldAlertIcon aria-hidden="true" data-icon="inline-start" /> : <Spinner aria-hidden="true" data-icon="inline-start" className="motion-reduce:animate-none" />}
    {activeChange.reconciliationRequired ? copy(language, "需要恢复", "Recovery required")
      : activeChange.operation === "upgrade" ? copy(language, "正在更新", "Updating")
      : copy(language, `正在${operationLabel(language, activeChange.operation)}`, `${operationLabel(language, activeChange.operation)} in progress`)}
  </Badge>;
  return application.status === "running"
    ? <span className={cn("inline-flex items-center gap-2", application.appKey === threeXUIAppKey && "text-muted-foreground")}><span aria-hidden="true" className="apps-status-dot bg-latency-fast" />{copy(language, "运行中", "Running")}</span>
    : <StateBadge language={language} value={application.status} />;
}

export function ApplicationStatus({ instance, language, onUpgrade }: { instance: InstalledAppInstance; language: Language; onUpgrade: (application: Application) => void }) {
  const { application, activeChange, agent } = instance;
  const syncing = application.role === "worker" && (application.nodeSyncStatus === "pending" || application.nodeSyncStatus === "applying");
  const syncFailed = application.role === "worker" && (!instance.controller || !["ready", "pending", "applying"].includes(application.nodeSyncStatus ?? ""));

  return <div className="flex flex-col items-start gap-1.5">
    <ApplicationPrimaryStatus instance={instance} language={language} />
    {!activeChange && syncing ? <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground"><Spinner aria-hidden="true" />{copy(language, "正在接入订阅主机", "Connecting to controller")}</span> : null}
    {!activeChange && syncFailed ? <span className="text-xs text-destructive">{copy(language, "尚未接入订阅主机", "Controller not connected")}</span> : null}
    {agent && !agent.connected ? <span className="text-xs text-destructive">{copy(language, "节点离线", "Node offline")}</span> : null}
    <ApplicationUpdate instance={instance} language={language} onUpgrade={onUpgrade} />
  </div>;
}

export function AccessStatus({ services, publications, language, threeXUI }: { services: Service[]; publications: Publication[]; language: Language; threeXUI: boolean }) {
  const attention = publications.some(publicationNeedsAttention) || services.some(serviceNeedsAttention);
  const changing = publications.find((publication) => publication.status === "pending" || publication.status === "applying");
  const hardening = services.some((service) => service.guardStatus === "pending" || service.guardStatus === "hardening");

  if (attention) return <div className="flex flex-col items-start gap-1">
    <span className="inline-flex items-center gap-1.5 text-sm text-destructive"><ShieldAlertIcon aria-hidden="true" className="size-4 shrink-0" />{copy(language, "入口待处理", "Access needs attention")}</span>
    
  </div>;
  if (hardening) return <StateBadge language={language} value="applying" />;
  if (changing) return <StateBadge language={language} value={changing.status} />;
  if (publications.length > 0) return <span className="inline-flex items-center gap-2 text-muted-foreground">{!threeXUI ? <span aria-hidden="true" className="apps-status-dot bg-latency-fast" /> : null}{copy(language, "就绪", "Ready")}</span>;
  return <span className="text-xs text-muted-foreground">
    {threeXUI && services.length === 0 ? copy(language, "尚未创建 VLESS", "VLESS not configured") : services.length > 0 ? copy(language, "未添加入口", "No access point") : copy(language, "无需访问入口", "No access point needed")}
  </span>;
}

