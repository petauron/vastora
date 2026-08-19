import { useState, type FormEvent } from "react";
import { CheckIcon, Globe2Icon, HouseIcon, LanguagesIcon, MapPinIcon, NetworkIcon, ShieldCheckIcon, type LucideIcon } from "lucide-react";
import { api } from "../api";
import type { AgentConnectionMode, CloudflareZone, InitialSetupInput, NetworkCandidate } from "../types";
import type { Language } from "../translations";
import { browserTimezone, validCenterURL } from "../lib/network";
import { Brand, copy } from "./shared";
import { CloudflareOAuthConnect } from "./CloudflareOAuthConnect";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Spinner } from "@/components/ui/spinner";

type SetupWizardProps = {
  language: Language;
  suggestedAgentConnectUrl: string;
  builtinHeadscaleAvailable: boolean;
  cloudflareOAuthAvailable: boolean;
  cloudflareConfigured: boolean;
  cloudflareZone?: string;
  publicAddressCandidates: NetworkCandidate[];
  onLanguage: (language: Language) => void;
  onComplete: (input: InitialSetupInput) => Promise<void>;
};

const connectionOptions: Array<{ mode: AgentConnectionMode; icon: LucideIcon; zh: string; en: string; descriptionZh: string; descriptionEn: string }> = [
  { mode: "lan", icon: HouseIcon, zh: "局域网", en: "Local network", descriptionZh: "Center 和节点在同一家庭、办公室或数据中心网络。", descriptionEn: "Center and nodes share a home, office, or data-center network." },
  { mode: "headscale", icon: ShieldCheckIcon, zh: "Headscale 私网", en: "Headscale private network", descriptionZh: "适合跨地区节点；节点先加入私网，再连接 Center。", descriptionEn: "Best for nodes across locations. Nodes join the private network before Center." },
  { mode: "public", icon: Globe2Icon, zh: "公网 HTTPS", en: "Public HTTPS", descriptionZh: "Center 有稳定域名和 HTTPS，可被各节点直接访问。", descriptionEn: "Center has a stable HTTPS hostname reachable by every node." }
];

export function SetupWizard(props: SetupWizardProps) {
  const { language, suggestedAgentConnectUrl, builtinHeadscaleAvailable, cloudflareOAuthAvailable, publicAddressCandidates, onLanguage, onComplete } = props;
  const [step, setStep] = useState(1);
  const [name, setName] = useState("");
  const [timezone, setTimezone] = useState(browserTimezone);
  const [domainSuffix, setDomainSuffix] = useState("");
  const [code] = useState(() => `site-${crypto.randomUUID().slice(0, 8)}`);
  const [mode, setMode] = useState<AgentConnectionMode>("lan");
  const [agentConnectUrl, setAgentConnectUrl] = useState(suggestedAgentConnectUrl);
  const [headscaleMode, setHeadscaleMode] = useState<"builtin" | "external">("builtin");
  const [headscaleUrl, setHeadscaleUrl] = useState("");
  const [headscaleApiKey, setHeadscaleApiKey] = useState("");
  const [dnsMode, setDNSMode] = useState<"cloudflare" | "manual">(cloudflareOAuthAvailable && publicAddressCandidates.length > 0 ? "cloudflare" : "manual");
  const [cloudflareConfigured, setCloudflareConfigured] = useState(props.cloudflareConfigured);
  const [cloudflareZone, setCloudflareZone] = useState(props.cloudflareZone ?? "");
  const [publicAddress, setPublicAddress] = useState(publicAddressCandidates[0]?.address ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const nextLocation = (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); setError(""); setStep(2); };
  const nextNetwork = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault(); setError("");
    if (!validCenterURL(agentConnectUrl)) { setError(copy(language, "Agent 连接地址必须是没有路径的 HTTPS URL；只有本机回环地址可以使用 HTTP。", "The Agent connection address must be an HTTPS URL without a path. Only loopback may use HTTP.")); return; }
    if (mode === "headscale") {
      if (!validHeadscaleURL(headscaleUrl)) { setError(copy(language, "Headscale 地址必须是没有路径的 HTTPS URL。", "The Headscale address must be an HTTPS URL without a path.")); return; }
      if (headscaleMode === "builtin" && (!builtinHeadscaleAvailable || new URL(headscaleUrl).port !== "8443" || new URL(agentConnectUrl).port !== "8443")) { setError(copy(language, "内置 Headscale 需要当前安装包的部署助手，并固定使用 8443 端口。", "Built-in Headscale needs the bundled deployment helper and must use port 8443.")); return; }
      if (headscaleMode === "external" && headscaleApiKey.trim().length < 20) { setError(copy(language, "已有 Headscale 需要有效的 API Key。", "An existing Headscale server requires a valid API key.")); return; }
      if (headscaleMode === "builtin" && dnsMode === "cloudflare" && (!cloudflareConfigured || !publicAddress)) { setError(copy(language, "请先登录 Cloudflare 并选择 Center 的公网地址。", "Sign in to Cloudflare and select the Center public address first.")); return; }
    }
    setStep(3);
  };
  const finish = async () => {
    setBusy(true); setError("");
    const input: InitialSetupInput = { site: { name, code, description: "", timezone, domainSuffix, gatewayNodes: [] }, network: { agentConnectionMode: mode, agentConnectUrl: agentConnectUrl.replace(/\/$/, "") } };
    if (mode === "headscale") input.headscale = { mode: headscaleMode, url: headscaleUrl.replace(/\/$/, ""), ...(headscaleMode === "external" ? { apiKey: headscaleApiKey } : {}) };
    try {
      if (mode === "headscale" && headscaleMode === "builtin" && dnsMode === "cloudflare") await api.configureSetupDNS({ centerUrl: agentConnectUrl.replace(/\/$/, ""), headscaleUrl: headscaleUrl.replace(/\/$/, ""), publicAddress });
      await onComplete(input);
    } catch (submitError) { setError(submitError instanceof Error ? submitError.message : "Request failed"); } finally { setBusy(false); }
  };
  const connectedCloudflare = (zone: CloudflareZone) => { setCloudflareConfigured(true); setCloudflareZone(zone.name); if (!domainSuffix) setDomainSuffix(zone.name); };
  const selected = connectionOptions.find((option) => option.mode === mode)!;

  return <main className="min-h-svh bg-muted/35 p-5 md:p-8"><div className="mx-auto flex w-full max-w-2xl flex-col gap-6">
    <div className="flex items-center justify-between"><Brand /><Button aria-label={copy(language, "切换语言", "Change language")} onClick={() => onLanguage(language === "zh-CN" ? "en" : "zh-CN")} size="icon" variant="ghost"><LanguagesIcon /></Button></div>
    <SetupProgress language={language} step={step} />
    <Card>
      {step === 1 ? <LocationStep language={language} name={name} timezone={timezone} domainSuffix={domainSuffix} onName={setName} onTimezone={setTimezone} onDomainSuffix={setDomainSuffix} onSubmit={nextLocation} /> : null}
      {step === 2 ? <form onSubmit={nextNetwork}><CardHeader><CardTitle>{copy(language, "节点如何连接 Center？", "How will nodes reach Center?")}</CardTitle><CardDescription>{copy(language, "这是添加节点时的默认接入方式。以后仍可以同时启用局域网、Headscale 和公网能力。", "This is the default joining path. LAN, Headscale, and public capabilities can still be enabled together later.")}</CardDescription></CardHeader><CardContent><FieldGroup>
        {!suggestedAgentConnectUrl ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "安装向导通过 SSH 隧道打开", "Setup is open through an SSH tunnel")}</AlertTitle><AlertDescription>{copy(language, "下面填写 Agent 以后实际能访问的 Center 地址，不要填写本机浏览器中的 127.0.0.1:18082。", "Enter the Center address that Agents will actually reach, not the local browser's 127.0.0.1:18082 address.")}</AlertDescription></Alert> : null}
        <ConnectionMode language={language} mode={mode} onMode={setMode} />
        <Field><FieldLabel htmlFor="setup-center-url">{copy(language, "Agent 连接地址", "Agent connection address")}</FieldLabel><Input id="setup-center-url" onChange={(event) => setAgentConnectUrl(event.target.value)} placeholder="https://center.example.com:8443" required type="url" value={agentConnectUrl} /><FieldDescription>{suggestedAgentConnectUrl ? copy(language, "已使用安装 Center 时配置的地址。节点必须能访问它。", "Filled from the Center installation. Every node must be able to reach it.") : copy(language, "内置网关使用 8443，不占用应用的 443。", "The bundled gateway uses 8443 and leaves application port 443 free.")}</FieldDescription></Field>
        {mode === "headscale" ? <HeadscaleSetup language={language} builtinAvailable={builtinHeadscaleAvailable} headscaleMode={headscaleMode} headscaleUrl={headscaleUrl} headscaleApiKey={headscaleApiKey} dnsMode={dnsMode} cloudflareOAuthAvailable={cloudflareOAuthAvailable} cloudflareConfigured={cloudflareConfigured} cloudflareZone={cloudflareZone} publicAddress={publicAddress} publicAddressCandidates={publicAddressCandidates} onHeadscaleMode={setHeadscaleMode} onHeadscaleUrl={setHeadscaleUrl} onHeadscaleApiKey={setHeadscaleApiKey} onDNSMode={setDNSMode} onCloudflareConnected={connectedCloudflare} onPublicAddress={setPublicAddress} /> : null}
        {error ? <FieldError role="alert">{error}</FieldError> : null}
      </FieldGroup></CardContent><CardFooter className="justify-between"><Button onClick={() => { setError(""); setStep(1); }} type="button" variant="outline">{copy(language, "返回", "Back")}</Button><Button type="submit">{copy(language, "检查配置", "Review")}</Button></CardFooter></form> : null}
      {step === 3 ? <ReviewStep language={language} busy={busy} error={error} name={name} timezone={timezone} domainSuffix={domainSuffix} mode={mode} selected={selected} agentConnectUrl={agentConnectUrl} headscaleMode={headscaleMode} headscaleUrl={headscaleUrl} dnsMode={dnsMode} cloudflareZone={cloudflareZone} onBack={() => { setError(""); setStep(2); }} onFinish={finish} /> : null}
    </Card>
  </div></main>;
}

function LocationStep({ language, name, timezone, domainSuffix, onName, onTimezone, onDomainSuffix, onSubmit }: { language: Language; name: string; timezone: string; domainSuffix: string; onName: (value: string) => void; onTimezone: (value: string) => void; onDomainSuffix: (value: string) => void; onSubmit: (event: FormEvent<HTMLFormElement>) => void }) {
  return <form onSubmit={onSubmit}><CardHeader><CardTitle>{copy(language, "创建第一个位置", "Create your first location")}</CardTitle><CardDescription>{copy(language, "位置通常是一处家庭、办公室或数据中心，用来归类同一网络中的节点。", "A location is usually a home, office, or data center and groups nodes on the same network.")}</CardDescription></CardHeader><CardContent><FieldGroup><Field><FieldLabel htmlFor="setup-location-name">{copy(language, "位置名称", "Location name")}</FieldLabel><Input autoFocus id="setup-location-name" maxLength={128} onChange={(event) => onName(event.target.value)} placeholder={copy(language, "例如：新加坡机房", "For example: Singapore data center")} required value={name} /></Field><Field><FieldLabel htmlFor="setup-timezone">{copy(language, "时区", "Time zone")}</FieldLabel><Input id="setup-timezone" list="vastora-timezones" onChange={(event) => onTimezone(event.target.value)} required value={timezone} /><TimezoneOptions /><FieldDescription>{copy(language, "已读取当前浏览器的时区。日志和计划任务会按此显示。", "Detected from this browser. Logs and schedules use this time zone.")}</FieldDescription></Field><Field><FieldLabel htmlFor="setup-domain">{copy(language, "默认域名（可选）", "Default domain (optional)")}</FieldLabel><Input id="setup-domain" onChange={(event) => onDomainSuffix(event.target.value.toLowerCase())} placeholder="example.com" value={domainSuffix} /><FieldDescription>{copy(language, "连接 Cloudflare 后会自动使用所选域名，也可以在这里提前填写。", "The selected Cloudflare zone is used automatically, or enter one here now.")}</FieldDescription></Field></FieldGroup></CardContent><CardFooter className="justify-end"><Button disabled={!name || !timezone} type="submit">{copy(language, "继续", "Continue")}</Button></CardFooter></form>;
}

function ConnectionMode({ language, mode, onMode }: { language: Language; mode: AgentConnectionMode; onMode: (mode: AgentConnectionMode) => void }) {
  return <FieldSet><FieldLegend>{copy(language, "主要接入环境", "Primary connection environment")}</FieldLegend><div className="grid gap-3">{connectionOptions.map((option) => { const Icon = option.icon; return <label className="flex min-h-20 cursor-pointer items-start gap-3 rounded-xl border p-4 has-checked:border-primary has-checked:bg-primary/5" key={option.mode}><input checked={mode === option.mode} className="mt-1" name="connection-mode" onChange={() => onMode(option.mode)} type="radio" value={option.mode} /><Icon aria-hidden="true" className="mt-0.5 size-5 shrink-0" /><span className="min-w-0 flex-1"><span className="flex items-center gap-2 text-sm font-medium">{copy(language, option.zh, option.en)}{option.mode === "headscale" ? <Badge variant="secondary">{copy(language, "跨站点推荐", "Recommended across locations")}</Badge> : null}</span><span className="mt-1 block text-xs leading-5 text-muted-foreground">{copy(language, option.descriptionZh, option.descriptionEn)}</span></span></label>; })}</div></FieldSet>;
}

type HeadscaleSetupProps = { language: Language; builtinAvailable: boolean; headscaleMode: "builtin" | "external"; headscaleUrl: string; headscaleApiKey: string; dnsMode: "cloudflare" | "manual"; cloudflareOAuthAvailable: boolean; cloudflareConfigured: boolean; cloudflareZone: string; publicAddress: string; publicAddressCandidates: NetworkCandidate[]; onHeadscaleMode: (value: "builtin" | "external") => void; onHeadscaleUrl: (value: string) => void; onHeadscaleApiKey: (value: string) => void; onDNSMode: (value: "cloudflare" | "manual") => void; onCloudflareConnected: (zone: CloudflareZone) => void; onPublicAddress: (value: string) => void };

function HeadscaleSetup(props: HeadscaleSetupProps) {
  const { language, headscaleMode } = props;
  return <div className="rounded-xl border bg-muted/25 p-4"><FieldGroup><Field><FieldLabel htmlFor="setup-headscale-mode">{copy(language, "Headscale 来源", "Headscale source")}</FieldLabel><NativeSelect id="setup-headscale-mode" onChange={(event) => props.onHeadscaleMode(event.target.value as "builtin" | "external")} value={headscaleMode}><option disabled={!props.builtinAvailable} value="builtin">{copy(language, "自动安装到 Center 服务器", "Install automatically on the Center server")}</option><option value="external">{copy(language, "连接已有 Headscale", "Connect an existing Headscale")}</option></NativeSelect></Field>{headscaleMode === "builtin" ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "向导会自动完成安装", "The wizard installs it automatically")}</AlertTitle><AlertDescription>{copy(language, "Vastora 会安装固定版本、申请 HTTPS 证书并安全保存自动创建的 API Key。需要开放 80 和 8443。", "Vastora installs a fixed version, obtains HTTPS certificates, and securely stores the generated API key. Ports 80 and 8443 must be open.")}</AlertDescription></Alert> : null}<Field><FieldLabel htmlFor="setup-headscale-url">Headscale URL</FieldLabel><Input id="setup-headscale-url" onChange={(event) => props.onHeadscaleUrl(event.target.value)} placeholder={headscaleMode === "builtin" ? "https://headscale.example.com:8443" : "https://headscale.example.com"} required type="url" value={props.headscaleUrl} /><FieldDescription>{headscaleMode === "builtin" ? copy(language, "必须使用独立域名和 8443 端口。", "Use a separate hostname and port 8443.") : copy(language, "填写浏览器和节点都能访问的地址。", "Use an address reachable by browsers and nodes.")}</FieldDescription></Field>{headscaleMode === "builtin" ? <DNSSetup {...props} /> : <Field><FieldLabel htmlFor="setup-headscale-key">API Key</FieldLabel><Input autoComplete="off" id="setup-headscale-key" minLength={20} onChange={(event) => props.onHeadscaleApiKey(event.target.value)} required type="password" value={props.headscaleApiKey} /><FieldDescription>{copy(language, "只会加密保存，不会再次显示。", "Stored only in encrypted form and never shown again.")}</FieldDescription></Field>}</FieldGroup></div>;
}

function DNSSetup(props: HeadscaleSetupProps) {
  const { language } = props;
  const automaticAvailable = props.cloudflareOAuthAvailable && props.publicAddressCandidates.length > 0;
  return <FieldSet><FieldLegend>{copy(language, "域名解析", "DNS setup")}</FieldLegend><FieldDescription>{copy(language, "登录 Cloudflare 后，Vastora 可以自动创建 Center 与 Headscale 的解析记录。", "After Cloudflare login, Vastora can create the Center and Headscale records automatically.")}</FieldDescription><Field><FieldLabel htmlFor="setup-dns-mode">{copy(language, "配置方式", "Configuration method")}</FieldLabel><NativeSelect id="setup-dns-mode" onChange={(event) => props.onDNSMode(event.target.value as "cloudflare" | "manual")} value={props.dnsMode}><option disabled={!automaticAvailable} value="cloudflare">{copy(language, "Cloudflare 自动配置", "Configure with Cloudflare")}</option><option value="manual">{copy(language, "我已手动配置", "I configured DNS manually")}</option></NativeSelect>{props.publicAddressCandidates.length === 0 ? <FieldDescription>{copy(language, "未发现配置在本机网卡上的公网地址，无法安全自动创建记录。", "No public address assigned to a local interface was found, so automatic records are unavailable.")}</FieldDescription> : null}</Field>{props.dnsMode === "cloudflare" ? <><CloudflareOAuthConnect available={props.cloudflareOAuthAvailable} connected={props.cloudflareConfigured} language={language} onConnected={props.onCloudflareConnected} zoneName={props.cloudflareZone} /><Field><FieldLabel htmlFor="setup-public-address">{copy(language, "Center 公网地址", "Center public address")}</FieldLabel><NativeSelect id="setup-public-address" onChange={(event) => props.onPublicAddress(event.target.value)} value={props.publicAddress}>{props.publicAddressCandidates.map((candidate) => <option key={candidate.address} value={candidate.address}>{candidate.address} — {candidate.interface}</option>)}</NativeSelect><FieldDescription>{copy(language, "只列出真实配置在服务器网卡上的地址，不使用 NAT 出口 IP。", "Only addresses assigned to server interfaces are listed; NAT egress addresses are excluded.")}</FieldDescription></Field></> : <Alert><Globe2Icon /><AlertTitle>{copy(language, "请先完成 DNS", "Configure DNS first")}</AlertTitle><AlertDescription>{copy(language, "将 Center 和 Headscale 域名都解析到这台服务器的公网地址，关闭代理，并等待解析生效。", "Point both Center and Headscale hostnames to this server's public address, disable proxying, and wait for DNS propagation.")}</AlertDescription></Alert>}</FieldSet>;
}

function ReviewStep({ language, busy, error, name, timezone, domainSuffix, mode, selected, agentConnectUrl, headscaleMode, headscaleUrl, dnsMode, cloudflareZone, onBack, onFinish }: { language: Language; busy: boolean; error: string; name: string; timezone: string; domainSuffix: string; mode: AgentConnectionMode; selected: (typeof connectionOptions)[number]; agentConnectUrl: string; headscaleMode: "builtin" | "external"; headscaleUrl: string; dnsMode: "cloudflare" | "manual"; cloudflareZone: string; onBack: () => void; onFinish: () => Promise<void> }) {
  const installsHeadscale = mode === "headscale" && headscaleMode === "builtin";
  return <><CardHeader><CardTitle>{copy(language, "确认首次设置", "Review initial setup")}</CardTitle><CardDescription>{copy(language, "完成后将直接进入“添加节点”。位置可在主页修改，单个节点也可覆盖连接地址。", "After finishing, Vastora opens Add node. Locations remain editable, and individual nodes can override the address.")}</CardDescription></CardHeader><CardContent className="flex flex-col gap-5"><Alert><CheckIcon /><AlertTitle>{copy(language, "已准备好", "Ready to finish")}</AlertTitle><AlertDescription>{installsHeadscale ? copy(language, "下一步会先确认 DNS，再安装 Headscale 与 HTTPS 网关，通常需要一到三分钟。不会占用 443。", "The next step confirms DNS, then installs Headscale and its HTTPS gateway. It usually takes one to three minutes and does not use port 443.") : copy(language, "不会自动安装应用或开放公网端口。", "No apps are installed and no public ports are opened automatically.")}</AlertDescription></Alert><dl className="grid gap-4 rounded-xl border p-4 text-sm sm:grid-cols-2"><Summary icon={MapPinIcon} label={copy(language, "位置", "Location")} value={name} /><Summary icon={NetworkIcon} label={copy(language, "接入环境", "Connection")} value={copy(language, selected.zh, selected.en)} /><Summary icon={Globe2Icon} label={copy(language, "Agent 连接地址", "Agent address")} value={agentConnectUrl} wide />{mode === "headscale" ? <Summary icon={ShieldCheckIcon} label="Headscale" value={headscaleMode === "builtin" ? copy(language, `自动安装 · ${headscaleUrl}`, `Automatic install · ${headscaleUrl}`) : headscaleUrl} wide /> : null}{installsHeadscale ? <Summary icon={Globe2Icon} label="DNS" value={dnsMode === "cloudflare" ? `Cloudflare · ${cloudflareZone}` : copy(language, "手动配置", "Manual")} wide /> : null}<Summary icon={HouseIcon} label={copy(language, "时区", "Time zone")} value={timezone} /><Summary icon={Globe2Icon} label={copy(language, "默认域名", "Default domain")} value={domainSuffix || copy(language, "未设置", "Not set")} /></dl>{busy && installsHeadscale ? <Alert><Spinner /><AlertTitle>{copy(language, "正在安装 Headscale", "Installing Headscale")}</AlertTitle><AlertDescription>{copy(language, "正在配置 DNS、下载固定版本、启动服务并申请 HTTPS 证书。请保持页面打开。", "Configuring DNS, downloading the fixed version, starting services, and obtaining HTTPS certificates. Keep this page open.")}</AlertDescription></Alert> : null}{error ? <FieldError role="alert">{error}</FieldError> : null}</CardContent><CardFooter className="justify-between"><Button disabled={busy} onClick={onBack} variant="outline">{copy(language, "返回", "Back")}</Button><Button disabled={busy} onClick={() => void onFinish()}>{busy ? <Spinner data-icon="inline-start" /> : null}{busy && installsHeadscale ? copy(language, "正在安装…", "Installing…") : copy(language, "完成并添加节点", "Finish and add a node")}</Button></CardFooter></>;
}

function SetupProgress({ language, step }: { language: Language; step: number }) { const labels = [copy(language, "位置", "Location"), copy(language, "网络", "Network"), copy(language, "确认", "Review")]; return <ol aria-label={copy(language, "设置进度", "Setup progress")} className="grid grid-cols-3 gap-2">{labels.map((label, index) => { const value = index + 1; return <li aria-current={value === step ? "step" : undefined} className="flex items-center gap-2 text-xs" key={label}><span className={`grid size-6 shrink-0 place-items-center rounded-full border font-semibold ${value < step ? "border-primary bg-primary text-primary-foreground" : value === step ? "border-primary text-primary" : "text-muted-foreground"}`}>{value < step ? <CheckIcon aria-hidden="true" className="size-3.5" /> : value}</span><span className={value === step ? "font-medium" : "text-muted-foreground"}>{label}</span></li>; })}</ol>; }
function Summary({ icon: Icon, label, value, wide }: { icon: LucideIcon; label: string; value: string; wide?: boolean }) { return <div className={wide ? "sm:col-span-2" : ""}><dt className="flex items-center gap-2 text-muted-foreground"><Icon aria-hidden="true" className="size-4" />{label}</dt><dd className="mt-1 break-words font-medium">{value}</dd></div>; }
function TimezoneOptions() { return <datalist id="vastora-timezones"><option value="UTC" /><option value="Asia/Shanghai" /><option value="Asia/Singapore" /><option value="Asia/Tokyo" /><option value="Europe/London" /><option value="America/Los_Angeles" /><option value="America/New_York" /></datalist>; }
function validHeadscaleURL(value: string) { try { const parsed = new URL(value); return parsed.protocol === "https:" && parsed.username === "" && parsed.password === "" && parsed.pathname === "/" && parsed.search === "" && parsed.hash === ""; } catch { return false; } }
