import type { Language } from "@/translations";
import { regionFlag, regionName } from "@/lib/regions";
import { copy } from "@/views/shared";

export function NodeLocation({ regionCode, siteName, language }: { regionCode?: string; siteName?: string; language: Language }) {
  const code = (regionCode || "").trim().toUpperCase();
  const country = regionFlag(code) ? regionName(code, [language]) : copy(language, "位置待识别", "Location unknown");
  // A report's city may come from a different database than its country.
  // Use the configured site for the node's location instead of joining them.
  return <span title={copy(language, "国家 / 地区与站点", "Country / region and site")}>{country}{siteName && siteName !== country ? ` · ${siteName}` : ""}</span>;
}
