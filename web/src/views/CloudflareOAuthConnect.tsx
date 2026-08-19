import { useEffect, useRef, useState } from "react";
import { CheckCircle2Icon, CircleAlertIcon, CloudIcon, ExternalLinkIcon } from "lucide-react";
import { api } from "../api";
import type { CloudflareZone } from "../types";
import type { Language } from "../translations";
import { copy } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { NativeSelect } from "@/components/ui/native-select";
import { Spinner } from "@/components/ui/spinner";

type Props = {
  available: boolean;
  connected: boolean;
  language: Language;
  zoneName?: string;
  onConnected: (zone: CloudflareZone) => void | Promise<void>;
};

export function CloudflareOAuthConnect({ available, connected, language, zoneName, onConnected }: Props) {
  const cancelled = useRef(false);
  const [sessionID, setSessionID] = useState("");
  const [zones, setZones] = useState<CloudflareZone[]>([]);
  const [zoneID, setZoneID] = useState("");
  const [stage, setStage] = useState<"idle" | "opening" | "waiting" | "selecting" | "saving">("idle");
  const [error, setError] = useState("");

  useEffect(() => () => { cancelled.current = true; }, []);

  const connect = async () => {
    cancelled.current = false;
    setError("");
    setZones([]);
    setStage("opening");
    const popup = window.open("about:blank", "vastora-cloudflare-oauth", "popup,width=720,height=760");
    if (!popup) {
      setStage("idle");
      setError(copy(language, "浏览器阻止了登录窗口，请允许此站点打开弹窗。", "The browser blocked the login window. Allow pop-ups for this site."));
      return;
    }
    try {
      popup.document.title = "Cloudflare";
      const started = await api.startCloudflareOAuth();
      setSessionID(started.sessionId);
      popup.location.replace(started.authorizationUrl);
      setStage("waiting");
      while (!cancelled.current && Date.now() < new Date(started.expiresAt).getTime()) {
        await new Promise((resolve) => window.setTimeout(resolve, 1500));
        const result = await api.pollCloudflareOAuth(started.sessionId);
        if (result.status === "pending") continue;
        const authorizedZones = result.zones ?? [];
        if (authorizedZones.length === 0) throw new Error(copy(language, "该账号没有可用域名。", "This account has no available zones."));
        setZones(authorizedZones);
        setZoneID(authorizedZones[0].id);
        setStage("selecting");
        popup.close();
        return;
      }
      throw new Error(copy(language, "Cloudflare 登录已超时，请重新连接。", "Cloudflare login timed out. Connect again."));
    } catch (connectError) {
      popup.close();
      if (!cancelled.current) {
        setStage("idle");
        setError(connectError instanceof Error ? connectError.message : "Request failed");
      }
    }
  };

  const save = async () => {
    const zone = zones.find((value) => value.id === zoneID);
    if (!zone) return;
    setStage("saving");
    setError("");
    try {
      await api.completeCloudflareOAuth(sessionID, zone.id);
      await onConnected(zone);
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
  return <section aria-label={copy(language, "连接 Cloudflare", "Connect Cloudflare")} className="rounded-2xl border bg-background p-4 shadow-xs sm:p-5">
    <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
      <div className="flex min-w-0 items-start gap-3">
        <span className="grid size-11 shrink-0 place-items-center rounded-xl bg-orange-500/10 text-orange-600 dark:text-orange-400"><CloudIcon aria-hidden="true" className="size-6" /></span>
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2"><h3 className="font-medium">{copy(language, "连接 Cloudflare", "Connect Cloudflare")}</h3><span className={`rounded-full px-2 py-0.5 text-xs font-medium ${connected ? "bg-primary/10 text-primary" : "bg-muted text-muted-foreground"}`}>{connected ? copy(language, "已连接", "Connected") : copy(language, "尚未连接", "Not connected")}</span></div>
          <p className="mt-1 text-sm leading-5 text-muted-foreground">{connected && zoneName ? copy(language, `已选择 ${zoneName}。Vastora 只管理自己创建的 DNS 记录。`, `${zoneName} selected. Vastora only manages DNS records it creates.`) : copy(language, "仅用于创建 Vastora 需要的 DNS 记录，不会更改其他域名。", "Only creates DNS records Vastora needs and does not change other domains.")}</p>
        </div>
      </div>
      {stage !== "selecting" && stage !== "saving" ? <Button className="min-h-11 shrink-0 sm:self-center" disabled={busy} onClick={() => void connect()} type="button" variant={connected ? "outline" : "default"}>{stage === "opening" ? <Spinner data-icon="inline-start" /> : <ExternalLinkIcon aria-hidden="true" data-icon="inline-start" />}{busy ? copy(language, "等待授权…", "Waiting…") : connected ? copy(language, "重新连接", "Reconnect") : copy(language, "登录 Cloudflare", "Sign in to Cloudflare")}</Button> : null}
    </div>
    {stage === "waiting" ? <Alert className="mt-4"><Spinner /><AlertTitle>{copy(language, "在新窗口完成登录", "Finish signing in in the new window")}</AlertTitle><AlertDescription>{copy(language, "授权完成后，本页会自动让你选择域名。", "After authorization, this page will automatically ask you to choose a domain.")}</AlertDescription></Alert> : null}
    {stage === "selecting" || stage === "saving" ? <div className="mt-4 flex flex-col gap-3 border-t pt-4 sm:flex-row sm:items-end"><Field className="flex-1"><FieldLabel htmlFor="cloudflare-zone">{copy(language, "选择用于 Vastora 的域名", "Choose a domain for Vastora")}</FieldLabel><NativeSelect disabled={stage === "saving"} id="cloudflare-zone" onChange={(event) => setZoneID(event.target.value)} value={zoneID}>{zones.map((zone) => <option key={zone.id} value={zone.id}>{zone.name}{zone.accountName ? ` — ${zone.accountName}` : ""}</option>)}</NativeSelect><FieldDescription>{copy(language, "后续仍可以在网络设置中更换。", "You can change this later in Network settings.")}</FieldDescription></Field><Button className="min-h-11" disabled={stage === "saving"} onClick={() => void save()} type="button">{stage === "saving" ? <Spinner data-icon="inline-start" /> : <CheckCircle2Icon aria-hidden="true" data-icon="inline-start" />}{copy(language, "使用这个域名", "Use this domain")}</Button></div> : null}
    {error ? <Alert className="mt-4" variant="destructive"><CircleAlertIcon /><AlertTitle>{friendlyOAuthError(language, error)}</AlertTitle><AlertDescription><p>{copy(language, "没有保存任何授权，也没有修改 DNS。请重试。", "No authorization was saved and no DNS was changed. Try again.")}</p><details className="mt-2"><summary className="cursor-pointer text-xs font-medium">{copy(language, "查看技术详情", "Show technical details")}</summary><p className="mt-2 break-words font-mono text-xs">{error}</p></details></AlertDescription></Alert> : null}
  </section>;
}

function friendlyOAuthError(language: Language, error: string) {
  const scopeMismatch = /scope.+(invalid|unknown|malformed)|not allowed to request scope/i.test(error);
  if (scopeMismatch) return copy(language, "Cloudflare 授权配置不匹配，请更新 Center 后重试", "Cloudflare authorization does not match this Center version. Update Center and try again");
  return copy(language, "Cloudflare 登录没有完成", "Cloudflare sign-in did not finish");
}
