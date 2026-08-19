import { useEffect, useRef, useState } from "react";
import { CheckCircle2Icon, CloudIcon, ExternalLinkIcon } from "lucide-react";
import { api } from "../api";
import type { CloudflareZone } from "../types";
import type { Language } from "../translations";
import { copy } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field";
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

  if (!available) return <Alert><CloudIcon /><AlertTitle>{copy(language, "此版本未启用 Cloudflare 登录", "Cloudflare login is unavailable in this build")}</AlertTitle><AlertDescription>{copy(language, "请先升级 Center。", "Upgrade Center first.")}</AlertDescription></Alert>;
  return <div className="flex flex-col gap-4">
    {connected && stage === "idle" ? <Alert><CheckCircle2Icon /><AlertTitle>{copy(language, "Cloudflare 已连接", "Cloudflare connected")}</AlertTitle><AlertDescription>{zoneName ? copy(language, `当前域名：${zoneName}`, `Current zone: ${zoneName}`) : copy(language, "权限已安全保存。", "Authorization is stored securely.")}</AlertDescription></Alert> : null}
    {stage === "waiting" ? <Alert><Spinner /><AlertTitle>{copy(language, "等待 Cloudflare 授权", "Waiting for Cloudflare authorization")}</AlertTitle><AlertDescription>{copy(language, "请在新窗口确认账号和权限。本页会自动继续。", "Confirm the account and permissions in the new window. This page continues automatically.")}</AlertDescription></Alert> : null}
    {stage === "selecting" || stage === "saving" ? <Field><FieldLabel htmlFor="cloudflare-zone">{copy(language, "管理哪个域名？", "Which zone should Vastora manage?")}</FieldLabel><NativeSelect disabled={stage === "saving"} id="cloudflare-zone" onChange={(event) => setZoneID(event.target.value)} value={zoneID}>{zones.map((zone) => <option key={zone.id} value={zone.id}>{zone.name}{zone.accountName ? ` — ${zone.accountName}` : ""}</option>)}</NativeSelect><FieldDescription>{copy(language, "Vastora 只会管理你明确创建的服务记录。", "Vastora only manages records you explicitly create for services.")}</FieldDescription></Field> : null}
    {error ? <FieldError role="alert">{error}</FieldError> : null}
    <div className="flex justify-end gap-2">
      {stage === "selecting" || stage === "saving" ? <Button disabled={stage === "saving"} onClick={() => void save()} type="button">{stage === "saving" ? <Spinner data-icon="inline-start" /> : null}{copy(language, "确认域名", "Use this zone")}</Button> : <Button disabled={stage === "opening" || stage === "waiting"} onClick={() => void connect()} type="button" variant={connected ? "outline" : "default"}>{stage === "opening" ? <Spinner data-icon="inline-start" /> : <ExternalLinkIcon data-icon="inline-start" />}{connected ? copy(language, "重新授权", "Reconnect") : copy(language, "使用 Cloudflare 登录", "Sign in with Cloudflare")}</Button>}
    </div>
  </div>;
}
