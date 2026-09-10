import { BadgeCheckIcon, InfoIcon, ShieldAlertIcon } from "lucide-react";
import type { AppView } from "../types";
import type { Language } from "../translations";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import { copy, HighPrivilegeBadge } from "./shared";

// Center reserves this namespace and verifies its catalog signature. Being
// included in that catalog alone does not make third-party software our product.
export function isOfficialProduct(app: AppView) {
  return app.sourceId === "vastora-official"
    && app.key === `vastora-official/${app.app.id}`
    && (app.app.id === "pulse" || app.app.id === "pulse-agent");
}

export function AppIdentityBadge({ app, language }: { app: AppView; language: Language }) {
  if (isOfficialProduct(app)) {
    return <Badge variant="outline" aria-label={copy(language, "Petauron 官方应用", "Official Petauron app")}>
      <BadgeCheckIcon aria-hidden="true" data-icon="inline-start" />
      {copy(language, "官方", "Official")}
    </Badge>;
  }
  return app.app.hostAccess ? <HighPrivilegeBadge language={language} /> : null;
}

export function AppHostAccessNote({ app, language }: { app: AppView; language: Language }) {
  if (!app.app.hostAccess) return null;
  const official = isOfficialProduct(app);
  const Icon = official ? InfoIcon : ShieldAlertIcon;
  return <p className={cn("flex items-start gap-2 text-xs leading-5", official ? "text-muted-foreground" : "text-destructive")}>
    <Icon aria-hidden="true" className="mt-0.5 size-3.5 shrink-0" />
    {official
      ? copy(language, "在节点上运行，读取主机监控指标。", "Runs on the node and reads host monitoring metrics.")
      : copy(language, "此应用需要主机级权限，请确认来源与用途。", "This app needs host-level access. Confirm its source and purpose.")}
  </p>;
}
