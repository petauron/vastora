import { useEffect, useState, type FormEvent } from "react";
import { ShieldCheckIcon } from "lucide-react";
import type { CenterRemoteAccess, CenterRemoteAccessInput, CloudflareZone, Integration } from "../types";
import type { Language } from "../translations";
import { centerRemoteAccessHostname, validRemoteAccessAudience } from "../lib/center-remote-access";
import { copy, userError } from "./shared";
import { CloudflareOAuthConnect } from "./CloudflareOAuthConnect";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { SelectControl } from "@/components/SelectControl";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";

type Props = {
  access: CenterRemoteAccess;
  cloudflare: Integration;
  language: Language;
  open: boolean;
  onClose: () => void;
  onCloudflareConnected: (zone: CloudflareZone) => Promise<void>;
  onSave: (input: CenterRemoteAccessInput) => Promise<void>;
};

export function CenterRemoteAccessSheet({ access, cloudflare, language, open, onClose, onCloudflareConnected, onSave }: Props) {
  const [enabled, setEnabled] = useState(access.enabled);
  const [protectionMode, setProtectionMode] = useState<"access" | "native">(access.protectionMode ?? "native");
  const [audienceKind, setAudienceKind] = useState<"email" | "email_domain">(access.audienceKind ?? "email");
  const [audienceValue, setAudienceValue] = useState(access.audienceValue ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) return;
    setEnabled(access.enabled);
    setProtectionMode(access.protectionMode ?? "native");
    setAudienceKind(access.audienceKind ?? "email");
    setAudienceValue(access.audienceValue ?? "");
    setError("");
  }, [open, access.enabled, access.protectionMode, access.audienceKind, access.audienceValue]);

  const cloudflareOAuthConnected = cloudflare.status === "configured" && cloudflare.mode === "oauth";
  const requiredPermissionGranted = protectionMode === "native" ? cloudflare.turnstileManagement === true : cloudflare.accessManagement === true;
  const cloudflareConnected = cloudflareOAuthConnected && requiredPermissionGranted;
  const cloudflareRequired = enabled && !cloudflareConnected;
  const audienceValid = !enabled || protectionMode === "native" || validRemoteAccessAudience(audienceKind, audienceValue);
  const remoteHostname = centerRemoteAccessHostname(cloudflare.endpoint ?? "");

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError("");
    if (enabled && (!cloudflareConnected || !audienceValid)) return;
    setBusy(true);
    try {
      await onSave({
        enabled,
        ...(enabled ? { protectionMode } : {}),
        ...(enabled && protectionMode === "access" ? { audienceKind, audienceValue: audienceValue.trim().toLowerCase().replace(/^@/, "") } : {})
      });
    } catch (failure) {
      setError(userError(language, failure));
    } finally {
      setBusy(false);
    }
  };

  return <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={open}>
    <SheetContent className="min-w-0 overflow-x-hidden sm:max-w-lg">
      <SheetHeader>
        <SheetTitle>{copy(language, "Center 远程备用入口", "Center remote fallback")}</SheetTitle>
        <SheetDescription>{copy(language, "通过 Cloudflare Tunnel 发布专用 HTTPS 登录地址，并选择入口保护方式。", "Publish a dedicated HTTPS sign-in address through Cloudflare Tunnel and choose its protection mode.")}</SheetDescription>
      </SheetHeader>
      <form className="flex min-h-0 min-w-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}>
        <div className="min-w-0 flex-1 overflow-x-hidden overflow-y-auto px-4">
          <FieldGroup>
            <Field orientation="horizontal">
              <FieldLabel htmlFor="center-remote-access-enabled"><span>{copy(language, "启用远程备用入口", "Enable remote fallback")}</span><span className="text-xs font-normal text-muted-foreground">{copy(language, "默认关闭；安全私网仍是首选路径", "Off by default; the secure private network remains preferred")}</span></FieldLabel>
              <Switch checked={enabled} disabled={!access.available} id="center-remote-access-enabled" onCheckedChange={setEnabled} />
            </Field>
            {!access.available ? <Alert><AlertTitle>{copy(language, "部署助手不可用", "Deployment helper unavailable")}</AlertTitle><AlertDescription>{copy(language, "请先修复或更新 Center 安装。Vastora 不会退回到不受管理的 Tunnel。", "Repair or update the Center installation first. Vastora will not fall back to an unmanaged Tunnel.")}</AlertDescription></Alert> : null}
            {enabled ? <>
              <Field>
                <FieldLabel htmlFor="center-remote-protection-mode">{copy(language, "入口保护", "Entry protection")}</FieldLabel>
                <SelectControl id="center-remote-protection-mode" onValueChange={(value) => setProtectionMode(value as "access" | "native")} options={[
                  { value: "native", label: copy(language, "直达 Center 登录（推荐）", "Direct Center sign-in (recommended)") },
                  { value: "access", label: copy(language, "Cloudflare Access 双层登录", "Cloudflare Access two-layer sign-in") }
                ]} value={protectionMode} />
                <FieldDescription>{protectionMode === "native" ? copy(language, "Tunnel 直接打开 Center 登录页；每次公网登录都验证 Turnstile，并执行服务端失败退避与锁定。", "Tunnel opens the Center sign-in page directly. Every public sign-in verifies Turnstile and applies server-side backoff and lockout.") : copy(language, "先通过邮箱一次性验证码，再输入 Center 管理员密码；新建受 Access 保护的应用入口也会复用此策略。", "Pass an email one-time PIN before entering the Center administrator password. New Access-protected application entries also reuse this policy.")}</FieldDescription>
              </Field>
              <Alert><ShieldCheckIcon aria-hidden="true" /><AlertTitle>{protectionMode === "native" ? copy(language, "Tunnel 直连已加固", "Hardened direct Tunnel") : copy(language, "Access 外层 + Center 内层", "Access outside + Center inside")}</AlertTitle><AlertDescription>{copy(language, `公网浏览器入口使用 ${remoteHostname || "center-vastora.example.com"}；不会改变 Agent 使用的私网 Center 地址。`, `The public browser entry uses ${remoteHostname || "center-vastora.example.com"}. It does not change the private Center address used by Agents.`)}</AlertDescription></Alert>
              {!cloudflareConnected ? <>{cloudflareOAuthConnected ? <Alert><AlertTitle>{copy(language, "需要补充 Cloudflare 授权", "Additional Cloudflare permission required")}</AlertTitle><AlertDescription>{protectionMode === "native" ? copy(language, "重新连接一次，为 Vastora 增加创建专用 Turnstile 组件的权限。", "Reconnect once so Vastora can create a dedicated Turnstile widget.") : copy(language, "重新连接一次，为 Vastora 增加管理 Access 应用和登录策略的权限。", "Reconnect once so Vastora can manage Access applications and policies.")}</AlertDescription></Alert> : null}<CloudflareOAuthConnect available connected={cloudflareOAuthConnected} language={language} onConnected={onCloudflareConnected} zoneName={cloudflare.endpoint} /></> : null}
              {protectionMode === "access" ? <>
                <Field><FieldLabel htmlFor="center-remote-access-kind">{copy(language, "允许谁登录", "Who can sign in")}</FieldLabel><SelectControl id="center-remote-access-kind" onValueChange={(value) => setAudienceKind(value as "email" | "email_domain")} options={[{ value: "email", label: copy(language, "指定邮箱（推荐）", "Specific email (recommended)") }, { value: "email_domain", label: copy(language, "整个邮箱域", "Entire email domain") }]} value={audienceKind} /><FieldDescription>{audienceKind === "email" ? copy(language, "仅允许一个明确邮箱。", "Allow one explicit email address.") : copy(language, "该域下所有可接收一次性验证码的邮箱都会被允许。", "Allow every mailbox in the domain that can receive a one-time PIN.")}</FieldDescription></Field>
                <Field data-invalid={!audienceValid}><FieldLabel htmlFor="center-remote-access-audience">{audienceKind === "email" ? copy(language, "登录邮箱", "Sign-in email") : copy(language, "邮箱域名", "Email domain")}</FieldLabel><Input aria-invalid={!audienceValid} autoCapitalize="none" autoCorrect="off" id="center-remote-access-audience" onChange={(event) => setAudienceValue(event.target.value)} placeholder={audienceKind === "email" ? "admin@example.com" : "example.com"} spellCheck={false} type={audienceKind === "email" ? "email" : "text"} value={audienceValue} />{!audienceValid ? <FieldError>{audienceKind === "email" ? copy(language, "请输入有效邮箱。", "Enter a valid email address.") : copy(language, "请输入有效邮箱域名。", "Enter a valid email domain.")}</FieldError> : null}</Field>
              </> : null}
            </> : null}
            {access.status === "failed" && access.lastError ? <Alert variant="destructive"><AlertTitle>{copy(language, "上次配置没有完成", "The previous configuration did not finish")}</AlertTitle><AlertDescription className="break-words">{access.lastError}</AlertDescription></Alert> : null}
            {error ? <FieldError role="alert">{error}</FieldError> : null}
          </FieldGroup>
        </div>
        <SheetFooter>
          {cloudflareRequired ? <p className="text-xs leading-5 text-muted-foreground" id="center-remote-access-save-help" role="status">{copy(language, "请先在上方重新连接 Cloudflare 并授予所需权限。", "Reconnect Cloudflare above and grant the required permission first.")}</p> : null}
          <Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button>
          <Button aria-describedby={cloudflareRequired ? "center-remote-access-save-help" : undefined} disabled={busy || !access.available || enabled && (!cloudflareConnected || !audienceValid)} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{enabled ? copy(language, "保存并启用", "Save and enable") : copy(language, "关闭入口", "Disable entry")}</Button>
        </SheetFooter>
      </form>
    </SheetContent>
  </Sheet>;
}
