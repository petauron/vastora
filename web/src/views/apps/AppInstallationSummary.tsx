import { ArrowUpCircleIcon, CircleAlertIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { Language } from "@/translations";
import { publicationNeedsAttention, serviceNeedsAttention, type InstalledAppGroup } from "../installed-apps-model";
import { copy } from "../shared";

export function AppInstallationSummary({ group, language }: { group: InstalledAppGroup; language: Language }) {
  const attention = (instance: InstalledAppGroup["instances"][number]) =>
    ["failed", "degraded"].includes(instance.application.status) || instance.activeChange?.reconciliationRequired
    || Boolean(instance.agent && (!instance.agent.connected || instance.agent.credentialRevoked))
    || instance.application.nodeSyncStatus === "failed" || instance.application.adoptionState === "blocked"
    || instance.services.some(serviceNeedsAttention) || instance.publications.some(publicationNeedsAttention);
  const affected = group.instances.filter(attention).length;
  const changing = group.instances.filter((instance) => instance.activeChange && !attention(instance)).length;
  const running = group.instances.filter((instance) => instance.application.status === "running" && !instance.activeChange && (!instance.agent || (instance.agent.connected && !instance.agent.credentialRevoked))).length;
  const updates = group.instances.filter((instance) => instance.application.updateAvailable).length;
  const versions = [...new Set(group.instances.map(({ application }) =>
    `v${application.installedVersion} · r${application.installedPackageRevision ?? 0}`))];
  return <div className="flex flex-col gap-2">
    <p className="break-words text-xs" title={versions.join(" / ")}>{copy(language, "已安装", "Installed")} {versions.length === 1 ? versions[0] : copy(language, `${versions.length} 种版本`, `${versions.length} versions`)}</p>
    <div className="flex flex-wrap gap-1.5">
      {affected ? <Badge variant="destructive"><CircleAlertIcon aria-hidden="true" data-icon="inline-start" />{copy(language, `${affected} 台需处理`, `${affected} need attention`)}</Badge> : null}
      {changing ? <Badge variant="secondary">{copy(language, `${changing} 台处理中`, `${changing} in progress`)}</Badge> : null}
      {!changing && !affected && !running ? <Badge variant="secondary">{copy(language, "未运行", "Not running")}</Badge> : <Badge variant="outline">{running === group.instances.length ? copy(language, "运行中", "Running") : copy(language, `运行 ${running}/${group.instances.length}`, `Running ${running}/${group.instances.length}`)}</Badge>}
      {updates ? <Badge variant="secondary"><ArrowUpCircleIcon aria-hidden="true" data-icon="inline-start" />{copy(language, `${updates} 台可更新`, `${updates} updatable`)}</Badge> : null}
    </div>
  </div>;
}
