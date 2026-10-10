import type { ReactNode } from "react";
import { Globe2Icon } from "lucide-react";
import { ThemeToggle } from "@/components/theme";
import { Button } from "@/components/ui/button";
import type { Language } from "@/translations";
import { copy } from "@/views/shared";

export function AuthShell({ children, title, description, language, onLanguage }: {
  children: ReactNode;
  title: string;
  description?: string;
  language: Language;
  onLanguage?: (language: Language) => void;
}) {
  return <div className="auth-shell">
    <header className="auth-header">
      <div className="auth-brand"><span className="desktop-brand-mark" aria-hidden="true"><i /><i /><i /><i /></span><span>Vastora</span></div>
      {onLanguage ? <nav className="auth-toolbar" aria-label={copy(language, "登录页设置", "Sign-in preferences")}>
        <ThemeToggle language={language} />
        <Button aria-label={copy(language, "切换语言", "Change language")} title={copy(language, "切换语言", "Change language")} onClick={() => onLanguage(language === "zh-CN" ? "en" : "zh-CN")} size="icon" type="button" variant="ghost"><Globe2Icon aria-hidden="true" /></Button>
      </nav> : null}
    </header>
    <main className="auth-main">
      <section className="auth-content" aria-labelledby="auth-title">
        <div className="auth-avatar" aria-hidden="true"><svg viewBox="0 0 80 80"><circle cx="40" cy="25" r="15" fill="currentColor" /><path d="M13 69c0-14.9 12.1-27 27-27s27 12.1 27 27c0 3.3-2.7 5-6 5H19c-3.3 0-6-1.7-6-5Z" fill="currentColor" /></svg></div>
        <div className="auth-heading"><h1 id="auth-title">{title}</h1>{description ? <p>{description}</p> : null}</div>
        {children}
      </section>
    </main>
  </div>;
}
