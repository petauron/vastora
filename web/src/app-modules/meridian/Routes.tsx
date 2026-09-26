import { useEffect, useId, useState } from "react";
import { Trash2Icon } from "lucide-react";
import { api } from "@/api";
import type { AppData, Mutate } from "@/App";
import type { LandingView } from "@/landing-types";
import type { Language } from "@/translations";
import { SelectControl } from "@/components/SelectControl";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { StateBadge, copy, userError } from "@/views/shared";

export function MeridianRoutes({ data, language, mutate, endpointId: fixedEntry, egressNodeId: fixedExit, landingServers }: {
  data: AppData; language: Language; mutate: Mutate; endpointId?: string; egressNodeId?: string; landingServers?: LandingView["servers"];
}) {
  const id = useId();
  const accounts = data.meridian.accounts.filter((account) => account.enabled);
  const [account, setAccount] = useState(accounts.length === 1 ? accounts[0].id : "");
  const [entry, setEntry] = useState(fixedEntry ?? "");
  const [exit, setExit] = useState(fixedExit ?? "");
  const [hideNative, setHideNative] = useState(false);
  const [servers, setServers] = useState<LandingView["servers"] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  useEffect(() => {
    if (landingServers) return;
    let active = true;
    setServers(null);
    setError("");
    void api.landing().then((value) => { if (active) setServers(value.servers); }).catch((cause) => { if (active) setError(userError(language, cause)); });
    return () => { active = false; };
  }, [reload, language, landingServers]);
  const availableServers = landingServers ?? servers;
  const entries = data.meridian.endpoints.filter((value) => value.vless && value.status === "ready" && value.nodeId !== exit);
  const exits = availableServers?.filter((value) => value.status === "ready" && value.nodeId !== data.meridian.endpoints.find((item) => item.id === entry)?.nodeId) ?? [];
  const grants = data.meridian.grants.filter((grant) => (!fixedEntry || grant.endpointId === fixedEntry) && (!fixedExit || grant.egressNodeId === fixedExit));
  const exists = grants.some((grant) => grant.accountId === account && grant.endpointId === entry && grant.egressNodeId === exit);
  const managementReady = data.meridian.cutover.subscriptionAuthority === "meridian" && ["not_required", "complete"].includes(data.meridian.cutover.state);
  const ready = managementReady && accounts.some((value) => value.id === account) && entries.some((value) => value.id === entry) && exits.some((value) => value.nodeId === exit);
  const run = async (operation: () => Promise<unknown>, message: string) => {
    if (busy) return;
    setBusy(true); setError("");
    try { await mutate(operation, message); } catch (cause) { setError(userError(language, cause)); } finally { setBusy(false); }
  };
  return <section className="flex min-w-0 flex-col gap-3 text-xs" aria-label={copy(language, "订阅线路", "Subscription routes")}>
    <h4 className="font-medium">{copy(language, "加入订阅", "Add to subscription")}</h4>
    <form onSubmit={(event) => { event.preventDefault(); if (ready && !exists) void run(() => api.createMeridianRoute({ accountId: account, endpointId: entry, egressNodeId: exit, hideNative }), copy(language, "线路已添加，正在配置。", "Route added; configuring.")); }}>
      <div className="flex flex-wrap items-end gap-2">
        <label className="flex min-w-36 flex-1 flex-col gap-1.5"><span>{copy(language, "账号", "Account")}</span><SelectControl aria-label={copy(language, "订阅账号", "Subscription account")} value={account} onValueChange={setAccount} disabled={busy} options={[{ value: "", label: copy(language, "选择账号", "Choose account"), disabled: true }, ...accounts.map((value) => ({ value: value.id, label: value.displayName }))]} /></label>
        {!fixedEntry ? <label className="flex min-w-36 flex-1 flex-col gap-1.5"><span>{copy(language, "线路机", "Entry node")}</span><SelectControl aria-label={copy(language, "线路机", "Entry node")} value={entry} onValueChange={setEntry} disabled={busy} options={[{ value: "", label: copy(language, "选择线路机", "Choose entry"), disabled: true }, ...entries.map((value) => ({ value: value.id, label: value.displayName || data.agents.find((agent) => agent.id === value.nodeId)?.name || value.applicationName }))]} /></label> : null}
        {!fixedExit ? <label className="flex min-w-36 flex-1 flex-col gap-1.5"><span>{copy(language, "落地机", "Exit node")}</span><SelectControl aria-label={copy(language, "落地机", "Exit node")} value={exit} onValueChange={setExit} disabled={busy || !availableServers} options={[{ value: "", label: copy(language, "选择落地机", "Choose exit"), disabled: true }, ...exits.map((value) => ({ value: value.nodeId, label: value.name }))]} /></label> : null}
        <Button size="sm" type="submit" disabled={busy || !ready || exists}>{busy ? copy(language, "正在保存…", "Saving…") : exists ? copy(language, "已添加", "Added") : copy(language, "加入订阅", "Add to subscription")}</Button>
      </div>
      {!accounts.length ? <p className="mt-2 text-muted-foreground">{copy(language, "请先在“账号与订阅”中创建账号。", "Create an account in Accounts & subscriptions first.")}</p> : null}
      <details className="mt-3 text-muted-foreground"><summary className="w-fit cursor-pointer">{copy(language, "高级选项", "Advanced")}</summary><div className="mt-2 flex items-center gap-2"><Switch id={`${id}-native`} checked={hideNative} onCheckedChange={setHideNative} disabled={busy} /><label htmlFor={`${id}-native`}>{copy(language, "隐藏直连 VLESS", "Hide direct VLESS")}</label></div></details>
    </form>
    {error ? <p role="alert" className="text-destructive">{error}{availableServers === null ? <Button size="sm" variant="ghost" onClick={() => setReload((value) => value + 1)}>{copy(language, "重试", "Retry")}</Button> : null}</p> : null}
    {grants.length ? <details className="border-t pt-2"><summary className="w-fit cursor-pointer text-muted-foreground">{copy(language, `已添加 ${grants.length} 条线路`, `${grants.length} added routes`)}</summary><div className="mt-2 divide-y">{grants.map((grant) => <div className="flex items-center gap-2 py-2" key={grant.id}><span className="min-w-0 flex-1 truncate">{data.meridian.accounts.find((value) => value.id === grant.accountId)?.displayName} · {data.meridian.endpoints.find((value) => value.id === grant.endpointId)?.displayName} → {grant.egressNodeName}</span><StateBadge language={language} value={grant.status} /><Button size="icon-sm" variant="ghost" aria-label={copy(language, "移除订阅线路", "Remove subscription route")} disabled={busy || grant.status === "revoking"} onClick={() => void run(() => api.revokeMeridianRoute(grant.id), copy(language, "正在移除线路。", "Removing route."))}><Trash2Icon /></Button>{grant.lastError ? <span className="text-destructive" title={grant.lastError}>{copy(language, "配置失败", "Failed")}</span> : null}</div>)}</div></details> : null}
  </section>;
}
