import { useEffect, useRef, useState } from "react";
import { CheckCircle2Icon, CircleAlertIcon, CloudIcon, CopyIcon, ExternalLinkIcon, XIcon } from "lucide-react";
import { api } from "../api";
import type { CloudflareZone } from "../types";
import type { Language } from "../translations";
import { copy } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { SelectControl } from "@/components/SelectControl";
import { Spinner } from "@/components/ui/spinner";

type Props = {
  available: boolean;
  connected: boolean;
  language: Language;
  zoneName?: string;
  onConnected: (zone: CloudflareZone) => void | Promise<void>;
  onAuthorized?: (sessionID: string, zone: CloudflareZone) => void | Promise<void>;
};

export function CloudflareOAuthConnect({ available, connected, language, zoneName, onConnected, onAuthorized }: Props) {
  const cancelled = useRef(false);
  const authorizationWindow = useRef<Window | null>(null);
  const [sessionID, setSessionID] = useState("");
  const [authorizationURL, setAuthorizationURL] = useState("");
  const [copied, setCopied] = useState(false);
  const [zones, setZones] = useState<CloudflareZone[]>([]);
  const [zoneID, setZoneID] = useState("");
  const [stage, setStage] = useState<"idle" | "opening" | "waiting" | "selecting" | "saving">("idle");
  const [error, setError] = useState("");

  useEffect(() => () => {
    cancelled.current = true;
    authorizationWindow.current?.close();
  }, []);

  const connect = async () => {
    cancelled.current = false;
    setError("");
    setZones([]);
    setAuthorizationURL("");
    setCopied(false);
    setStage("opening");
    const popup = openAuthorizationWindow(copy(language, "正在打开 Cloudflare…", "Opening Cloudflare…"));
    if (!popup) {
      setStage("idle");
      setError(copy(language, "浏览器阻止了登录窗口，请允许此站点打开弹窗。", "The browser blocked the login window. Allow pop-ups for this site."));
      return;
    }
    authorizationWindow.current = popup;
    try {
      const started = await api.startCloudflareOAuth();
      setSessionID(started.sessionId);
      setAuthorizationURL(started.authorizationUrl);
      popup.location.replace(started.authorizationUrl);
      setStage("waiting");
      while (!cancelled.current && Date.now() < new Date(started.expiresAt).getTime()) {
        await new Promise((resolve) => window.setTimeout(resolve, 1500));
        if (cancelled.current) return;
        const result = await api.pollCloudflareOAuth(started.sessionId);
        if (result.status === "pending") continue;
        const authorizedZones = result.zones ?? [];
        if (authorizedZones.length === 0) throw new Error(copy(language, "该账号没有可用域名。", "This account has no available zones."));
        setZones(authorizedZones);
        setZoneID(authorizedZones[0].id);
        setStage("selecting");
        popup.close();
        authorizationWindow.current = null;
        return;
      }
      throw new Error(copy(language, "Cloudflare 登录已超时，请重新连接。", "Cloudflare login timed out. Connect again."));
    } catch (connectError) {
      popup.close();
      authorizationWindow.current = null;
      if (!cancelled.current) {
        setStage("idle");
        setAuthorizationURL("");
        setError(connectError instanceof Error ? connectError.message : "Request failed");
      }
    }
  };

  const reopenAuthorization = () => {
    if (!authorizationURL) return;
    const popup = window.open(authorizationURL, "_blank");
    if (!popup) {
      setError(copy(language, "浏览器阻止了登录标签页，请复制链接并在 Safari、Chrome 或 Edge 中打开。", "The browser blocked the sign-in tab. Copy the link and open it in Safari, Chrome, or Edge."));
      return;
    }
    popup.opener = null;
    authorizationWindow.current = popup;
  };

  const copyAuthorization = async () => {
    if (!authorizationURL) return;
    try {
      await navigator.clipboard.writeText(authorizationURL);
      authorizationWindow.current?.close();
      authorizationWindow.current = null;
      setCopied(true);
    } catch {
      setError(copy(language, "无法复制登录链接，请使用“重新打开登录页”。", "The sign-in link could not be copied. Use Reopen sign-in page."));
    }
  };

  const cancelAuthorization = () => {
    cancelled.current = true;
    authorizationWindow.current?.close();
    authorizationWindow.current = null;
    setSessionID("");
    setAuthorizationURL("");
    setCopied(false);
    setStage("idle");
    setError("");
  };

  const save = async () => {
    const zone = zones.find((value) => value.id === zoneID);
    if (!zone) return;
    setStage("saving");
    setError("");
    try {
      if (onAuthorized) {
        await onAuthorized(sessionID, zone);
      } else {
        await api.completeCloudflareOAuth(sessionID, zone.id);
        await onConnected(zone);
      }
      setZones([]);
      setSessionID("");
      setStage("idle");
    } catch (saveError) {
      setStage("selecting");
      setError(saveError instanceof Error ? saveError.message : "Request failed");
    }
  };

  if (!available) return <Alert><CloudIcon /><AlertTitle>{copy(language, "需要更新 Center", "Center update required")}</AlertTitle><AlertDescription>{copy(language, "当前版本还不能自动连接 Cloudflare。升级后可以直接登录，无需复制 Token。", "This version cannot connect Cloudflare automatically. Upgrade to sign in without copying a token.")}</AlertDescription></Alert>;

  const busy = stage === "opening" || stage === "waiting";
  return <section aria-label={copy(language, "连接 Cloudflare", "Connect Cloudflare")} className="@container/cloudflare-oauth min-w-0 rounded-2xl border bg-background p-4 shadow-xs sm:p-5">
    <div className="flex min-w-0 flex-col gap-4 @md/cloudflare-oauth:flex-row @md/cloudflare-oauth:items-center @md/cloudflare-oauth:justify-between">
      <div className="flex min-w-0 items-start gap-3">
        <span className="grid size-11 shrink-0 place-items-center rounded-xl bg-orange-500/10 text-orange-600 dark:text-orange-400"><CloudIcon aria-hidden="true" className="size-6" /></span>
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2"><h3 className="font-medium">{copy(language, "连接 Cloudflare", "Connect Cloudflare")}</h3><span className={`rounded-full px-2 py-0.5 text-xs font-medium ${connected ? "bg-primary/10 text-primary" : "bg-muted text-muted-foreground"}`}>{connected ? copy(language, "已连接", "Connected") : copy(language, "尚未连接", "Not connected")}</span></div>
          <p className="mt-1 text-sm leading-5 text-muted-foreground">{connected && zoneName ? copy(language, `已选择 ${zoneName}。Vastora 只管理自己创建的 DNS、Tunnel、Access 与 Turnstile 资源。`, `${zoneName} selected. Vastora manages only the DNS, Tunnel, Access, and Turnstile resources it creates.`) : copy(language, "用于管理 Vastora 自己的 DNS、Tunnel、Access 与 Turnstile 资源，不会更改其他域名。", "Used to manage Vastora-owned DNS, Tunnel, Access, and Turnstile resources without changing other hostnames.")}</p>
        </div>
      </div>
      {stage !== "selecting" && stage !== "saving" ? <Button className="min-h-11 w-full shrink-0 @md/cloudflare-oauth:w-auto @md/cloudflare-oauth:self-center" disabled={busy} onClick={() => void connect()} type="button" variant={connected ? "outline" : "default"}>{stage === "opening" ? <Spinner data-icon="inline-start" /> : <ExternalLinkIcon aria-hidden="true" data-icon="inline-start" />}{busy ? copy(language, "等待授权…", "Waiting…") : connected ? copy(language, "重新连接", "Reconnect") : copy(language, "登录 Cloudflare", "Sign in to Cloudflare")}</Button> : null}
    </div>
    {stage === "waiting" ? <Alert className="mt-4"><Spinner /><AlertTitle>{copy(language, "在新标签页完成登录", "Finish signing in in the new tab")}</AlertTitle><AlertDescription className="flex flex-col gap-3"><p>{copy(language, "Cloudflare 首次加载可能需要几秒。授权完成后，本页会自动让你选择域名。", "Cloudflare may take a few seconds to load. After authorization, this page will automatically ask you to choose a domain.")}</p><p>{copy(language, "如果登录页持续空白，请复制链接并在 Safari、Chrome 或 Edge 中打开。", "If the sign-in page stays blank, copy the link and open it in Safari, Chrome, or Edge.")}</p><div className="flex flex-wrap gap-2"><Button onClick={reopenAuthorization} size="sm" type="button" variant="outline"><ExternalLinkIcon data-icon="inline-start" />{copy(language, "重新打开登录页", "Reopen sign-in page")}</Button><Button onClick={() => void copyAuthorization()} size="sm" type="button" variant="outline"><CopyIcon data-icon="inline-start" />{copied ? copy(language, "已复制", "Copied") : copy(language, "复制登录链接", "Copy sign-in link")}</Button><Button onClick={cancelAuthorization} size="sm" type="button" variant="ghost"><XIcon data-icon="inline-start" />{copy(language, "取消", "Cancel")}</Button></div></AlertDescription></Alert> : null}
    {stage === "selecting" || stage === "saving" ? <div className="mt-4 flex min-w-0 flex-col gap-3 border-t pt-4 @md/cloudflare-oauth:flex-row @md/cloudflare-oauth:items-end"><Field className="min-w-0 flex-1"><FieldLabel htmlFor="cloudflare-zone">{copy(language, "选择用于 Vastora 的域名", "Choose a domain for Vastora")}</FieldLabel><SelectControl disabled={stage === "saving"} id="cloudflare-zone" onValueChange={setZoneID} options={zones.map((zone) => ({ value: zone.id, label: `${zone.name}${zone.accountName ? ` — ${zone.accountName}` : ""}` }))} value={zoneID} /><FieldDescription>{copy(language, "后续仍可以在网络设置中更换。", "You can change this later in Network settings.")}</FieldDescription></Field><Button className="min-h-11 w-full @md/cloudflare-oauth:w-auto" disabled={stage === "saving"} onClick={() => void save()} type="button">{stage === "saving" ? <Spinner data-icon="inline-start" /> : <CheckCircle2Icon aria-hidden="true" data-icon="inline-start" />}{copy(language, "使用这个域名", "Use this domain")}</Button></div> : null}
    {error ? <Alert className="mt-4" variant="destructive"><CircleAlertIcon /><AlertTitle>{friendlyOAuthError(language, error)}</AlertTitle><AlertDescription><p>{copy(language, "没有保存任何授权，也没有修改 DNS。请重试。", "No authorization was saved and no DNS was changed. Try again.")}</p><details className="mt-2"><summary className="cursor-pointer text-xs font-medium">{copy(language, "查看技术详情", "Show technical details")}</summary><p className="mt-2 break-words font-mono text-xs">{error}</p></details></AlertDescription></Alert> : null}
  </section>;
}

function openAuthorizationWindow(message: string) {
  const popup = window.open("about:blank", "_blank");
  if (!popup) return null;
  popup.opener = null;
  try {
    popup.document.title = "Cloudflare";
    popup.document.documentElement.style.colorScheme = "light";
    popup.document.body.style.cssText = "margin:0;min-height:100vh;display:grid;place-items:center;background:#fff;color:#18181b;font:16px system-ui,sans-serif";
    const status = popup.document.createElement("p");
    status.textContent = message;
    popup.document.body.append(status);
  } catch {
    // Navigation still works if the browser does not expose about:blank.
  }
  return popup;
}

function friendlyOAuthError(language: Language, error: string) {
  const scopeMismatch = /scope.+(invalid|unknown|malformed)|not allowed to request scope/i.test(error);
  if (scopeMismatch) return copy(language, "Cloudflare 授权配置不匹配，请更新 Center 后重试", "Cloudflare authorization does not match this Center version. Update Center and try again");
  return copy(language, "Cloudflare 登录没有完成", "Cloudflare sign-in did not finish");
}
