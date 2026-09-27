import { ExternalLinkIcon, Settings2Icon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { AppWorkspaceProps } from "@/app-workspaces/types";
import { ApplicationStatus } from "./apps/InstalledApplicationPrimitives";
import { pulsePrivateAccess } from "./appAccess";
import { copy } from "./shared";

function secureDashboardURL(value?: string) {
  if (!value) return undefined;
  try {
    const url = new URL(value);
    if (url.protocol === "https:" && url.hostname && !url.username && !url.password && !url.hash) return url.href;
  } catch { /* An incomplete access publication cannot open the dashboard. */ }
  return undefined;
}

// Pulse serves and owns its dashboard. Center only shows installation state,
// access publication, and a link to the dashboard's authenticated origin.
export function PulseWorkspace({ group, data, language, onManage, onUpgrade }: AppWorkspaceProps) {
  const instance = group.instances[0];
  const publication = pulsePrivateAccess(data);
  const accessURL = publication && !publication.actionRequired && !publication.lastError ? secureDashboardURL(publication.accessUrl) : undefined;

  return <Card aria-label="Pulse" data-app-workspace="vastora-official/pulse">
    <CardHeader className="flex flex-row items-center justify-between gap-3">
      <CardTitle>Pulse</CardTitle>
      <ApplicationStatus instance={instance} language={language} onUpgrade={onUpgrade} />
    </CardHeader>
    <CardContent className="flex flex-wrap items-center gap-3">
      {accessURL ? <Button nativeButton={false} render={<a href={accessURL} rel="noreferrer" target="_blank" />}>
        <ExternalLinkIcon data-icon="inline-start" />{copy(language, "打开 Pulse 监控", "Open Pulse dashboard")}
      </Button> : <Badge variant="secondary">{copy(language, "等待配置访问入口", "Access point needed")}</Badge>}
      <Button onClick={() => onManage(instance.application)} size="sm" variant="outline">
        <Settings2Icon data-icon="inline-start" />{copy(language, "安装与访问", "Installation & access")}
      </Button>
    </CardContent>
  </Card>;
}
