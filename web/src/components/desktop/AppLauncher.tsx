import { useRef, useState, type KeyboardEvent } from "react";
import { ArrowUpRightIcon, SearchIcon } from "lucide-react";
import type { Screen } from "@/types";
import type { Language } from "@/translations";
import type { DesktopApplication } from "@/views/applicationLaunch";
import { copy } from "@/views/shared";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { AppIcon } from "./AppIcon";
import { systemApplications } from "./navigation";

export function AppLauncher({ apps, language, open, mode, onOpenChange, onNavigate, onOpenApp }: {
  apps: DesktopApplication[];
  language: Language;
  open: boolean;
  mode: "all" | "search";
  onOpenChange: (open: boolean) => void;
  onNavigate: (screen: Screen) => void;
  onOpenApp: (key: string) => void;
}) {
  const [query, setQuery] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const resultsRef = useRef<HTMLDivElement>(null);
  const search = query.trim().toLocaleLowerCase();
  const system = systemApplications.filter((app) => [app.id, app.zh, app.en].some((text) => text.toLocaleLowerCase().includes(search)));
  const installed = apps.filter((app) => [app.name, app.key].some((text) => text.toLocaleLowerCase().includes(search)));
  const close = () => { onOpenChange(false); setQuery(""); };
  const iconSize = mode === "all" ? "size-12" : "size-9";
  // Results keep native button/link semantics and Tab navigation. The search
  // list also moves real focus with arrows, so Enter activates the focused link.
  const onKeyDown = (event: KeyboardEvent<HTMLElement>) => {
    if (mode !== "search" || event.nativeEvent.isComposing || event.altKey || event.metaKey || event.ctrlKey) return;
    const results = [...(resultsRef.current?.querySelectorAll<HTMLElement>("[data-launcher-result]") ?? [])];
    if (!results.length) return;
    const fromSearch = event.target === inputRef.current;
    const current = results.indexOf(event.target as HTMLElement);
    if (event.key === "Enter" && fromSearch) { event.preventDefault(); results[0].click(); }
    else if (event.key === "ArrowDown" && (fromSearch || current >= 0)) { event.preventDefault(); results[Math.min(current + 1, results.length - 1)].focus(); }
    else if (event.key === "ArrowUp" && current >= 0) { event.preventDefault(); (results[current - 1] ?? inputRef.current)?.focus(); }
  };
  return <Sheet open={open} onOpenChange={(next) => { onOpenChange(next); if (!next) setQuery(""); }}>
    <SheetContent className="desktop-launcher-popup" data-mode={mode} side="top" onKeyDown={onKeyDown}>
      <SheetTitle className={mode === "all" ? "px-6 pt-5" : "sr-only"}>{mode === "all" ? copy(language, "所有应用", "All applications") : copy(language, "搜索应用", "Search applications")}</SheetTitle>
      <SheetDescription className="sr-only">{copy(language, "查找并打开系统功能与已安装应用。后台服务请到应用商店管理。", "Find and open system tools and installed apps. Manage background services in App Store.")}</SheetDescription>
      <InputGroup className="desktop-launcher-search"><InputGroupAddon><SearchIcon /></InputGroupAddon><InputGroupInput ref={inputRef} autoFocus aria-label={copy(language, "搜索应用", "Search applications")} placeholder={copy(language, "搜索应用…", "Search applications…")} type="search" value={query} onChange={(event) => setQuery(event.target.value)} /></InputGroup>
      <div className="desktop-launcher-results" ref={resultsRef}>
        {system.length ? <section><h2>{copy(language, "系统", "System")}</h2><div className="desktop-launcher-items">{system.map((app) => <button className="desktop-launcher-result" data-launcher-result key={app.id} onClick={() => { close(); onNavigate(app.id); }}><AppIcon appKey={app.id} className={iconSize} /><span>{copy(language, app.zh, app.en)}</span></button>)}</div></section> : null}
        {installed.length ? <section><h2>{copy(language, "应用", "Applications")}</h2><div className="desktop-launcher-items">{installed.map((app) => {
          const content = <><AppIcon appKey={app.key} className={iconSize} /><span>{app.name}{app.url ? <ArrowUpRightIcon aria-hidden="true" className="ml-1 inline size-3.5 text-muted-foreground" /> : null}</span>{mode === "search" && app.count > 1 ? <small className="ml-auto shrink-0 text-muted-foreground">{copy(language, `${app.count} 台主机`, `${app.count} hosts`)}</small> : null}</>;
          return app.url ? <a className="desktop-launcher-result" data-launcher-result key={app.key} href={app.url} target="_blank" rel="noreferrer" onClick={close} aria-label={copy(language, `在新标签页打开 ${app.name}`, `Open ${app.name} in a new tab`)}>{content}</a> : <button className="desktop-launcher-result" data-launcher-result key={app.key} onClick={() => { close(); onOpenApp(app.key); }}>{content}</button>;
        })}</div></section> : null}
        {!system.length && !installed.length ? <p role="status" className="p-6 text-center text-sm text-muted-foreground">{copy(language, "没有匹配的应用", "No matching applications")}</p> : null}
      </div>
      <footer className="desktop-launcher-footer"><span>{mode === "search" ? copy(language, "↑↓ 选择 · Enter 打开", "↑↓ Select · Enter Open") : copy(language, "后台服务在应用商店管理", "Manage background services in App Store")}</span><Button size="sm" variant="ghost" onClick={() => { close(); onNavigate("apps"); }}>{copy(language, "应用商店", "App Store")}</Button></footer>
    </SheetContent>
  </Sheet>;
}
