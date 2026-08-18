import { useState, type FormEvent } from "react";
import { CheckIcon, Globe2Icon, HouseIcon, LanguagesIcon, MapPinIcon, NetworkIcon, ShieldCheckIcon, type LucideIcon } from "lucide-react";
import type { AgentConnectionMode, InitialSetupInput } from "../types";
import type { Language } from "../translations";
import { browserTimezone, validCenterURL } from "../lib/network";
import { Brand, copy } from "./shared";
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
  onLanguage: (language: Language) => void;
  onComplete: (input: InitialSetupInput) => Promise<void>;
};

const connectionOptions: Array<{ mode: AgentConnectionMode; icon: LucideIcon; zh: string; en: string; descriptionZh: string; descriptionEn: string }> = [
  { mode: "lan", icon: HouseIcon, zh: "局域网", en: "Local network", descriptionZh: "Center 和节点在同一家庭、办公室或数据中心网络。", descriptionEn: "Center and nodes share a home, office, or data-center network." },
  { mode: "headscale", icon: ShieldCheckIcon, zh: "Headscale 私网", en: "Headscale private network", descriptionZh: "适合跨地区节点；节点先加入私网，再连接 Center。", descriptionEn: "Best for nodes across locations. Nodes join the private network before Center." },
  { mode: "public", icon: Globe2Icon, zh: "公网 HTTPS", en: "Public HTTPS", descriptionZh: "Center 有稳定域名和 HTTPS，可被各节点直接访问。", descriptionEn: "Center has a stable HTTPS hostname reachable by every node." }
];

export function SetupWizard({ language, suggestedAgentConnectUrl, onLanguage, onComplete }: SetupWizardProps) {
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
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const nextLocation = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError("");
    setStep(2);
  };
  const nextNetwork = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError("");
    if (!validCenterURL(agentConnectUrl)) {
      setError(copy(language, "Agent 连接地址必须是没有路径的 HTTPS URL；只有本机回环地址可以使用 HTTP。", "The Agent connection address must be an HTTPS URL without a path. Only loopback may use HTTP."));
      return;
    }
    if (mode === "headscale" && (!headscaleUrl.startsWith("https://") || headscaleApiKey.trim().length < 20)) {
      setError(copy(language, "Headscale 需要 HTTPS 控制面地址和有效的 API Key。", "Headscale requires an HTTPS control-plane URL and a valid API key."));
      return;
    }
    setStep(3);
  };
  const finish = async () => {
    setBusy(true); setError("");
    const input: InitialSetupInput = {
      site: { name, code, description: "", timezone, domainSuffix, gatewayNodes: [] },
      network: { agentConnectionMode: mode, agentConnectUrl: agentConnectUrl.replace(/\/$/, "") }
    };
    if (mode === "headscale") input.headscale = { mode: headscaleMode, url: headscaleUrl.replace(/\/$/, ""), apiKey: headscaleApiKey };
    try {
      await onComplete(input);
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : "Request failed");
    } finally {
      setBusy(false);
    }
  };

  const selected = connectionOptions.find((option) => option.mode === mode)!;
  return <main className="min-h-svh bg-muted/35 p-5 md:p-8"><div className="mx-auto flex w-full max-w-2xl flex-col gap-6"><div className="flex items-center justify-between"><Brand /><Button aria-label={copy(language, "切换语言", "Change language")} onClick={() => onLanguage(language === "zh-CN" ? "en" : "zh-CN")} size="icon" variant="ghost"><LanguagesIcon /></Button></div><SetupProgress language={language} step={step} /><Card>
    {step === 1 ? <form onSubmit={nextLocation}><CardHeader><CardTitle>{copy(language, "创建第一个位置", "Create your first location")}</CardTitle><CardDescription>{copy(language, "位置通常是一处家庭、办公室或数据中心，用来归类同一网络中的节点。", "A location is usually a home, office, or data center and groups nodes on the same network.")}</CardDescription></CardHeader><CardContent><FieldGroup><Field><FieldLabel htmlFor="setup-location-name">{copy(language, "位置名称", "Location name")}</FieldLabel><Input autoFocus id="setup-location-name" maxLength={128} onChange={(event) => setName(event.target.value)} placeholder={copy(language, "例如：新加坡机房", "For example: Singapore data center")} required value={name} /></Field><Field><FieldLabel htmlFor="setup-timezone">{copy(language, "时区", "Time zone")}</FieldLabel><Input id="setup-timezone" list="vastora-timezones" onChange={(event) => setTimezone(event.target.value)} required value={timezone} /><TimezoneOptions /><FieldDescription>{copy(language, "已读取当前浏览器的时区。日志和计划任务会按此显示。", "Detected from this browser. Logs and schedules use this time zone.")}</FieldDescription></Field><Field><FieldLabel htmlFor="setup-domain">{copy(language, "默认域名（可选）", "Default domain (optional)")}</FieldLabel><Input id="setup-domain" onChange={(event) => setDomainSuffix(event.target.value.toLowerCase())} placeholder="example.com" value={domainSuffix} /><FieldDescription>{copy(language, "以后发布服务时仍可为每个入口指定其他域名。", "Each access point can still use a different hostname later.")}</FieldDescription></Field></FieldGroup></CardContent><CardFooter className="justify-end"><Button disabled={!name || !timezone} type="submit">{copy(language, "继续", "Continue")}</Button></CardFooter></form> : null}
    {step === 2 ? <form onSubmit={nextNetwork}><CardHeader><CardTitle>{copy(language, "节点如何连接 Center？", "How will nodes reach Center?")}</CardTitle><CardDescription>{copy(language, "这是添加节点时的默认接入方式。以后仍可以同时启用局域网、Headscale 和公网能力。", "This is the default joining path. LAN, Headscale, and public capabilities can still be enabled together later.")}</CardDescription></CardHeader><CardContent><FieldGroup>{!suggestedAgentConnectUrl ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "安装向导通过 SSH 隧道打开", "Setup is open through an SSH tunnel")}</AlertTitle><AlertDescription>{copy(language, "下面填写 Agent 以后实际能访问的 Center 地址，不要填写本机浏览器中的 127.0.0.1:18082。", "Enter the Center address that Agents will actually reach later, not 127.0.0.1:18082 from this browser.")}</AlertDescription></Alert> : null}<FieldSet><FieldLegend>{copy(language, "主要接入环境", "Primary connection environment")}</FieldLegend><div className="grid gap-3">{connectionOptions.map((option) => { const Icon = option.icon; return <label className="flex min-h-20 cursor-pointer items-start gap-3 rounded-xl border p-4 has-checked:border-primary has-checked:bg-primary/5" key={option.mode}><input checked={mode === option.mode} className="mt-1" name="connection-mode" onChange={() => setMode(option.mode)} type="radio" value={option.mode} /><Icon aria-hidden="true" className="mt-0.5 size-5 shrink-0" /><span className="min-w-0 flex-1"><span className="flex items-center gap-2 text-sm font-medium">{copy(language, option.zh, option.en)}{option.mode === "headscale" ? <Badge variant="secondary">{copy(language, "跨站点推荐", "Recommended across locations")}</Badge> : null}</span><span className="mt-1 block text-xs leading-5 text-muted-foreground">{copy(language, option.descriptionZh, option.descriptionEn)}</span></span></label>; })}</div></FieldSet><Field><FieldLabel htmlFor="setup-center-url">{copy(language, "Agent 连接地址", "Agent connection address")}</FieldLabel><Input id="setup-center-url" onChange={(event) => setAgentConnectUrl(event.target.value)} placeholder="https://center.example.com" required type="url" value={agentConnectUrl} /><FieldDescription>{suggestedAgentConnectUrl ? copy(language, "已使用安装 Center 时配置的地址。节点必须能访问它。", "Filled from the Center installation. Every node must be able to reach it.") : copy(language, "填写目标节点能够访问的 Center HTTPS 地址。", "Enter the Center HTTPS address reachable from target nodes.")}</FieldDescription></Field>{mode === "headscale" ? <div className="rounded-xl border bg-muted/25 p-4"><FieldGroup><Field><FieldLabel htmlFor="setup-headscale-mode">{copy(language, "Headscale 来源", "Headscale source")}</FieldLabel><NativeSelect id="setup-headscale-mode" onChange={(event) => setHeadscaleMode(event.target.value as "builtin" | "external")} value={headscaleMode}><option value="builtin">{copy(language, "Center 安装包内置", "Bundled with Center")}</option><option value="external">{copy(language, "已有 Headscale", "Existing Headscale")}</option></NativeSelect></Field><Field><FieldLabel htmlFor="setup-headscale-url">Headscale URL</FieldLabel><Input id="setup-headscale-url" onChange={(event) => setHeadscaleUrl(event.target.value)} placeholder="https://headscale.example.com" required type="url" value={headscaleUrl} /></Field><Field><FieldLabel htmlFor="setup-headscale-key">API Key</FieldLabel><Input autoComplete="off" id="setup-headscale-key" minLength={20} onChange={(event) => setHeadscaleApiKey(event.target.value)} required type="password" value={headscaleApiKey} /><FieldDescription>{copy(language, "内置安装时，密钥保存在 generated/headscale-api-key.txt。", "For the bundled install, the key is saved in generated/headscale-api-key.txt.")}</FieldDescription></Field></FieldGroup></div> : null}{error ? <FieldError>{error}</FieldError> : null}</FieldGroup></CardContent><CardFooter className="justify-between"><Button onClick={() => { setError(""); setStep(1); }} type="button" variant="outline">{copy(language, "返回", "Back")}</Button><Button type="submit">{copy(language, "检查配置", "Review")}</Button></CardFooter></form> : null}
    {step === 3 ? <><CardHeader><CardTitle>{copy(language, "确认首次设置", "Review initial setup")}</CardTitle><CardDescription>{copy(language, "完成后将直接进入“添加节点”。位置可在主页修改，单个节点也可覆盖连接地址。", "After finishing, Vastora opens Add node. Locations remain editable, and individual nodes can override the address.")}</CardDescription></CardHeader><CardContent className="flex flex-col gap-5"><Alert><CheckIcon /><AlertTitle>{copy(language, "已准备好", "Ready to finish")}</AlertTitle><AlertDescription>{copy(language, "不会自动安装应用或开放公网端口。", "No apps are installed and no public ports are opened automatically.")}</AlertDescription></Alert><dl className="grid gap-4 rounded-xl border p-4 text-sm sm:grid-cols-2"><Summary icon={MapPinIcon} label={copy(language, "位置", "Location")} value={name} /><Summary icon={NetworkIcon} label={copy(language, "接入环境", "Connection")} value={copy(language, selected.zh, selected.en)} /><Summary icon={Globe2Icon} label={copy(language, "Agent 连接地址", "Agent address")} value={agentConnectUrl} wide /><Summary icon={HouseIcon} label={copy(language, "时区", "Time zone")} value={timezone} /><Summary icon={Globe2Icon} label={copy(language, "默认域名", "Default domain")} value={domainSuffix || copy(language, "未设置", "Not set")} /></dl>{error ? <FieldError>{error}</FieldError> : null}</CardContent><CardFooter className="justify-between"><Button disabled={busy} onClick={() => { setError(""); setStep(2); }} variant="outline">{copy(language, "返回", "Back")}</Button><Button disabled={busy} onClick={() => void finish()}>{busy ? <Spinner data-icon="inline-start" /> : null}{copy(language, "完成并添加节点", "Finish and add a node")}</Button></CardFooter></> : null}
  </Card></div></main>;
}

function SetupProgress({ language, step }: { language: Language; step: number }) {
  const labels = [copy(language, "位置", "Location"), copy(language, "网络", "Network"), copy(language, "确认", "Review")];
  return <ol aria-label={copy(language, "设置进度", "Setup progress")} className="grid grid-cols-3 gap-2">{labels.map((label, index) => { const value = index + 1; return <li aria-current={value === step ? "step" : undefined} className="flex items-center gap-2 text-xs" key={label}><span className={`grid size-6 shrink-0 place-items-center rounded-full border font-semibold ${value < step ? "border-primary bg-primary text-primary-foreground" : value === step ? "border-primary text-primary" : "text-muted-foreground"}`}>{value < step ? <CheckIcon aria-hidden="true" className="size-3.5" /> : value}</span><span className={value === step ? "font-medium" : "text-muted-foreground"}>{label}</span></li>; })}</ol>;
}

function Summary({ icon: Icon, label, value, wide }: { icon: LucideIcon; label: string; value: string; wide?: boolean }) {
  return <div className={wide ? "sm:col-span-2" : ""}><dt className="flex items-center gap-2 text-muted-foreground"><Icon aria-hidden="true" className="size-4" />{label}</dt><dd className="mt-1 break-words font-medium">{value}</dd></div>;
}

function TimezoneOptions() {
  return <datalist id="vastora-timezones"><option value="UTC" /><option value="Asia/Shanghai" /><option value="Asia/Singapore" /><option value="Asia/Tokyo" /><option value="Europe/London" /><option value="America/Los_Angeles" /><option value="America/New_York" /></datalist>;
}
