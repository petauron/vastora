import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { MoonIcon, SunIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import type { Language } from "@/translations";

type Theme = "light" | "dark";

const themeStorageKey = "vastora.theme";
const ThemeContext = createContext<{ theme: Theme; toggleTheme: () => void } | null>(null);

function storedTheme(): Theme | null {
  try {
    const value = window.localStorage.getItem(themeStorageKey);
    return value === "light" || value === "dark" ? value : null;
  } catch {
    return null;
  }
}

function systemTheme(): Theme {
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function preferredTheme(): Theme {
  return storedTheme() ?? systemTheme();
}

function applyTheme(theme: Theme) {
  document.documentElement.classList.toggle("dark", theme === "dark");
  document.documentElement.style.colorScheme = theme;
}

export function initializeTheme() {
  applyTheme(preferredTheme());
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<Theme>(preferredTheme);

  useEffect(() => {
    applyTheme(theme);
  }, [theme]);

  useEffect(() => {
    if (storedTheme()) return;
    const media = window.matchMedia?.("(prefers-color-scheme: dark)");
    if (!media) return;
    const followSystem = () => {
      if (!storedTheme()) setTheme(media.matches ? "dark" : "light");
    };
    media.addEventListener("change", followSystem);
    return () => media.removeEventListener("change", followSystem);
  }, []);

  const toggleTheme = useCallback(() => {
    setTheme((current) => {
      const next = current === "dark" ? "light" : "dark";
      try { window.localStorage.setItem(themeStorageKey, next); } catch { /* The visible theme still changes when storage is unavailable. */ }
      applyTheme(next);
      return next;
    });
  }, []);

  const value = useMemo(() => ({ theme, toggleTheme }), [theme, toggleTheme]);
  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function ThemeToggle({ language }: { language: Language }) {
  const context = useContext(ThemeContext);
  if (!context) throw new Error("ThemeToggle must be used inside ThemeProvider");
  const dark = context.theme === "dark";
  const label = dark ? (language === "zh-CN" ? "切换到浅色模式" : "Switch to light mode") : (language === "zh-CN" ? "切换到深色模式" : "Switch to dark mode");
  return <Button aria-label={label} aria-pressed={dark} onClick={context.toggleTheme} size="icon" title={label} variant="ghost">{dark ? <SunIcon /> : <MoonIcon />}</Button>;
}
