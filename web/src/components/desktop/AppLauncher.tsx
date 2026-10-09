import { useState } from "react";
import { ArrowUpRightIcon, SearchIcon } from "lucide-react";
import type { Screen } from "@/types";
import type { Language } from "@/translations";
import type { DesktopApplication } from "@/views/applicationLaunch";
import { copy } from "@/views/shared";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { AppIcon } from "./AppIcon";
import { systemApplications } from "./navigation";

export function AppLauncher({ apps, language, open, onOpenChange, onNavigate, onOpenApp }: {
  apps: DesktopApplication[];
  language: Language;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onNavigate: (screen: Screen) => void;
  onOpenApp: (key: string) => void;
}) {
  const [query, setQuery] = useState("");
  const search = query.trim().toLocaleLowerCase();
  const system = systemApplications.filter((app) => copy(language, app.zh, app.en).toLocaleLowerCase().includes(search));
  const installed = apps.filter((app) => app.name.toLocaleLowerCase().includes(search));
  const close = () => { onOpenChange(false); setQuery(""); };
  return <Sheet open={open} onOpenChange={(next) => { onOpenChange(next); if (!next) setQuery(""); }}>
    <SheetContent className="desktop-launcher-popup" side="top">
      <SheetTitle className="sr-only">{copy(language, "搜索应用", "Search applications")}</SheetTitle>
      <SheetDescription className="sr-only">{copy(language, "查找并打开系统功能与已安装应用。", "Find and open system tools and installed apps.")}</SheetDescription>
      <InputGroup className="desktop-launcher-search"><InputGroupAddon><SearchIcon /></InputGroupAddon><InputGroupInput autoFocus aria-label={copy(language, "搜索应用", "Search applications")} placeholder={copy(language, "搜索应用…", "Search applications…")} type="search" value={query} onChange={(event) => setQuery(event.target.value)} /></InputGroup>
      <div className="desktop-launcher-results">
        {system.length ? <section><h2>{copy(language, "系统", "System")}</h2>{system.map((app) => <button className="desktop-launcher-result" key={app.id} onClick={() => { close(); onNavigate(app.id); }}><AppIcon appKey={app.id} className="size-9" /><span>{copy(language, app.zh, app.en)}</span></button>)}</section> : null}
        {installed.length ? <section><h2>{copy(language, "应用", "Applications")}</h2>{installed.map((app) => {
          const content = <><AppIcon appKey={app.key} className="size-9" /><span>{app.name}</span>{app.url ? <ArrowUpRightIcon className="ml-auto size-4 text-muted-foreground" /> : null}</>;
          return app.url ? <a className="desktop-launcher-result" key={app.key} href={app.url} target="_blank" rel="noreferrer" onClick={close} aria-label={copy(language, `在新标签页打开 ${app.name}`, `Open ${app.name} in a new tab`)}>{content}</a> : <button className="desktop-launcher-result" key={app.key} onClick={() => { close(); onOpenApp(app.key); }}>{content}</button>;
        })}</section> : null}
        {!system.length && !installed.length ? <p className="p-6 text-center text-sm text-muted-foreground">{copy(language, "没有匹配的应用", "No matching applications")}</p> : null}
      </div>
    </SheetContent>
  </Sheet>;
}
