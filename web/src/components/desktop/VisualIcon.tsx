import { cn } from "@/lib/utils";

export type VisualIconName = "app-store" | "settings" | "users" | "home" | "nodes" | "info" | "network" | "activity" | "assistant" | "security" | "update" | "backup" | "diagnostics" | "catalog" | "meridian" | "senulun" | "probe" | "cpa" | "keeper" | "telegram" | "headscale" | "files" | "web-gateway" | "application";

/** Shared Color Planes assets; utility controls continue to use vector glyphs. */
export function VisualIcon({ name, className }: { name: VisualIconName; className?: string }) {
  return <span aria-hidden="true" data-icon={name} className={cn("vastora-visual-icon", className)}><img alt="" decoding="async" draggable={false} height={256} src={`/icons/flat-color/${name}.webp?v=9`} width={256} /></span>;
}
