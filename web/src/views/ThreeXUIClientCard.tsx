import type { ReactNode } from "react";
import { ChevronRightIcon, CopyIcon, LinkIcon, MoreHorizontalIcon, PencilIcon, RotateCcwIcon, Trash2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { DropdownMenu, DropdownMenuContent, DropdownMenuGroup, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Switch } from "@/components/ui/switch";
import type { Language } from "../translations";
import type { ThreeXUIClient, ThreeXUIClientInbound } from "../types";
import { formatBytes } from "./TrafficPlanFields";
import { copy } from "./shared";

type Props = {
  client: ThreeXUIClient;
  inbounds: ThreeXUIClientInbound[];
  language: Language;
  expiryLabel: string;
  busy: boolean;
  linksDisabled: boolean;
  subscriptionAvailable: boolean;
  onEnabledChange: (enabled: boolean) => void;
  onCopySubscription: () => void;
  onCopyLink: () => void;
  onEdit: () => void;
  onReset: () => void;
  onDelete: () => void;
  children?: ReactNode;
};

export function ThreeXUIClientCard({ client, inbounds, language, expiryLabel, busy, linksDisabled, subscriptionAvailable, onEnabledChange, onCopySubscription, onCopyLink, onEdit, onReset, onDelete, children }: Props) {
  const nodes = inbounds.filter((inbound) => client.inboundIds.includes(inbound.id));
  const published = nodes.some((inbound) => inbound.connectHostname);

  return <Card className="@container min-w-0" data-client-email={client.email}>
    <CardHeader className="grid-cols-[minmax(0,1fr)_auto] gap-x-4">
      <CardTitle><h3 className="break-words [overflow-wrap:anywhere]">{client.email}</h3></CardTitle>
      <CardAction className="row-span-1 flex items-center gap-2">
        <label className="flex cursor-pointer items-center gap-2 text-xs text-muted-foreground">
          <span>{client.enabled ? copy(language, "已启用", "Enabled") : copy(language, "已停用", "Disabled")}</span>
          <Switch aria-label={copy(language, `启用 ${client.email}`, `Enable ${client.email}`)} checked={client.enabled} disabled={busy} onCheckedChange={onEnabledChange} />
        </label>
      </CardAction>
      <div className="col-span-2 min-w-0">
        {nodes.length ? <details className="group/nodes">
          <summary className="flex min-h-8 w-fit cursor-pointer list-none items-center gap-1 rounded-md text-xs text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
            <span>{copy(language, `已接入 ${nodes.length} 个节点`, `${nodes.length} connected node(s)`)}</span>
            <ChevronRightIcon aria-hidden="true" className="size-3.5 group-open/nodes:rotate-90" />
          </summary>
          <ul aria-label={copy(language, "已接入节点", "Connected nodes")} className="mt-1 grid gap-2 text-xs text-muted-foreground">
            {nodes.map((node) => <li className="break-words [overflow-wrap:anywhere]" key={node.id}>{node.displayName || node.nodeName || node.name}</li>)}
          </ul>
        </details> : <p className="pt-2 text-xs text-muted-foreground">{copy(language, "未连接节点", "No node attached")}</p>}
      </div>
    </CardHeader>
    <CardContent className="flex flex-col gap-4">
      <div>
        <p className="text-xs text-muted-foreground">{copy(language, "已用流量", "Data used")}</p>
        <p className="mt-1 flex flex-wrap items-baseline gap-x-2 gap-y-1 tabular-nums">
          <span className="text-2xl font-semibold tracking-tight">{formatBytes(client.usedBytes)}</span>
          <span className="text-xs text-muted-foreground">{client.totalBytes ? `/ ${formatBytes(client.totalBytes)}` : copy(language, "不限流量", "Unlimited data")}</span>
        </p>
      </div>
      <dl className="grid grid-cols-2 gap-x-4 gap-y-3 text-xs @min-[22rem]:grid-cols-3">
        <ClientMetric label={copy(language, "有效期", "Expires")} value={expiryLabel} />
        <ClientMetric label={copy(language, "自动续期", "Auto-renewal")} value={client.resetDays ? copy(language, `每 ${client.resetDays} 天`, `Every ${client.resetDays} days`) : copy(language, "关闭", "Off")} />
        <ClientMetric label={copy(language, "设备限制", "Device limit")} value={client.limitIp ? String(client.limitIp) : copy(language, "不限", "Unlimited")} />
      </dl>
      {children}
    </CardContent>
    <CardFooter className="flex-col items-stretch gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <Button className="flex-1" disabled={busy || linksDisabled || !subscriptionAvailable} onClick={onCopySubscription}>
          <CopyIcon aria-hidden="true" data-icon="inline-start" />{copy(language, "复制订阅", "Copy subscription")}
        </Button>
        <Button disabled={busy} onClick={onEdit} variant="outline"><PencilIcon aria-hidden="true" data-icon="inline-start" />{copy(language, "编辑", "Edit")}</Button>
        <DropdownMenu>
          <DropdownMenuTrigger disabled={busy} render={<Button aria-label={copy(language, `更多操作：${client.email}`, `More actions for ${client.email}`)} size="icon" variant="ghost" />}>
            <MoreHorizontalIcon aria-hidden="true" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuGroup>
              <DropdownMenuItem disabled={busy || linksDisabled || !published} onClick={onCopyLink}><LinkIcon aria-hidden="true" />{copy(language, "复制 VLESS", "Copy VLESS")}</DropdownMenuItem>
              <DropdownMenuItem disabled={busy} onClick={onReset}><RotateCcwIcon aria-hidden="true" />{copy(language, "重置流量", "Reset traffic")}</DropdownMenuItem>
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <DropdownMenuGroup>
              <DropdownMenuItem disabled={busy} onClick={onDelete} variant="destructive"><Trash2Icon aria-hidden="true" />{copy(language, "删除客户端", "Delete client")}</DropdownMenuItem>
            </DropdownMenuGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      {!subscriptionAvailable ? <p className="text-xs text-muted-foreground">{copy(language, "开启公网订阅后可复制地址。", "Enable public subscription to copy its URL.")}</p> : null}
    </CardFooter>
  </Card>;
}

function ClientMetric({ label, value }: { label: string; value: string }) {
  return <div className="min-w-0"><dt className="text-muted-foreground">{label}</dt><dd className="mt-1 break-words font-medium tabular-nums">{value}</dd></div>;
}
