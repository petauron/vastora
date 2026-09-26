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
  const [mode, setMode] = useState(server.egressIp ? "fixed" : "auto");
  const [address, setAddress] = useState(server.egressIp ?? "");
  const candidates = server.egressAddresses ?? [];
  const value = mode === "auto" ? "" : address.trim();
  return <section aria-label={copy(language, "出口 IP", "Egress IP")} className="text-xs">
    <h4 className="font-medium">{copy(language, "出口 IP", "Egress IP")}</h4>
    <p className="mt-1 break-all text-muted-foreground">{server.egressIp || copy(language, "自动 IPv4", "Automatic IPv4")}</p>
    <form className="mt-2 flex flex-col gap-2" onSubmit={(event) => { event.preventDefault(); if (!disabled && server.egressSupported && (mode === "auto" || value)) void save(value); }}>
      <SelectControl aria-label={copy(language, `${server.name} 出口方式`, `${server.name} egress mode`)} value={mode} onValueChange={setMode} disabled={disabled || !server.egressSupported} options={[
        { value: "auto", label: copy(language, "自动 IPv4", "Automatic IPv4") },
        { value: "fixed", label: copy(language, "指定 IPv4 / IPv6", "Bind IPv4 / IPv6") },
      ]} />
      {mode === "fixed" ? <><label htmlFor={`${id}-candidate`}>{copy(language, "网卡地址", "Interface address")}</label><SelectControl id={`${id}-candidate`} aria-label={copy(language, `${server.name} 网卡地址`, `${server.name} interface address`)} value={candidates.some((candidate) => candidate.address === address) ? address : "manual"} onValueChange={(value) => setAddress(value === "manual" ? "" : value)} disabled={disabled || !server.egressSupported} options={[
        ...candidates.map((candidate) => ({ value: candidate.address, label: `${candidate.address.includes(":") ? "IPv6" : "IPv4"} · ${candidate.address} · ${candidate.interface}` })),
        { value: "manual", label: copy(language, "手动填写", "Enter manually") },
      ]} /><label htmlFor={id}>{copy(language, "服务器本机 IP", "Local server IP")}</label><Input id={id} value={address} onChange={(event) => setAddress(event.target.value)} required placeholder={copy(language, "填写网卡上的 IPv4 或 IPv6", "IPv4 or IPv6 assigned to the server")} disabled={disabled} autoComplete="off" spellCheck={false} /><p className="text-muted-foreground">{copy(language, "绑定此地址，不回退其他 IP。IPv6 出口仅能访问 IPv6 目标；NAT 主机填写网卡地址。", "Bind this address without fallback. IPv6 exits reach IPv6 destinations only; on NAT hosts enter the interface address.")}</p></> : null}
      {!server.egressSupported ? <p className="text-muted-foreground">{copy(language, "请先更新此节点的 Agent", "Update this node’s Agent first")}</p> : null}
      {server.egressError ? <p role="alert" className="break-words text-destructive">{server.egressError}</p> : null}
      <Button type="submit" size="sm" variant="outline" disabled={disabled || !server.egressSupported || value === (server.egressIp ?? "") || mode === "fixed" && !value}>{copy(language, "应用出口 IP", "Apply egress IP")}</Button>
    </form>
  </section>;
}
