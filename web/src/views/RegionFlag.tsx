import { regionFlag, regionName } from "@/lib/regions";
import type { Language } from "../translations";

export function RegionFlag({ code, language }: { code?: string; language: Language }) {
  const normalized = code?.trim().toUpperCase() ?? "";
  const flag = regionFlag(normalized);
  if (!flag) return null;
  const label = regionName(normalized, [language]);
  return <span className="shrink-0" role="img" aria-label={label} title={label}>{flag}</span>;
}
