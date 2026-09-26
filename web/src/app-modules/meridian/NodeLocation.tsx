import type { Language } from "@/translations";
import { regionFlag, regionName } from "@/lib/regions";
import { useIPQuality } from "@/views/IPQuality";
import { cleanIPQualityValue } from "@/views/ipQualityModel";
import { copy } from "@/views/shared";

export function NodeLocation({ nodeId, regionCode, language }: { nodeId: string; regionCode?: string; language: Language }) {
  const quality = useIPQuality();
  const agent = quality?.agents.find((item) => item.id === nodeId);
  const address = agent?.publicEgress?.address || agent?.networkProfile?.publicAddress;
  const check = quality?.checks.find((item) => item.agentId === nodeId);
  const report = !quality?.error && check?.state === "succeeded" && !check.stale && !check.error && check.report?.address === address ? check.report : undefined;
  const code = (regionCode || report?.regionCode || "").trim().toUpperCase();
  const country = regionFlag(code) ? regionName(code, [language]) : copy(language, "位置待识别", "Location unknown");
  const city = report?.regionCode?.toUpperCase() === code ? cleanIPQualityValue(report.city) : "";
  return <span title={copy(language, "节点 IP 归属地", "Node IP location")}>{country}{city && city !== country ? ` · ${city}` : ""}</span>;
}
