import { useEffect, useState, type FormEvent } from "react";
import { CheckIcon, ChevronDownIcon, Globe2Icon, HouseIcon, LanguagesIcon, MapPinIcon, NetworkIcon, Settings2Icon, ShieldCheckIcon, type LucideIcon } from "lucide-react";
import { api } from "../api";
import type { AgentConnectionMode, CloudflareZone, InitialSetupInput, NetworkCandidate } from "../types";
import type { Language } from "../translations";
import { browserTimezone, validCenterURL, vastoraDomainDefaults } from "../lib/network";
import { Brand, copy, TechnicalError } from "./shared";
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
  { mode: "lan", icon: HouseIcon, zh: "同一网络", en: "Same network", descriptionZh: "Center 和服务器在同一个局域网。", descriptionEn: "Center and servers are on the same local network." },
  { mode: "headscale", icon: ShieldCheckIcon, zh: "随时随地", en: "Anywhere", descriptionZh: "通过安全私网连接远程服务器。", descriptionEn: "Connect remote servers through a secure private network." },
  { mode: "public", icon: Globe2Icon, zh: "已有公网地址", en: "Existing public address", descriptionZh: "Center 已经有域名和 HTTPS。", descriptionEn: "Center already has a hostname and HTTPS." }
];

const setupDraftKey = "vastora.initial-setup.v1";

type SetupDraft = {
  step: 1 | 2;
  name: string;
  timezone: string;
  domainSuffix: string;
  mode: AgentConnectionMode;
  agentConnectUrl: string;
  headscaleMode: "builtin" | "external";
  headscaleUrl: string;
  publicAddress: string;
};

export function SetupWizard(props: SetupWizardProps) {
  const { language, suggestedAgentConnectUrl, builtinHeadscaleAvailable, cloudflareOAuthAvailable, publicAddressCandidates, onLanguage, onComplete } = props;
  const [draft] = useState(readSetupDraft);
  const configuredCloudflareZone = props.cloudflareZone ?? "";
	const initialCloudflareZone = configuredCloudflareZone || inferLegacyCloudflareZone(draft);
	const initialDomainDefaults = vastoraDomainDefaults(initialCloudflareZone);
	const automaticHeadscaleAvailable = builtinHeadscaleAvailable && cloudflareOAuthAvailable && publicAddressCandidates.length > 0;
  const [step, setStep] = useState<1 | 2 | 3>(draft.step ?? 1);
  const [name, setName] = useState(draft.name ?? "");
  const [timezone, setTimezone] = useState(draft.timezone ?? browserTimezone);
  const [domainSuffix, setDomainSuffix] = useState(() => preferNamespacedDefault(draft.domainSuffix, [initialCloudflareZone], initialDomainDefaults.namespace));
  const [code] = useState(() => `site-${crypto.randomUUID().slice(0, 8)}`);
  const [mode, setMode] = useState<AgentConnectionMode>(draft.mode ?? "lan");
  const [agentConnectUrl, setAgentConnectUrl] = useState(() => preferNamespacedDefault(draft.agentConnectUrl ?? suggestedAgentConnectUrl, [`https://center.${initialCloudflareZone}`], initialDomainDefaults.centerURL));
	const [headscaleMode, setHeadscaleMode] = useState<"builtin" | "external">(draft.headscaleMode === "external" || !automaticHeadscaleAvailable ? "external" : "builtin");
  const [headscaleUrl, setHeadscaleUrl] = useState(() => preferNamespacedDefault(draft.headscaleUrl, [`https://headscale.${initialCloudflareZone}`], initialDomainDefaults.headscaleURL));
  const [headscaleApiKey, setHeadscaleApiKey] = useState("");
  const [cloudflareConfigured, setCloudflareConfigured] = useState(props.cloudflareConfigured);
  const [cloudflareZone, setCloudflareZone] = useState(configuredCloudflareZone);
  const [publicAddress, setPublicAddress] = useState(draft.publicAddress ?? publicAddressCandidates[0]?.address ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [submitError, setSubmitError] = useState<unknown>(null);

  useEffect(() => {
    writeSetupDraft({ step: step === 1 ? 1 : 2, name, timezone, domainSuffix, mode, agentConnectUrl, headscaleMode, headscaleUrl, publicAddress });
  }, [step, name, timezone, domainSuffix, mode, agentConnectUrl, headscaleMode, headscaleUrl, publicAddress]);

  const nextLocation = (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); setError(""); setSubmitError(null); setStep(2); };
  const nextNetwork = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault(); setError(""); setSubmitError(null);
    if (!validCenterURL(agentConnectUrl)) { setError(copy(language, "Center 地址应类似 https://center.example.com，不能包含账号、密码或路径。", "The Center address should look like https://center.example.com and cannot contain credentials or a path.")); return; }
    if (mode === "headscale") {
      if (!validHeadscaleURL(headscaleUrl)) { setError(copy(language, "私网地址应类似 https://headscale.example.com，不能包含账号、密码或路径。", "The private-network address should look like https://headscale.example.com and cannot contain credentials or a path.")); return; }
      if (headscaleMode === "builtin" && (!builtinHeadscaleAvailable || new URL(headscaleUrl).port !== "" || new URL(agentConnectUrl).port !== "")) { setError(copy(language, "自动安装的 Center 和私网地址使用标准 HTTPS，不需要填写端口。", "Automatically installed Center and private-network addresses use standard HTTPS without an explicit port.")); return; }
      if (headscaleMode === "external" && headscaleApiKey.trim().length < 20) { setError(copy(language, "已有私网服务需要有效的 API Key。", "An existing private-network service requires a valid API key.")); return; }
		if (headscaleMode === "builtin" && (!cloudflareOAuthAvailable || !cloudflareConfigured || !publicAddress)) { setError(copy(language, "内置安全私网需要先登录 Cloudflare，用于 Headscale 公网域名和 Center 私网 HTTPS 证书。", "Built-in secure networking requires Cloudflare sign-in for the public Headscale hostname and Center's private HTTPS certificate.")); return; }
      if (headscaleMode === "builtin") {
        setAgentConnectUrl(canonicalStandardHTTPSURL(agentConnectUrl));
        setHeadscaleUrl(canonicalStandardHTTPSURL(headscaleUrl));
      }
    }
    setStep(3);
  };
  const finish = async () => {
    setBusy(true); setError(""); setSubmitError(null);
    const builtinGateway = mode === "headscale" && headscaleMode === "builtin";
    const centerURL = builtinGateway ? canonicalStandardHTTPSURL(agentConnectUrl) : agentConnectUrl.replace(/\/$/, "");
    const privateNetworkURL = builtinGateway ? canonicalStandardHTTPSURL(headscaleUrl) : headscaleUrl.replace(/\/$/, "");
    const input: InitialSetupInput = { site: { name, code, description: "", timezone, domainSuffix, gatewayNodes: [] }, network: { agentConnectionMode: mode, agentConnectUrl: centerURL } };
    if (mode === "headscale") input.headscale = { mode: headscaleMode, url: privateNetworkURL, ...(headscaleMode === "external" ? { apiKey: headscaleApiKey } : {}) };
    try {
		if (builtinGateway) await api.configureSetupDNS({ centerUrl: centerURL, headscaleUrl: privateNetworkURL, publicAddress });
      await onComplete(input);
      clearSetupDraft();
    } catch (failure) { setSubmitError(failure); } finally { setBusy(false); }
  };
  const connectedCloudflare = (zone: CloudflareZone) => {
    const previousDefaults = vastoraDomainDefaults(cloudflareZone);
    const nextDefaults = vastoraDomainDefaults(zone.name);
    setCloudflareConfigured(true);
    setCloudflareZone(nextDefaults.zone);
    setDomainSuffix((current) => preferNamespacedDefault(current, [cloudflareZone, previousDefaults.namespace, nextDefaults.zone], nextDefaults.namespace));
    setAgentConnectUrl((current) => preferNamespacedDefault(current, [`https://center.${cloudflareZone}`, previousDefaults.centerURL, `https://center.${nextDefaults.zone}`], nextDefaults.centerURL));
    setHeadscaleUrl((current) => preferNamespacedDefault(current, [`https://headscale.${cloudflareZone}`, previousDefaults.headscaleURL, `https://headscale.${nextDefaults.zone}`], nextDefaults.headscaleURL));
  };
  const selected = connectionOptions.find((option) => option.mode === mode)!;

  return <main className="min-h-svh bg-muted/25 p-4 sm:p-6 md:p-8"><div className="mx-auto flex w-full max-w-5xl flex-col gap-6">
    <div className="flex items-center justify-between"><Brand /><Button aria-label={copy(language, "切换语言", "Change language")} onClick={() => onLanguage(language === "zh-CN" ? "en" : "zh-CN")} size="icon" variant="ghost"><LanguagesIcon /></Button></div>
    <SetupProgress language={language} step={step} />
    <Card>
      {step === 1 ? <LocationStep language={language} name={name} timezone={timezone} domainSuffix={domainSuffix} onName={setName} onTimezone={setTimezone} onDomainSuffix={setDomainSuffix} onSubmit={nextLocation} /> : null}
      {step === 2 ? <form onSubmit={nextNetwork}><CardHeader><CardTitle>{copy(language, "你准备在哪里使用 Vastora？", "Where will you use Vastora?")}</CardTitle><CardDescription>{copy(language, "选择最符合你的情况，具体网络设置可以稍后调整。", "Choose what best matches your situation. Network details can be changed later.")}</CardDescription></CardHeader><CardContent><FieldGroup>
		{!suggestedAgentConnectUrl ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "当前是临时访问地址", "You are using a temporary access address")}</AlertTitle><AlertDescription>{copy(language, "请继续选择使用方式。安全私网模式会让 Center 只在 Headscale 网络内开放。", "Choose how you will use Vastora. Secure private mode exposes Center only inside the Headscale network.")}</AlertDescription></Alert> : null}
        <ConnectionMode language={language} mode={mode} onMode={setMode} />
        {mode !== "headscale" ? <Field><FieldLabel htmlFor="setup-center-url">{copy(language, "Center 地址", "Center address")}</FieldLabel><Input id="setup-center-url" onChange={(event) => setAgentConnectUrl(event.target.value)} placeholder="https://center.example.com" required type="url" value={agentConnectUrl} /><FieldDescription>{suggestedAgentConnectUrl ? copy(language, "已自动填写。以后添加的服务器会使用这个地址连接 Center。", "Filled automatically. Servers added later will use this address to reach Center.") : copy(language, "填写服务器能够访问的地址，不要使用当前浏览器里的 127.0.0.1。", "Enter an address servers can reach, not the 127.0.0.1 address in this browser.")}</FieldDescription></Field> : null}
		{mode === "headscale" ? <HeadscaleSetup language={language} agentConnectUrl={agentConnectUrl} builtinAvailable={builtinHeadscaleAvailable} headscaleMode={headscaleMode} headscaleUrl={headscaleUrl} headscaleApiKey={headscaleApiKey} cloudflareOAuthAvailable={cloudflareOAuthAvailable} cloudflareConfigured={cloudflareConfigured} cloudflareZone={cloudflareZone} publicAddress={publicAddress} publicAddressCandidates={publicAddressCandidates} onAgentConnectUrl={setAgentConnectUrl} onHeadscaleMode={setHeadscaleMode} onHeadscaleUrl={setHeadscaleUrl} onHeadscaleApiKey={setHeadscaleApiKey} onCloudflareConnected={connectedCloudflare} onPublicAddress={setPublicAddress} /> : null}
        {error ? <FieldError role="alert">{error}</FieldError> : null}
      </FieldGroup></CardContent><CardFooter className="justify-between"><Button onClick={() => { setError(""); setStep(1); }} type="button" variant="outline">{copy(language, "返回", "Back")}</Button><Button type="submit">{copy(language, "继续", "Continue")}</Button></CardFooter></form> : null}
      {step === 3 ? <ReviewStep language={language} busy={busy} error={error} submitError={submitError} name={name} timezone={timezone} domainSuffix={domainSuffix} mode={mode} selected={selected} agentConnectUrl={agentConnectUrl} headscaleMode={headscaleMode} headscaleUrl={headscaleUrl} cloudflareZone={cloudflareZone} onBack={() => { setError(""); setSubmitError(null); setStep(2); }} onFinish={finish} /> : null}
    </Card>
  </div></main>;
}

function LocationStep({ language, name, timezone, domainSuffix, onName, onTimezone, onDomainSuffix, onSubmit }: { language: Language; name: string; timezone: string; domainSuffix: string; onName: (value: string) => void; onTimezone: (value: string) => void; onDomainSuffix: (value: string) => void; onSubmit: (event: FormEvent<HTMLFormElement>) => void }) {
  return <form onSubmit={onSubmit}><CardHeader><CardTitle>{copy(language, "创建第一个位置", "Create your first location")}</CardTitle><CardDescription>{copy(language, "位置通常是一处家庭、办公室或数据中心，用来归类同一网络中的节点。", "A location is usually a home, office, or data center and groups nodes on the same network.")}</CardDescription></CardHeader><CardContent><FieldGroup><Field><FieldLabel htmlFor="setup-location-name">{copy(language, "位置名称", "Location name")}</FieldLabel><Input autoFocus id="setup-location-name" maxLength={128} onChange={(event) => onName(event.target.value)} placeholder={copy(language, "例如：新加坡机房", "For example: Singapore data center")} required value={name} /></Field><Field><FieldLabel htmlFor="setup-timezone">{copy(language, "时区", "Time zone")}</FieldLabel><Input id="setup-timezone" list="vastora-timezones" onChange={(event) => onTimezone(event.target.value)} required value={timezone} /><TimezoneOptions /><FieldDescription>{copy(language, "已读取当前浏览器的时区。日志和计划任务会按此显示。", "Detected from this browser. Logs and schedules use this time zone.")}</FieldDescription></Field><Field><FieldLabel htmlFor="setup-domain">{copy(language, "服务域名空间（可选）", "Service domain namespace (optional)")}</FieldLabel><Input autoCapitalize="none" autoCorrect="off" id="setup-domain" onChange={(event) => onDomainSuffix(event.target.value.toLowerCase())} placeholder="vastora.example.com" spellCheck={false} value={domainSuffix} /><FieldDescription>{copy(language, "连接 Cloudflare 后默认使用 vastora.根域名；Center、私网和应用都会放在这个空间下。", "After Cloudflare is connected, Vastora uses vastora.your-domain by default for Center, private networking, and apps.")}</FieldDescription></Field></FieldGroup></CardContent><CardFooter className="justify-end"><Button disabled={!name || !timezone} type="submit">{copy(language, "继续", "Continue")}</Button></CardFooter></form>;
}

function ConnectionMode({ language, mode, onMode }: { language: Language; mode: AgentConnectionMode; onMode: (mode: AgentConnectionMode) => void }) {
  return <FieldSet><FieldLegend className="sr-only">{copy(language, "使用场景", "Usage")}</FieldLegend><div className="grid gap-3 lg:grid-cols-3">{connectionOptions.map((option) => { const Icon = option.icon; const selected = mode === option.mode; return <label className="relative flex min-h-28 cursor-pointer items-start gap-3 rounded-2xl border p-4 transition-colors hover:bg-muted/40 has-checked:border-primary has-checked:bg-primary/5 focus-within:ring-2 focus-within:ring-ring focus-within:ring-offset-2" key={option.mode}><input checked={selected} className="mt-1 size-4 accent-primary" name="connection-mode" onChange={() => onMode(option.mode)} type="radio" value={option.mode} /><span className={`grid size-11 shrink-0 place-items-center rounded-xl ${selected ? "bg-primary/10 text-primary" : "bg-muted text-muted-foreground"}`}><Icon aria-hidden="true" className="size-5" /></span><span className="min-w-0 flex-1"><span className="flex flex-wrap items-center gap-2 text-sm font-medium">{copy(language, option.zh, option.en)}{option.mode === "headscale" ? <Badge variant="secondary">{copy(language, "推荐", "Recommended")}</Badge> : null}</span><span className="mt-1 block text-xs leading-5 text-muted-foreground">{copy(language, option.descriptionZh, option.descriptionEn)}</span></span></label>; })}</div></FieldSet>;
}

type HeadscaleSetupProps = { language: Language; agentConnectUrl: string; builtinAvailable: boolean; headscaleMode: "builtin" | "external"; headscaleUrl: string; headscaleApiKey: string; cloudflareOAuthAvailable: boolean; cloudflareConfigured: boolean; cloudflareZone: string; publicAddress: string; publicAddressCandidates: NetworkCandidate[]; onAgentConnectUrl: (value: string) => void; onHeadscaleMode: (value: "builtin" | "external") => void; onHeadscaleUrl: (value: string) => void; onHeadscaleApiKey: (value: string) => void; onCloudflareConnected: (zone: CloudflareZone) => void; onPublicAddress: (value: string) => void };

function HeadscaleSetup(props: HeadscaleSetupProps) {
  const { language, headscaleMode } = props;
  const automaticAvailable = props.cloudflareOAuthAvailable && props.publicAddressCandidates.length > 0;
  return <section aria-labelledby="secure-connection-title" className="flex flex-col gap-5 border-t pt-6">
    <div><h2 className="text-base font-semibold" id="secure-connection-title">{copy(language, "设置安全连接", "Set up a secure connection")}</h2><p className="mt-1 text-sm leading-6 text-muted-foreground">{copy(language, "Headscale 保留一个公网 HTTPS 入口；Center 控制面只在加密私网内开放。", "Headscale keeps one public HTTPS entry while the Center console is exposed only inside the encrypted private network.")}</p></div>
    {headscaleMode === "builtin" ? <ol className="grid gap-3 sm:grid-cols-3"><SetupOutcome language={language} number="1" titleZh="连接 Cloudflare" titleEn="Connect Cloudflare" descriptionZh="配置公网入口与 DNS-01" descriptionEn="Configure public entry and DNS-01" /><SetupOutcome language={language} number="2" titleZh="安装安全私网" titleEn="Install secure network" descriptionZh="Headscale 使用 Caddy HTTPS" descriptionEn="Headscale uses Caddy HTTPS" /><SetupOutcome language={language} number="3" titleZh="添加当前主机" titleEn="Add this host" descriptionZh="Center 转入私网访问" descriptionEn="Move Center to private access" /></ol> : null}
    {headscaleMode === "builtin" ? <CloudflareOAuthConnect available={props.cloudflareOAuthAvailable} connected={props.cloudflareConfigured} language={language} onConnected={props.onCloudflareConnected} zoneName={props.cloudflareZone} /> : null}
    <div className="grid gap-4 sm:grid-cols-2"><Field><FieldLabel htmlFor="setup-center-url">{copy(language, "Center 私网地址", "Private Center address")}</FieldLabel><Input id="setup-center-url" onChange={(event) => props.onAgentConnectUrl(event.target.value)} placeholder="https://center.example.com" required type="url" value={props.agentConnectUrl} /><FieldDescription>{copy(language, "只写入 Headscale DNS，不会创建公网 A/AAAA 记录。", "Published only in Headscale DNS; no public A/AAAA record is created.")}</FieldDescription></Field><Field><FieldLabel htmlFor="setup-headscale-url">{copy(language, "Headscale 公网地址", "Public Headscale address")}</FieldLabel><Input id="setup-headscale-url" onChange={(event) => props.onHeadscaleUrl(event.target.value)} placeholder="https://headscale.example.com" required type="url" value={props.headscaleUrl} /><FieldDescription>{copy(language, "Caddy 自动提供 HTTPS，节点通过这里加入私网。", "Caddy provides HTTPS automatically and nodes use it to join the private network.")}</FieldDescription></Field></div>
    {headscaleMode === "external" ? <Field><FieldLabel htmlFor="setup-headscale-key">API Key</FieldLabel><Input autoComplete="off" id="setup-headscale-key" minLength={20} onChange={(event) => props.onHeadscaleApiKey(event.target.value)} required type="password" value={props.headscaleApiKey} /><FieldDescription>{copy(language, "只会加密保存，不会再次显示。", "Stored only in encrypted form and never shown again.")}</FieldDescription></Field> : null}
    <details className="group rounded-2xl border bg-muted/20 p-4"><summary className="flex min-h-6 cursor-pointer list-none items-center gap-3 text-sm font-medium"><Settings2Icon aria-hidden="true" className="size-4 text-muted-foreground" /><span>{copy(language, "高级设置", "Advanced settings")}</span><span className="ml-auto hidden text-xs font-normal text-muted-foreground sm:inline">{copy(language, "Headscale 来源和公网地址", "Headscale source and public address")}</span><ChevronDownIcon aria-hidden="true" className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-180" /></summary><FieldGroup className="mt-5 border-t pt-5"><Field><FieldLabel htmlFor="setup-headscale-mode">{copy(language, "私网服务", "Private-network service")}</FieldLabel><NativeSelect id="setup-headscale-mode" onChange={(event) => props.onHeadscaleMode(event.target.value as "builtin" | "external")} value={headscaleMode}><option disabled={!props.builtinAvailable || !automaticAvailable} value="builtin">{copy(language, "由 Vastora 自动安装（推荐）", "Install automatically with Vastora (recommended)")}</option><option value="external">{copy(language, "连接已有 Headscale", "Connect an existing Headscale")}</option></NativeSelect>{headscaleMode === "builtin" ? <FieldDescription>{copy(language, "只把 Headscale 发布到公网 80/443；Center 使用 Cloudflare DNS-01 证书并绑定 Headscale 地址。", "Only Headscale is published on public ports 80/443. Center uses a Cloudflare DNS-01 certificate and binds to its Headscale address.")}</FieldDescription> : null}</Field>{headscaleMode === "builtin" ? <Field><FieldLabel htmlFor="setup-public-address">{copy(language, "服务器公网地址", "Server public address")}</FieldLabel><NativeSelect id="setup-public-address" onChange={(event) => props.onPublicAddress(event.target.value)} value={props.publicAddress}>{props.publicAddressCandidates.map((candidate) => <option key={candidate.address} value={candidate.address}>{candidate.address} — {candidate.interface}</option>)}</NativeSelect><FieldDescription>{copy(language, "只显示真实配置在服务器网卡上的地址。", "Only addresses assigned to a server interface are shown.")}</FieldDescription></Field> : null}</FieldGroup></details>
  </section>;
}

function SetupOutcome({ language, number, titleZh, titleEn, descriptionZh, descriptionEn }: { language: Language; number: string; titleZh: string; titleEn: string; descriptionZh: string; descriptionEn: string }) {
  return <li className="flex items-start gap-3 rounded-xl bg-muted/45 p-3"><span className="grid size-6 shrink-0 place-items-center rounded-full bg-primary text-xs font-semibold text-primary-foreground">{number}</span><span><span className="block text-sm font-medium">{copy(language, titleZh, titleEn)}</span><span className="mt-0.5 block text-xs leading-5 text-muted-foreground">{copy(language, descriptionZh, descriptionEn)}</span></span></li>;
}

function ReviewStep({ language, busy, error, submitError, name, timezone, domainSuffix, mode, selected, agentConnectUrl, headscaleMode, headscaleUrl, cloudflareZone, onBack, onFinish }: { language: Language; busy: boolean; error: string; submitError: unknown; name: string; timezone: string; domainSuffix: string; mode: AgentConnectionMode; selected: (typeof connectionOptions)[number]; agentConnectUrl: string; headscaleMode: "builtin" | "external"; headscaleUrl: string; cloudflareZone: string; onBack: () => void; onFinish: () => Promise<void> }) {
  const installsHeadscale = mode === "headscale" && headscaleMode === "builtin";
  return <><CardHeader><CardTitle>{copy(language, "确认首次设置", "Review initial setup")}</CardTitle><CardDescription>{copy(language, "完成后将直接进入“添加节点”。位置可在主页修改，单个节点也可覆盖连接地址。", "After finishing, Vastora opens Add node. Locations remain editable, and individual nodes can override the address.")}</CardDescription></CardHeader><CardContent className="flex flex-col gap-5"><Alert><CheckIcon /><AlertTitle>{copy(language, "已准备好", "Ready to finish")}</AlertTitle><AlertDescription>{installsHeadscale ? copy(language, "下一步会先确认 DNS，再安装安全私网与 HTTPS 网关，通常需要一到三分钟。网关会使用标准 443。", "The next step confirms DNS, then installs the secure private network and its HTTPS gateway. It usually takes one to three minutes and uses standard port 443.") : copy(language, "不会自动安装应用或开放公网端口。", "No apps are installed and no public ports are opened automatically.")}</AlertDescription></Alert><dl className="grid gap-4 rounded-xl border p-4 text-sm sm:grid-cols-2"><Summary icon={MapPinIcon} label={copy(language, "位置", "Location")} value={name} /><Summary icon={NetworkIcon} label={copy(language, "接入环境", "Connection")} value={copy(language, selected.zh, selected.en)} /><Summary icon={Globe2Icon} label={copy(language, "Agent 连接地址", "Agent address")} value={agentConnectUrl} wide />{mode === "headscale" ? <Summary icon={ShieldCheckIcon} label={copy(language, "安全私网", "Secure private network")} value={headscaleMode === "builtin" ? copy(language, `自动安装 · ${headscaleUrl}`, `Automatic install · ${headscaleUrl}`) : headscaleUrl} wide /> : null}{installsHeadscale ? <Summary icon={Globe2Icon} label="DNS" value={`Cloudflare · ${cloudflareZone}`} wide /> : null}<Summary icon={HouseIcon} label={copy(language, "时区", "Time zone")} value={timezone} /><Summary icon={Globe2Icon} label={copy(language, "服务域名空间", "Service domain namespace")} value={domainSuffix || copy(language, "未设置", "Not set")} /></dl>{busy && installsHeadscale ? <Alert><Spinner /><AlertTitle>{copy(language, "正在安装安全私网", "Installing secure private network")}</AlertTitle><AlertDescription>{copy(language, "正在配置 DNS、下载固定版本、启动服务并申请 HTTPS 证书。请保持页面打开。", "Configuring DNS, downloading the fixed version, starting services, and obtaining HTTPS certificates. Keep this page open.")}</AlertDescription></Alert> : null}{submitError ? <div role="alert"><TechnicalError error={submitError} language={language} /></div> : error ? <FieldError role="alert">{error}</FieldError> : null}</CardContent><CardFooter className="justify-between"><Button disabled={busy} onClick={onBack} variant="outline">{copy(language, "返回", "Back")}</Button><Button disabled={busy} onClick={() => void onFinish()}>{busy ? <Spinner data-icon="inline-start" /> : null}{busy && installsHeadscale ? copy(language, "正在安装…", "Installing…") : copy(language, "完成并添加节点", "Finish and add a node")}</Button></CardFooter></>;
}

function SetupProgress({ language, step }: { language: Language; step: number }) { const labels = [copy(language, "位置", "Location"), copy(language, "连接", "Connection"), copy(language, "完成", "Finish")]; return <ol aria-label={copy(language, "设置进度", "Setup progress")} className="grid grid-cols-3 gap-2">{labels.map((label, index) => { const value = index + 1; return <li aria-current={value === step ? "step" : undefined} className="flex items-center gap-2 text-xs" key={label}><span className={`grid size-6 shrink-0 place-items-center rounded-full border font-semibold ${value < step ? "border-primary bg-primary text-primary-foreground" : value === step ? "border-primary text-primary" : "text-muted-foreground"}`}>{value < step ? <CheckIcon aria-hidden="true" className="size-3.5" /> : value}</span><span className={value === step ? "font-medium" : "text-muted-foreground"}>{label}</span></li>; })}</ol>; }
function Summary({ icon: Icon, label, value, wide }: { icon: LucideIcon; label: string; value: string; wide?: boolean }) { return <div className={wide ? "sm:col-span-2" : ""}><dt className="flex items-center gap-2 text-muted-foreground"><Icon aria-hidden="true" className="size-4" />{label}</dt><dd className="mt-1 break-words font-medium">{value}</dd></div>; }
function TimezoneOptions() { return <datalist id="vastora-timezones"><option value="UTC" /><option value="Asia/Shanghai" /><option value="Asia/Singapore" /><option value="Asia/Tokyo" /><option value="Europe/London" /><option value="America/Los_Angeles" /><option value="America/New_York" /></datalist>; }
function validHeadscaleURL(value: string) { try { const parsed = new URL(value); return parsed.protocol === "https:" && parsed.username === "" && parsed.password === "" && parsed.pathname === "/" && parsed.search === "" && parsed.hash === ""; } catch { return false; } }

function readSetupDraft(): Partial<SetupDraft> {
  try {
    const parsed = JSON.parse(window.sessionStorage.getItem(setupDraftKey) ?? "null") as Partial<SetupDraft> | null;
    if (!parsed || typeof parsed !== "object") return {};
    const mode = connectionOptions.some((option) => option.mode === parsed.mode) ? parsed.mode : undefined;
    const headscaleMode = parsed.headscaleMode === "builtin" || parsed.headscaleMode === "external" ? parsed.headscaleMode : undefined;
    return {
      step: parsed.step === 2 ? 2 : 1,
      name: typeof parsed.name === "string" ? parsed.name : undefined,
      timezone: typeof parsed.timezone === "string" ? parsed.timezone : undefined,
      domainSuffix: typeof parsed.domainSuffix === "string" ? parsed.domainSuffix : undefined,
      mode,
      agentConnectUrl: stringValue(parsed.agentConnectUrl),
      headscaleMode,
      headscaleUrl: stringValue(parsed.headscaleUrl),
      publicAddress: typeof parsed.publicAddress === "string" ? parsed.publicAddress : undefined
    };
  } catch {
    return {};
  }
}

function stringValue(value: unknown) {
  return typeof value === "string" ? value : undefined;
}

function canonicalStandardHTTPSURL(value: string) {
  return new URL(value).toString().replace(/\/$/, "");
}

function preferNamespacedDefault(value: string | undefined, generatedValues: string[], nextValue: string) {
  const current = value?.trim() ?? "";
  if (!nextValue) return current;
  const normalized = current.toLowerCase().replace(/\/$/, "");
  const generated = generatedValues.some((candidate) => candidate && normalized === candidate.toLowerCase().replace(/\/$/, ""));
  return !current || generated ? nextValue : current;
}

function inferLegacyCloudflareZone(draft: Partial<SetupDraft>) {
  const zone = draft.domainSuffix?.trim().toLowerCase().replace(/\.+$/, "") ?? "";
  if (!zone) return "";
  const centerURL = draft.agentConnectUrl?.trim().toLowerCase().replace(/\/$/, "") ?? "";
  const headscaleURL = draft.headscaleUrl?.trim().toLowerCase().replace(/\/$/, "") ?? "";
  return centerURL === `https://center.${zone}` && headscaleURL === `https://headscale.${zone}` ? zone : "";
}

function writeSetupDraft(draft: SetupDraft) {
  try {
    window.sessionStorage.setItem(setupDraftKey, JSON.stringify(draft));
  } catch {
    // Setup still works when browser storage is unavailable.
  }
}

function clearSetupDraft() {
  try {
    window.sessionStorage.removeItem(setupDraftKey);
  } catch {
    // The completed setup does not depend on browser storage cleanup.
  }
}
