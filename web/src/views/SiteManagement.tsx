import { useEffect, useRef, useState, type ComponentProps, type FormEvent, type RefObject } from "react";
import { MapPinIcon, PencilIcon, PlusIcon } from "lucide-react";
import { api } from "../api";
import { browserTimezone } from "../lib/network";
import type { AppData, Mutate } from "../App";
import type { AgentView, Site, SiteInput } from "../types";
import type { Language } from "../translations";
import { copy, userError } from "./shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { TimezoneCombobox } from "@/components/TimezoneCombobox";

function siteNamespaceLocked(data: AppData, site: Site | null) {
  if (!site) return false;
  const serviceIDs = new Set(data.services.filter((service) => service.siteId === site.id).map((service) => service.id));
  return data.publications.some((publication) => publication.status !== "stopped" && serviceIDs.has(publication.serviceId));
}

async function saveSite(language: Language, mutate: Mutate, site: Site | null, input: SiteInput) {
  await mutate(
    () => (site ? api.updateSite(site, input) : api.createSite(input)),
    copy(language, site ? "位置信息已保存。" : "位置已创建。", site ? "Location saved." : "Location created."),
  );
}

export function LocationsCard({ data, language, mutate }: { data: AppData; language: Language; mutate: Mutate }) {
  const [editingSite, setEditingSite] = useState<Site | null>(null);
  const [siteEditorOpen, setSiteEditorOpen] = useState(false);
  const activeAgents = data.agents.filter((agent) => agent.status === "active");
  const namespaceLocked = siteNamespaceLocked(data, editingSite);

  return <>
    <Card size="sm">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <MapPinIcon aria-hidden="true" />
          {copy(language, "位置管理", "Locations")}
          <Badge variant="secondary">{data.sites.length}</Badge>
        </CardTitle>
        <CardDescription>{copy(language, "按家庭、办公室或机房分组管理节点。", "Group nodes by home, office, or data center.")}</CardDescription>
        <CardAction>
          <Button onClick={() => { setEditingSite(null); setSiteEditorOpen(true); }} size="sm" variant="outline">
            <PlusIcon data-icon="inline-start" />{copy(language, "新建位置", "New location")}
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent className="flex flex-col">
        {data.sites.length > 0 ? data.sites.map((site) => (
          <div className="flex min-w-0 items-center gap-3 border-b py-2.5 last:border-b-0" key={site.id}>
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium">{site.name}</p>
              <p className="text-xs text-muted-foreground">
                {copy(language, `${activeAgents.filter((agent) => agent.siteId === site.id).length} 台节点`, `${activeAgents.filter((agent) => agent.siteId === site.id).length} nodes`)}
                {site.domainSuffix ? ` · ${site.domainSuffix}` : ""}
              </p>
            </div>
            <Button aria-label={copy(language, `编辑位置 ${site.name}`, `Edit location ${site.name}`)} onClick={() => { setEditingSite(site); setSiteEditorOpen(true); }} size="sm" variant="ghost">
              <PencilIcon data-icon="inline-start" />{copy(language, "编辑", "Edit")}
            </Button>
          </div>
        )) : <p className="py-2 text-sm text-muted-foreground">{copy(language, "尚未创建位置。", "No locations created yet.")}</p>}
      </CardContent>
    </Card>
    <SiteEditor
      agents={activeAgents}
      language={language}
      namespaceLocked={namespaceLocked}
      open={siteEditorOpen}
      site={editingSite}
      onClose={() => setSiteEditorOpen(false)}
      onSave={async (site, input) => {
        await saveSite(language, mutate, site, input);
        setSiteEditorOpen(false);
      }}
    />
  </>;
}

export function LocationsSheet({ data, language, mutate, open, onClose, returnFocusTo }: { data: AppData; language: Language; mutate: Mutate; open: boolean; onClose: () => void; returnFocusTo?: RefObject<HTMLElement | null> }) {
  const handingOffToEditor = useRef(false);
  const [editingSite, setEditingSite] = useState<Site | null>(null);
  const [siteEditorOpen, setSiteEditorOpen] = useState(false);
  const activeAgents = data.agents.filter((agent) => agent.status === "active");
  const namespaceLocked = siteNamespaceLocked(data, editingSite);
  useEffect(() => {
    if (open) handingOffToEditor.current = false;
  }, [open]);

  const openEditor = (site: Site | null) => {
    handingOffToEditor.current = true;
    onClose();
    setEditingSite(site);
    setSiteEditorOpen(true);
  };

  return <>
    <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={open}>
      <SheetContent className="sm:max-w-lg" finalFocus={() => !handingOffToEditor.current}>
        <SheetHeader>
          <SheetTitle>{copy(language, "位置管理", "Locations")}</SheetTitle>
          <SheetDescription>{copy(language, "按家庭、办公室或机房分组管理节点与服务域名空间。", "Group nodes and service domain namespaces by home, office, or data center.")}</SheetDescription>
        </SheetHeader>
        <SheetBody>
          {data.sites.length > 0 ? (
            <div className="divide-y rounded-xl border">
              {data.sites.map((site) => (
                <div className="flex min-w-0 items-center gap-3 px-4 py-3" key={site.id}>
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium">{site.name}</p>
                    <p className="mt-0.5 truncate text-xs text-muted-foreground">
                      {copy(language, `${activeAgents.filter((agent) => agent.siteId === site.id).length} 台节点`, `${activeAgents.filter((agent) => agent.siteId === site.id).length} nodes`)}
                      {site.domainSuffix ? ` · ${site.domainSuffix}` : ""}
                    </p>
                  </div>
                  <Button aria-label={copy(language, `编辑位置 ${site.name}`, `Edit location ${site.name}`)} onClick={() => openEditor(site)} size="sm" variant="ghost">
                    <PencilIcon data-icon="inline-start" />{copy(language, "编辑", "Edit")}
                  </Button>
                </div>
              ))}
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">{copy(language, "尚未创建位置。", "No locations created yet.")}</p>
          )}
        </SheetBody>
        <SheetFooter>
          <Button onClick={onClose} type="button" variant="outline">{copy(language, "关闭", "Close")}</Button>
          <Button onClick={() => openEditor(null)} type="button"><PlusIcon data-icon="inline-start" />{copy(language, "新建位置", "New location")}</Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
    <SiteEditor
      agents={activeAgents}
      finalFocus={returnFocusTo}
      language={language}
      namespaceLocked={namespaceLocked}
      open={siteEditorOpen}
      site={editingSite}
      onClose={() => setSiteEditorOpen(false)}
      onSave={async (site, input) => {
        await saveSite(language, mutate, site, input);
        setSiteEditorOpen(false);
      }}
    />
  </>;
}

export function SiteEditor({ agents, finalFocus, language, namespaceLocked, open, site, onClose, onSave }: { agents: AgentView[]; finalFocus?: ComponentProps<typeof SheetContent>["finalFocus"]; language: Language; namespaceLocked: boolean; open: boolean; site: Site | null; onClose: () => void; onSave: (site: Site | null, input: SiteInput) => Promise<void> }) {
  const [name, setName] = useState("");
  const [code, setCode] = useState("");
  const [description, setDescription] = useState("");
  const [timezone, setTimezone] = useState(browserTimezone);
  const [domainSuffix, setDomainSuffix] = useState("");
  const [gatewayNodes, setGatewayNodes] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    setName(site?.name ?? ""); setCode(site?.code ?? `site-${crypto.randomUUID().slice(0, 8)}`); setDescription(site?.description ?? ""); setTimezone(site?.timezone ?? browserTimezone()); setDomainSuffix(site?.domainSuffix ?? ""); setGatewayNodes(site?.gatewayNodes ?? []); setError("");
  }, [open, site]);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true); setError("");
    try { await onSave(site, { name, code, description, timezone, domainSuffix, gatewayNodes }); } catch (submitError) { setError(userError(language, submitError)); } finally { setBusy(false); }
  };
  const candidates = agents.filter((agent) => agent.siteId === site?.id && agent.capabilities.gateway);
  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={open}><SheetContent className="sm:max-w-lg" finalFocus={finalFocus}><SheetHeader><SheetTitle>{site ? copy(language, "编辑位置", "Edit location") : copy(language, "新建位置", "New location")}</SheetTitle><SheetDescription>{copy(language, "位置通常对应一个家庭、办公室或数据中心。", "A location usually represents a home, office, or data center.")}</SheetDescription></SheetHeader><form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}><SheetBody><FieldGroup><Field data-invalid={Boolean(error)}><FieldLabel htmlFor="site-name">{copy(language, "名称", "Name")}</FieldLabel><Input aria-invalid={Boolean(error)} autoFocus id="site-name" onChange={(event) => setName(event.target.value)} placeholder={copy(language, "例如：家里", "For example: Home")} required value={name} /></Field><Field><FieldLabel htmlFor="site-description">{copy(language, "说明", "Description")}</FieldLabel><Input id="site-description" onChange={(event) => setDescription(event.target.value)} value={description} /></Field><Field><FieldLabel htmlFor="site-timezone">{copy(language, "时区", "Time zone")}</FieldLabel><TimezoneCombobox id="site-timezone" language={language} onValueChange={setTimezone} value={timezone} /><FieldDescription>{copy(language, "新位置默认使用当前浏览器时区，也可以搜索并选择。", "New locations default to this browser's time zone and can be searched and changed.")}</FieldDescription></Field><Field><FieldLabel htmlFor="site-domain">{copy(language, "服务域名空间", "Service domain namespace")}</FieldLabel><Input autoCapitalize="none" autoCorrect="off" disabled={namespaceLocked} id="site-domain" onChange={(event) => setDomainSuffix(event.target.value.toLowerCase())} placeholder="vastora.example.com" spellCheck={false} value={domainSuffix} /><FieldDescription>{namespaceLocked ? copy(language, "已有访问入口正在使用此域名空间；停止这些入口后才能修改。", "Active access points use this namespace. Stop them before changing it.") : copy(language, `服务会按“服务.应用.${code || "位置"}.${domainSuffix || "vastora.example.com"}”分层命名。`, `Services use the hierarchy “service.app.${code || "location"}.${domainSuffix || "vastora.example.com"}”.`)}</FieldDescription></Field>{site ? <FieldSet><FieldLegend>{copy(language, "网关节点", "Gateway nodes")}</FieldLegend><FieldDescription>{copy(language, "网关负责把此位置内的 Web 服务变成容易访问的地址。第一个具备网关能力并完成网络确认的节点会自动选中。", "Gateways provide friendly addresses for Web services in this location. The first gateway-capable node is selected automatically after network confirmation.")}</FieldDescription><FieldGroup>{candidates.map((agent) => <Field key={agent.id} orientation="horizontal"><FieldLabel htmlFor={`gateway-${agent.id}`}><span>{agent.name}</span><span className="text-xs font-normal text-muted-foreground">{agent.networkProfile?.serviceAddress || copy(language, "网络未确认", "Network not confirmed")}</span></FieldLabel><Switch checked={gatewayNodes.includes(agent.id)} id={`gateway-${agent.id}`} onCheckedChange={(checked) => setGatewayNodes((current) => checked ? [...new Set([...current, agent.id])] : current.filter((id) => id !== agent.id))} /></Field>)}</FieldGroup></FieldSet> : null}<details className="rounded-xl border p-3"><summary className="cursor-pointer text-sm font-medium">{copy(language, "高级设置", "Advanced settings")}</summary><Field className="mt-4"><FieldLabel htmlFor="site-code">{copy(language, "域名中的位置标识", "Location label in domains")}</FieldLabel><Input disabled={namespaceLocked} id="site-code" onChange={(event) => setCode(event.target.value.toLowerCase())} pattern="[a-z][a-z0-9-]{0,31}" required value={code} /><FieldDescription>{namespaceLocked ? copy(language, "此标识已经用于现有入口，暂时不能修改。", "This label is already used by active access points.") : copy(language, "会出现在此位置的服务域名中；创建入口前可以修改。", "Appears in this location's service hostnames and can be changed before access points are created.")}</FieldDescription></Field></details>{error ? <FieldError>{error}</FieldError> : null}</FieldGroup></SheetBody><SheetFooter><Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button><Button disabled={busy || !name || !timezone} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{site ? copy(language, "保存", "Save") : copy(language, "创建位置", "Create location")}</Button></SheetFooter></form></SheetContent></Sheet>;
}
