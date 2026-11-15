import { ArrowUpCircleIcon, CircleAlertIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { Language } from "@/translations";
import { publicationNeedsAttention, serviceNeedsAttention, type InstalledAppGroup } from "../installed-apps-model";
import { copy } from "../shared";

export function AppInstallationSummary({ group, language, compact = false, byNode = false }: { group: InstalledAppGroup; language: Language; compact?: boolean; byNode?: boolean }) {
  const attention = (instance: InstalledAppGroup["instances"][number]) =>
    ["failed", "degraded"].includes(instance.application.status) || instance.activeChange?.reconciliationRequired
    || Boolean(instance.agent && (!instance.agent.connected || instance.agent.credentialRevoked))
    || instance.application.nodeSyncStatus === "failed" || ["pending", "blocked"].includes(instance.application.adoptionState ?? "")
    || ["unconfigured", "action_required", "rolled_back"].includes(instance.application.nodeSiteStatus ?? "")
    || instance.services.some(serviceNeedsAttention) || instance.publications.some(publicationNeedsAttention);
  const units: InstalledAppGroup["instances"][] = [];
  const hosts = new Map<string, InstalledAppGroup["instances"]>();
  for (const instance of group.instances) {
    if (!byNode) { units.push([instance]); continue; }
    const host = hosts.get(instance.application.nodeId);
    if (host) host.push(instance);
    else {
      const instances = [instance];
      hosts.set(instance.application.nodeId, instances);
      units.push(instances);
    }
  }
  const affected = units.filter((instances) => instances.some(attention)).length;
  const changing = units.filter((instances) => instances.some((instance) => instance.activeChange) && !instances.some(attention)).length;
  const running = units.filter((instances) => instances.every((instance) => instance.application.status === "running" && !instance.activeChange && (!instance.agent || (instance.agent.connected && !instance.agent.credentialRevoked)))).length;
  const updates = units.filter((instances) => instances.some((instance) => instance.application.updateAvailable)).length;
  if (compact) {
    const label = affected ? copy(language, `${affected} 台需处理`, `${affected} need attention`)
      : changing ? copy(language, `${changing} 台处理中`, `${changing} in progress`)
      : updates ? copy(language, `${updates} 台可更新`, `${updates} updatable`)
      : running ? copy(language, `运行中 · ${running} 台主机`, `Running · ${running} hosts`)
      : copy(language, "未运行", "Not running");
    return <span className={affected ? "text-destructive" : undefined}>{label}</span>;
  }
  const versions = [...new Set(group.instances.map(({ application }) =>
    `v${application.installedVersion}`))];
  return <div className="flex flex-col gap-2">
    <p className="break-words text-xs" title={versions.join(" / ")}>{copy(language, "已安装", "Installed")} {versions.length === 1 ? versions[0] : copy(language, `${versions.length} 种版本`, `${versions.length} versions`)}</p>
    <div className="flex flex-wrap gap-1.5">
      {affected ? <Badge variant="destructive"><CircleAlertIcon aria-hidden="true" data-icon="inline-start" />{copy(language, `${affected} 台需处理`, `${affected} need attention`)}</Badge> : null}
      {changing ? <Badge variant="secondary">{copy(language, `${changing} 台处理中`, `${changing} in progress`)}</Badge> : null}
      {!changing && !affected && !running ? <Badge variant="secondary">{copy(language, "未运行", "Not running")}</Badge> : <Badge variant="outline">{running === units.length ? copy(language, "运行中", "Running") : copy(language, `运行 ${running}/${units.length}`, `Running ${running}/${units.length}`)}</Badge>}
      {updates ? <Badge variant="secondary"><ArrowUpCircleIcon aria-hidden="true" data-icon="inline-start" />{copy(language, `${updates} 台可更新`, `${updates} updatable`)}</Badge> : null}
    </div>
  </div>;
}
