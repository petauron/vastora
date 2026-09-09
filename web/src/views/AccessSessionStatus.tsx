import type { AccessSessionSync } from "../types";
import type { Language } from "../translations";
import { copy } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";

export function AccessSessionStatus({ sync, language }: { sync?: AccessSessionSync; language: Language }) {
  if (!sync || sync.status === "not_synced") return null;
  const incomplete = sync.status !== "synced";
  const overrides = sync.policyOverrideHosts ?? [];
  return <Alert role={incomplete ? "alert" : "status"} variant={incomplete ? "destructive" : "default"}>
    <AlertTitle>{incomplete ? copy(language, "会话时长已保存，部分入口尚未同步", "Session duration saved; some entries are not synchronized") : copy(language, "会话时长已同步", "Session duration synchronized")}</AlertTitle>
    <AlertDescription>
      <p>{copy(language, `上次保存：${sync.updated}/${sync.total} 个入口已同步。`, `Last save: ${sync.updated}/${sync.total} entries synchronized.`)}{incomplete ? copy(language, "请保存重试；原有访问保护仍保留。", "Save again to retry. Existing access protection is retained.") : null}</p>
      {sync.failedHosts?.length ? <details><summary className="cursor-pointer">{copy(language, "查看未完成的入口", "View unfinished entries")}</summary><ul className="mt-2 list-inside list-disc break-all">{sync.failedHosts.map((host) => <li key={host}>{host}</li>)}</ul></details> : null}
      {overrides.length ? <p className="break-words">{copy(language, `以下入口有单独的策略时长，未被覆盖：${overrides.join("、")}。若希望统一时长，请在 Cloudflare 中将对应策略设为“与应用相同”。`, `These entries have separate policy durations, which were preserved: ${overrides.join(", ")}. To use the application duration, set those policies to “Same as application” in Cloudflare.`)}</p> : null}
    </AlertDescription>
  </Alert>;
}
