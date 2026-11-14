import { useEffect, useState, type FormEvent } from "react";
import { api } from "../api";
import type { ManagedDomain } from "../managed-domain-types";
import type { Application, AgentView } from "../types";
import type { Language } from "../translations";
import { SelectControl } from "@/components/SelectControl";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { copy, userError } from "./shared";

export function ManagedDomainSettings({ applications, agents, language }: { applications: Application[]; agents: AgentView[]; language: Language }) {
  const [appId, setAppId] = useState("");
  const [key, setKey] = useState("ingress");
  const [prefix, setPrefix] = useState("");
  const [hostname, setHostname] = useState("");
  const [address, setAddress] = useState("");
  const [secondaryAddress, setSecondaryAddress] = useState("");
  const [previousAddress, setPreviousAddress] = useState("");
  const [migrateFamily, setMigrateFamily] = useState(false);
  const [domains, setDomains] = useState<ManagedDomain[]>([]);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  useEffect(() => {
    if (!appId) return;
    const abort = new AbortController();
    api.managedDomains(appId, abort.signal).then(result => {
      if (!abort.signal.aborted) setDomains(result.domains);
    }).catch((reason: unknown) => {
      if (!abort.signal.aborted) setError(userError(language, reason));
    }).finally(() => { if (!abort.signal.aborted) setLoading(false); });
    return () => abort.abort();
  }, [appId, language]);
  const chooseApplication = (id: string) => {
    setMigrateFamily(false); setAppId(id); setDomains([]); setHostname(""); setAddress(""); setSecondaryAddress(""); setPreviousAddress(""); setKey("ingress"); setPrefix(""); setError(""); setMessage(""); setLoading(true);
  };
  const chooseDomain = (domain: ManagedDomain) => {
    setMigrateFamily(false); setKey(domain.key); setPrefix(domain.prefix ?? ""); setHostname(domain.hostname); setAddress(domain.address); setSecondaryAddress(domain.secondaryAddress ?? ""); setPreviousAddress(""); setError(""); setMessage("");
  };
  const submit = async (event: FormEvent) => {
    event.preventDefault(); setBusy(true); setError(""); setMessage("");
    try {
      const current = domains.find(domain => domain.key === key);
      const value = await api.ensureManagedDomain({ applicationId: appId, key, prefix, hostname, address, secondaryAddress: secondaryAddress !== (current?.secondaryAddress ?? "") ? secondaryAddress : undefined, expectedRevision: current?.revision ?? 0, expectedRecordAddress: previousAddress || undefined, migrateAddressFamily: migrateFamily || undefined });
      setDomains(values => [...values.filter(domain => domain.id !== value.id), value]);
      chooseDomain(value);
      setMessage(copy(language, "域名已保存。请确认公网解析和应用访问后再切换连接参数。", "Domain saved. Verify public DNS and application access before changing connection settings."));
    } catch (reason) { setError(userError(language, reason)); }
    finally { setBusy(false); }
  };
  const current = domains.find(domain => domain.key === key);
  const familyChanges = Boolean(current && address.trim() && (address.includes(":") ? "AAAA" : "A") !== current.type);
  return <Card>
    <CardHeader><CardTitle>{copy(language, "应用受管域名", "Managed application domains")}</CardTitle><CardDescription>{copy(language, "复用原接入域名，或分配固定随机名称；地区前缀用于独立网站。保存域名不会切换应用或订阅。", "Reuse an original ingress domain or allocate a stable random name. A regional prefix identifies a separate website. Saving DNS does not switch an application or subscription.")}</CardDescription></CardHeader>
    <CardContent className="flex flex-col gap-4">
      <Field><FieldLabel htmlFor="managed-domain-app">{copy(language, "应用与主机", "Application and host")}</FieldLabel><SelectControl id="managed-domain-app" disabled={busy} value={appId} onValueChange={chooseApplication} placeholder={copy(language, "选择正在运行的应用", "Choose a running application")} options={applications.filter(app => app.status === "running").map(app => ({ value: app.id, label: `${agents.find(node => node.id === app.nodeId)?.name ?? app.nodeId} · ${app.name}` }))} /></Field>
      {appId ? <>
        {loading ? <p role="status"><Spinner />{copy(language, "正在读取域名…", "Loading domains…")}</p> : null}
        {domains.map(domain => <Button type="button" variant="outline" className="justify-start overflow-hidden" disabled={busy} key={domain.id} onClick={() => chooseDomain(domain)}><span className="truncate">{domain.key} · {domain.hostname} · {domain.type} {domain.address}{domain.secondaryAddress ? ` + ${domain.secondaryAddress.includes(":") ? "AAAA" : "A"} ${domain.secondaryAddress}` : ""} · {domain.status}</span></Button>)}
        <form onSubmit={event => void submit(event)}>
          <FieldGroup>
            <Field><FieldLabel htmlFor="managed-domain-key">{copy(language, "用途标识", "Purpose key")}</FieldLabel><Input id="managed-domain-key" value={key} onChange={e => { setKey(e.target.value); setMigrateFamily(false); }} required disabled={busy || loading} pattern="[a-z][a-z0-9_.:-]{0,63}" /><FieldDescription>{copy(language, "同一应用和用途始终复用同一个域名，例如 ingress 或 site。", "The same application and purpose always reuse one domain, for example ingress or site.")}</FieldDescription></Field>
            <Field><FieldLabel htmlFor="managed-domain-prefix">{copy(language, "地区前缀（可选）", "Region prefix (optional)")}</FieldLabel><Input id="managed-domain-prefix" value={prefix} onChange={e => { setPrefix(e.target.value); setMigrateFamily(false); }} disabled={busy || loading} pattern="[a-z]{2,8}" placeholder="us / hk / jp / sg" /><FieldDescription>{copy(language, "接入域名留空；独立网站填写地区，分配后保持不变。", "Leave empty for ingress; use a region for a website. The allocated name remains stable.")}</FieldDescription></Field>
            <Field><FieldLabel htmlFor="managed-domain-host">{copy(language, "复用原域名（可选）", "Original domain (optional)")}</FieldLabel><Input id="managed-domain-host" value={hostname} onChange={e => { setHostname(e.target.value); setMigrateFamily(false); }} disabled={busy || loading} /><FieldDescription>{copy(language, "留空时优先复用本应用原有随机域名；没有时自动分配。", "When empty, prefer this application's retained random domain; otherwise allocate a new one.")}</FieldDescription></Field>
            <Field><FieldLabel htmlFor="managed-domain-address">{copy(language, "公网接入 IP", "Public ingress IP")}</FieldLabel><Input id="managed-domain-address" value={address} onChange={e => { setAddress(e.target.value); setMigrateFamily(false); }} required disabled={busy || loading} /><FieldDescription>{copy(language, "必须是本机已确认的地址；IPv6 接入保留 AAAA。", "Must be a confirmed address of this host; IPv6 ingress retains AAAA.")}</FieldDescription></Field>
            <Field><FieldLabel htmlFor="managed-domain-secondary">{copy(language, "另一地址族的公网 IP（双栈可选）", "Other-family public IP (optional dual stack)")}</FieldLabel><Input id="managed-domain-secondary" value={secondaryAddress} onChange={e => { setSecondaryAddress(e.target.value); setMigrateFamily(false); }} disabled={busy || loading} /><FieldDescription>{copy(language, "填写同机另一地址族的已确认地址，保留原记录并增加 A/AAAA。清空会撤回第二条受管记录；保存 DNS 不会开启端口监听。", "Use a confirmed address of the other family on this same host. Clearing removes the secondary managed record. Saving DNS does not start a listener.")}</FieldDescription></Field>
            <details><summary>{copy(language, "主机迁移：核对原记录地址", "Host migration: verify the prior record address")}</summary><Field><FieldLabel htmlFor="managed-domain-previous">{copy(language, "已核实的原 IP", "Verified previous IP")}</FieldLabel><Input id="managed-domain-previous" value={previousAddress} onChange={e => { setPreviousAddress(e.target.value); setMigrateFamily(false); }} disabled={busy || loading} /></Field></details>
            {familyChanges ? <Field orientation="horizontal"><Checkbox id="managed-domain-family" checked={migrateFamily} disabled={busy || loading} onCheckedChange={value => setMigrateFamily(value === true)} /><FieldLabel htmlFor="managed-domain-family">{copy(language, "已核对原 IP 和新监听，确认迁移此域名的 IPv4/IPv6 地址族", "I verified the previous IP and new listener and confirm this domain's IPv4/IPv6 migration")}</FieldLabel><FieldDescription>{copy(language, "请在上方高级选项填写已核实的原 IP。仅更新这条受管 DNS，不会自动更改代理入口、SNI、订阅或服务监听。", "Enter the verified previous IP above. Only this managed DNS record changes; proxy ingress, SNI, subscriptions and service listeners remain unchanged.")}</FieldDescription></Field> : null}
            {error ? <FieldError role="alert">{error}</FieldError> : null}
            {message ? <p role="status">{message}</p> : null}
            <Button type="submit" disabled={busy || loading || familyChanges && (!migrateFamily || previousAddress.trim() !== current?.address)}>{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "保存并复用域名", "Save and reuse domain")}</Button>
          </FieldGroup>
        </form>
      </> : null}
    </CardContent>
  </Card>;
}
