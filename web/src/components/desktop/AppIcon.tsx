import { ActivityIcon, AppWindowIcon, BotIcon, CableIcon, ChartNoAxesCombinedIcon, CompassIcon, FolderIcon, RadioTowerIcon, RadarIcon, GlobeIcon, HistoryIcon, LayoutDashboardIcon, MonitorIcon, NetworkIcon, SendIcon, ServerIcon, SettingsIcon, StoreIcon, WaypointsIcon, type LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";

const icons: Record<string, { icon: LucideIcon; tone: string }> = {
  home: { icon: MonitorIcon, tone: "blue" },
  apps: { icon: StoreIcon, tone: "blue" },
  nodes: { icon: ServerIcon, tone: "slate" },
  settings: { icon: SettingsIcon, tone: "slate" },
  network: { icon: WaypointsIcon, tone: "violet" },
  overview: { icon: LayoutDashboardIcon, tone: "teal" },
  activity: { icon: HistoryIcon, tone: "amber" },
  assistant: { icon: BotIcon, tone: "violet" },
  "vastora-official/meridian": { icon: CompassIcon, tone: "teal" },
  "vastora-official/pulse": { icon: ActivityIcon, tone: "blue" },
  "vastora-official/pulse-agent": { icon: RadioTowerIcon, tone: "teal" },
  "vastora-official/komari-agent": { icon: RadarIcon, tone: "violet" },
  "vastora-official/telegram-bot": { icon: SendIcon, tone: "cyan" },
  "vastora-official/headscale": { icon: NetworkIcon, tone: "violet" },
  "vastora-official/cpa": { icon: CableIcon, tone: "amber" },
  "vastora-official/keeper": { icon: ChartNoAxesCombinedIcon, tone: "violet" },
  "vastora-official/filebrowser": { icon: FolderIcon, tone: "blue" },
  "vastora-official/3x-ui": { icon: GlobeIcon, tone: "teal" },
};

export function AppIcon({ appKey, className }: { appKey: string; className?: string }) {
  const { icon: Icon, tone } = icons[appKey] ?? { icon: AppWindowIcon, tone: "slate" };
  return <span aria-hidden="true" className={cn("desktop-app-icon size-14", className)} data-tone={tone}><Icon strokeWidth={1.7} /></span>;
}
