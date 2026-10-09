import { ActivityIcon, AppWindowIcon, BotIcon, CompassIcon, FolderIcon, GaugeIcon, GlobeIcon, LayoutDashboardIcon, MonitorIcon, NetworkIcon, SendIcon, ServerIcon, SettingsIcon, StoreIcon, TerminalIcon, type LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";

const icons: Record<string, { icon: LucideIcon; tone: string }> = {
  home: { icon: MonitorIcon, tone: "blue" },
  apps: { icon: StoreIcon, tone: "blue" },
  nodes: { icon: ServerIcon, tone: "slate" },
  settings: { icon: SettingsIcon, tone: "slate" },
  network: { icon: NetworkIcon, tone: "violet" },
  overview: { icon: LayoutDashboardIcon, tone: "teal" },
  activity: { icon: ActivityIcon, tone: "amber" },
  assistant: { icon: BotIcon, tone: "violet" },
  "vastora-official/meridian": { icon: CompassIcon, tone: "teal" },
  "vastora-official/pulse": { icon: ActivityIcon, tone: "blue" },
  "vastora-official/pulse-agent": { icon: GaugeIcon, tone: "blue" },
  "vastora-official/telegram-bot": { icon: SendIcon, tone: "cyan" },
  "vastora-official/headscale": { icon: NetworkIcon, tone: "violet" },
  "vastora-official/cpa": { icon: TerminalIcon, tone: "amber" },
  "vastora-official/filebrowser": { icon: FolderIcon, tone: "blue" },
  "vastora-official/3x-ui": { icon: GlobeIcon, tone: "teal" },
};

export function AppIcon({ appKey, className }: { appKey: string; className?: string }) {
  const { icon: Icon, tone } = icons[appKey] ?? { icon: AppWindowIcon, tone: "slate" };
  return <span aria-hidden="true" className={cn("desktop-app-icon size-14", className)} data-tone={tone}><Icon strokeWidth={1.7} /></span>;
}
