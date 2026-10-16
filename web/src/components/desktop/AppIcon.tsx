import { VisualIcon, type VisualIconName } from "./VisualIcon";
import { cn } from "@/lib/utils";

const icons: Record<string, VisualIconName> = {
  home: "home", apps: "app-store", nodes: "nodes", settings: "settings",
  updates: "update", backup: "backup", catalog: "catalog",
  network: "network", overview: "info", activity: "activity", assistant: "assistant",
  "vastora-official/meridian": "meridian",
  "vastora-official/pulse": "senulun",
  "vastora-official/pulse-agent": "probe",
  "vastora-official/telegram-bot": "telegram",
  "vastora-official/headscale": "headscale",
  "vastora-official/cpa": "cpa",
  "vastora-official/keeper": "keeper",
  "vastora-official/filebrowser": "files",
  "vastora-official/3x-ui": "web-gateway",
};

export function AppIcon({ appKey, className, variant = "tile" }: { appKey: string; className?: string; variant?: "tile" | "glyph" }) {
  return <VisualIcon className={cn("desktop-app-icon size-14", variant === "tile" ? "desktop-asset-icon" : "desktop-feature-icon", className)} name={icons[appKey] ?? "application"} />;
}
