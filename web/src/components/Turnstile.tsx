import { useEffect, useRef, useState } from "react";
import { Spinner } from "@/components/ui/spinner";
import type { Language } from "../translations";
import { copy } from "../views/shared";

type TurnstileAPI = {
  render: (container: HTMLElement, options: Record<string, unknown>) => string;
  remove: (widgetId: string) => void;
};

declare global {
  interface Window {
    turnstile?: TurnstileAPI;
  }
}

let turnstileScript: Promise<TurnstileAPI> | null = null;

function loadTurnstile(): Promise<TurnstileAPI> {
  if (window.turnstile) return Promise.resolve(window.turnstile);
  if (turnstileScript) return turnstileScript;
  const pending = new Promise<TurnstileAPI>((resolve, reject) => {
    const existing = document.querySelector<HTMLScriptElement>('script[data-vastora-turnstile="true"]');
    const script = existing ?? document.createElement("script");
    const fail = (error: Error) => { script.remove(); reject(error); };
    const finish = () => window.turnstile ? resolve(window.turnstile) : fail(new Error("Turnstile did not initialize"));
    script.addEventListener("load", finish, { once: true });
    script.addEventListener("error", () => fail(new Error("Turnstile could not be loaded")), { once: true });
    if (!existing) {
      script.src = "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";
      script.async = true;
      script.defer = true;
      script.dataset.vastoraTurnstile = "true";
      document.head.append(script);
    }
  });
  turnstileScript = pending.catch((error) => {
    turnstileScript = null;
    throw error;
  });
  return turnstileScript;
}

export function Turnstile({ language, resetKey, siteKey, onError, onToken }: { language: Language; resetKey: number; siteKey: string; onError: () => void; onToken: (token: string) => void }) {
  const container = useRef<HTMLDivElement>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    let widgetId = "";
    setLoading(true);
    onToken("");
    void loadTurnstile().then((turnstile) => {
      if (cancelled || !container.current) return;
      widgetId = turnstile.render(container.current, {
        sitekey: siteKey,
        action: "center_login",
        appearance: "always",
        theme: "auto",
        callback: (token: string) => { setLoading(false); onToken(token); },
        "expired-callback": () => { onToken(""); },
        "error-callback": () => { setLoading(false); onToken(""); onError(); }
      });
    }).catch(() => {
      if (!cancelled) { setLoading(false); onError(); }
    });
    return () => {
      cancelled = true;
      if (widgetId && window.turnstile) window.turnstile.remove(widgetId);
    };
  }, [onError, onToken, resetKey, siteKey]);

  return <div className="min-h-[70px] w-full"><div aria-label={copy(language, "安全验证", "Security check")} id="center-login-turnstile" ref={container} />{loading ? <span aria-live="polite" className="flex items-center gap-2 text-xs text-muted-foreground"><Spinner />{copy(language, "正在加载安全验证…", "Loading security check…")}</span> : null}</div>;
}
