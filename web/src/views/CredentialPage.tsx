import { useCallback, useState, type FormEvent } from "react";
import { ArrowRightIcon, EyeIcon, EyeOffIcon } from "lucide-react";
import { APIError } from "@/api";
import { administratorPasswordMinLength } from "@/lib/security";
import type { SetupStatus } from "@/types";
import type { Language } from "@/translations";
import { copy, userError } from "./shared";
import { AuthShell } from "@/components/auth/AuthShell";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { Turnstile } from "@/components/Turnstile";

export function CredentialPage({ language, loginProtection, mode, onLanguage, onSubmit }: { language: Language; loginProtection?: SetupStatus["loginProtection"]; mode: "setup" | "login"; onLanguage: (language: Language) => void; onSubmit: (username: string, password: string, turnstileToken: string) => Promise<void> }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [passwordVisible, setPasswordVisible] = useState(false);
  const [busy, setBusy] = useState(false);
  const [submitError, setSubmitError] = useState<unknown>(null);
  const [turnstileFailure, setTurnstileFailure] = useState<"load" | "verification" | null>(null);
  const [turnstileToken, setTurnstileToken] = useState("");
  const [turnstileReset, setTurnstileReset] = useState(0);
  const captchaRequired = mode === "login" && loginProtection?.captchaRequired === true;
  const error = submitError == null ? "" : credentialError(language, submitError);
  const turnstileError = turnstileFailure === "load"
    ? copy(language, "安全验证没有加载成功，请检查网络后重试。", "The security check did not load. Check your connection and retry.")
    : turnstileFailure === "verification" ? copy(language, "安全验证失败，请完成新的验证后再试。", "The security check failed. Complete a new check and try again.") : "";
  const reportTurnstileError = useCallback(() => setTurnstileFailure("load"), []);
  const acceptTurnstileToken = useCallback((token: string) => { setTurnstileToken(token); if (token) setTurnstileFailure(null); }, []);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy || captchaRequired && !turnstileToken) return;
    setBusy(true); setSubmitError(null); setTurnstileFailure(null);
    try {
      await onSubmit(username, password, turnstileToken);
    } catch (failure) {
      if (failure instanceof APIError && failure.code === "captcha_failed" && captchaRequired) {
        setTurnstileFailure("verification");
      } else {
        setSubmitError(failure);
      }
      if (failure instanceof APIError && (captchaRequired || failure.captchaRequired)) setTurnstileReset((current) => current + 1);
    } finally { setBusy(false); }
  };
  return <AuthShell language={language} onLanguage={onLanguage}
    title={mode === "setup" ? copy(language, "创建管理员", "Create administrator") : copy(language, "登录 Center", "Sign in to Center")}
    description={mode === "setup" ? copy(language, "创建账号后即可继续设置。", "Create an account to continue setup.") : copy(language, "使用管理员账号继续。", "Continue with your administrator account.")}>
    <form onSubmit={(event) => void submit(event)} aria-busy={busy}>
      <FieldGroup>
        <Field data-invalid={Boolean(error)}>
          <FieldLabel htmlFor="username">{copy(language, "账号", "Username")}</FieldLabel>
          <Input aria-describedby={error ? "credential-error" : undefined} aria-invalid={Boolean(error)} autoComplete="username" autoCapitalize="none" spellCheck={false} id="username" minLength={3} onChange={(event) => setUsername(event.target.value)} required value={username} />
        </Field>
        <Field data-invalid={Boolean(error)}>
          <FieldLabel htmlFor="password">{copy(language, "密码", "Password")}</FieldLabel>
          <div className="auth-password-field">
            <Input aria-describedby={[mode === "setup" ? "credential-password-hint" : "", error ? "credential-error" : ""].filter(Boolean).join(" ") || undefined} aria-invalid={Boolean(error)} autoComplete={mode === "setup" ? "new-password" : "current-password"} id="password" minLength={mode === "setup" ? administratorPasswordMinLength : undefined} onChange={(event) => setPassword(event.target.value)} required type={passwordVisible ? "text" : "password"} value={password} />
            <Button className="auth-password-toggle" aria-label={passwordVisible ? copy(language, "隐藏密码", "Hide password") : copy(language, "显示密码", "Show password")} aria-pressed={passwordVisible} onClick={() => setPasswordVisible((visible) => !visible)} size="icon" type="button" variant="ghost">{passwordVisible ? <EyeIcon aria-hidden="true" /> : <EyeOffIcon aria-hidden="true" />}</Button>
          </div>
          {mode === "setup" ? <FieldDescription id="credential-password-hint">{copy(language, "至少 10 个字符。", "At least 10 characters.")}</FieldDescription> : null}
          {error ? <FieldError id="credential-error" role="alert">{error}</FieldError> : null}
        </Field>
        {captchaRequired && loginProtection?.turnstileSiteKey ? <Field className="auth-security-check" data-invalid={Boolean(turnstileError)}>
          <FieldLabel htmlFor="center-login-turnstile">{copy(language, "安全验证", "Security check")}</FieldLabel>
          <Turnstile language={language} onError={reportTurnstileError} onToken={acceptTurnstileToken} resetKey={turnstileReset} siteKey={loginProtection.turnstileSiteKey} />
          {turnstileError ? <><FieldError role="alert">{turnstileError}</FieldError><Button onClick={() => { setTurnstileFailure(null); setTurnstileReset((current) => current + 1); }} size="sm" type="button" variant="outline">{copy(language, "重新加载验证", "Reload security check")}</Button></> : null}
        </Field> : null}
        <Button className="auth-submit" disabled={busy || captchaRequired && !turnstileToken} size="lg" type="submit">
          {busy ? <Spinner data-icon="inline-start" /> : null}
          {mode === "setup" ? copy(language, "创建并继续", "Create and continue") : copy(language, "登录", "Sign in")}
          {!busy ? <ArrowRightIcon aria-hidden="true" data-icon="inline-end" /> : null}
        </Button>
      </FieldGroup>
    </form>
  </AuthShell>;
}

function credentialError(language: Language, error: unknown): string {
  if (error instanceof APIError) {
    if (error.code === "login_throttled" || error.code === "login_protection_unavailable" || error.code === "captcha_failed") {
      return copy(language, "暂时无法登录，请稍后再试。", "Unable to sign in right now. Try again later.");
    }
    if (error.code === "invalid_credentials") return copy(language, "账号或密码不正确。", "The username or password is incorrect.");
  }
  return userError(language, error);
}
