import { useId, useState } from "react";
import type { LandingView } from "@/landing-types";
import type { Language } from "@/translations";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { SelectControl } from "@/components/SelectControl";
import { copy } from "@/views/shared";

type Server = LandingView["servers"][number];
export function LandingEgressIP({ server, language, disabled, save }: {
  server: Server; language: Language; disabled: boolean;
  save: (address: string) => Promise<boolean>;
}) {
  const id = useId();
  const [choice, setChoice] = useState(server.egressIp || "auto");
  const [manual, setManual] = useState("");
  const candidates = server.egressAddresses ?? [];
  const value = choice === "auto" ? "" : choice === "manual" ? manual.trim() : choice;
  return <section aria-label={copy(language, "出口 IP", "Egress IP")} className="min-w-0 text-xs">
    <h4 className="font-medium">{copy(language, "出口 IP", "Egress IP")}</h4>
    <form className="mt-3 flex flex-col gap-2" onSubmit={(event) => { event.preventDefault(); if (!disabled && server.egressSupported && (choice === "auto" || value)) void save(value); }}>
      <div className="flex items-center gap-2"><SelectControl aria-label={copy(language, `${server.name} 出口 IP`, `${server.name} egress IP`)} value={choice} onValueChange={setChoice} disabled={disabled || !server.egressSupported} options={[
        { value: "auto", label: copy(language, "自动 IPv4", "Automatic IPv4") },
        ...candidates.map((candidate) => ({ value: candidate.address, label: `${candidate.address.includes(":") ? "IPv6" : "IPv4"} · ${candidate.address}` })),
        ...(server.egressIp && !candidates.some((candidate) => candidate.address === server.egressIp) ? [{ value: server.egressIp, label: server.egressIp }] : []),
        { value: "manual", label: copy(language, "手动填写…", "Enter manually…") },
      ]} /><Button type="submit" size="sm" variant="outline" disabled={disabled || !server.egressSupported || value === (server.egressIp ?? "") || choice !== "auto" && !value}>{copy(language, "保存", "Save")}</Button></div>
      {choice === "manual" ? <><label htmlFor={id}>{copy(language, "服务器本机 IP", "Local server IP")}</label><Input id={id} value={manual} onChange={(event) => setManual(event.target.value)} required placeholder={copy(language, "IPv4 或 IPv6", "IPv4 or IPv6")} disabled={disabled} autoComplete="off" spellCheck={false} /></> : null}
      {value.includes(":") ? <p className="text-muted-foreground">{copy(language, "此出口仅支持 IPv6 目标。", "This exit reaches IPv6 destinations only.")}</p> : null}
      {!server.egressSupported ? <p className="text-muted-foreground">{copy(language, "请先更新此节点的 Agent", "Update this node’s Agent first")}</p> : null}
      {server.egressError ? <p role="alert" className="break-words text-destructive">{server.egressError}</p> : null}
    </form>
  </section>;
}
