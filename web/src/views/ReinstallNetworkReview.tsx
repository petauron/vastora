import { useState } from "react";
import type { AgentReinstallNetworkReview, NetworkKind, NetworkProfile } from "../types";
import type { Language } from "../translations";
import { copy, formatDate } from "./shared";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { SelectControl } from "@/components/SelectControl";

function initialProfile(review: AgentReinstallNetworkReview): NetworkProfile {
  const previous = review.previous;
  const choose = (kind: NetworkKind, saved?: string) => {
    const candidates = review.candidates.filter((candidate) => candidate.kind === kind);
    if (candidates.some((candidate) => candidate.address === saved)) return saved!;
    return candidates.length === 1 ? candidates[0].address : "";
  };
  const lanAddress = choose("lan", previous?.lanAddress);
  const headscaleAddress = choose("headscale", previous?.headscaleAddress);
  const serviceAddress = previous?.serviceAddress === "127.0.0.1" ? "127.0.0.1"
    : previous?.headscaleAddress && previous.serviceAddress === previous.headscaleAddress ? headscaleAddress
    : previous?.lanAddress && previous.serviceAddress === previous.lanAddress ? lanAddress
    : headscaleAddress || lanAddress;
  return { serviceAddress, lanAddress, headscaleAddress, enabledKinds: [], directPublic: previous?.directPublic ?? false };
}

export function ReinstallNetworkReview({ review, busy, language, onApprove }: {
  review: AgentReinstallNetworkReview; busy: boolean; language: Language; onApprove: (profile: NetworkProfile) => Promise<void>;
}) {
  const [draft, setDraft] = useState(() => initialProfile(review));
  const [editing, setEditing] = useState(!review.approval || !review.approvalCurrent);
  const previous = review.previous;
  const approval = review.approval;
  const enabledKinds: NetworkKind[] = [...new Set<NetworkKind>([
    ...(previous?.enabledKinds ?? []), ...(draft.lanAddress ? ["lan" as const] : []), ...(draft.headscaleAddress ? ["headscale" as const] : []),
  ])];
  const egress = review.publicEgress;
  const profile: NetworkProfile = { ...draft, enabledKinds, ...(draft.directPublic && egress ? { publicAddress: egress.address, publicBindAddress: egress.bindAddress, publicMode: egress.mode } : {}) };
  const incomplete = !review.ready || !draft.serviceAddress || enabledKinds.includes("lan") && !draft.lanAddress
    || enabledKinds.includes("headscale") && (!draft.headscaleAddress || review.privatePeer?.address !== draft.headscaleAddress)
    || draft.directPublic && !egress;
  const options = (kind: NetworkKind, required: boolean) => [
    { value: "", label: required ? copy(language, "选择新机器地址", "Choose a replacement address") : copy(language, "不启用", "Disabled"), disabled: required },
    ...review.candidates.filter((candidate) => candidate.kind === kind).map((candidate) => ({ value: candidate.address, label: `${candidate.address} · ${candidate.interface}` })),
  ];
  return <section aria-label={copy(language, "恢复地址", "Recovery addresses")} className="flex flex-col gap-3 rounded-xl border p-4">
    <h3 className="text-sm font-medium">{copy(language, "恢复地址", "Recovery addresses")}</h3>
    {approval && !review.approvalCurrent ? <p role="status" className="text-sm text-muted-foreground">{copy(language, "当前网络与已确认记录不符，需重新核对。", "Current network evidence differs from the saved approval. Review it again.")}</p> : null}
    {approval && !editing ? <>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
        <dt className="text-muted-foreground">{copy(language, "服务地址", "Service")}</dt><dd className="break-all text-right">{approval.previous?.serviceAddress || "—"} → {approval.profile.serviceAddress}</dd>
        {approval.profile.headscaleAddress ? <><dt className="text-muted-foreground">Headscale</dt><dd className="break-all text-right">{approval.previous?.headscaleAddress || "—"} → {approval.profile.headscaleAddress}</dd></> : null}
        {approval.profile.directPublic ? <><dt className="text-muted-foreground">{copy(language, "公网入口", "Public entry")}</dt><dd className="break-all text-right">{approval.previous?.publicAddress || "—"} → {approval.profile.publicAddress}</dd></> : null}
      </dl>
      <p className="text-xs text-muted-foreground">{copy(language, "已保存，待应用恢复时启用。", "Saved for activation during application restoration.")} {formatDate(language, approval.approvedAt)}</p>
      <Button className="self-end" disabled={busy || !review.ready} onClick={() => setEditing(true)} size="sm" variant="outline">{copy(language, "重新核对", "Review again")}</Button>
    </> : !review.ready ? <p className="text-sm text-muted-foreground">{copy(language, "等待新机器上报当前网络地址。", "Waiting for current network addresses from the replacement machine.")}</p> : <>
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor="reinstall-service-address">{copy(language, "服务地址", "Service address")}</FieldLabel>
          <SelectControl disabled={busy} id="reinstall-service-address" onValueChange={(serviceAddress) => setDraft((value) => ({ ...value, serviceAddress }))} options={[
            { value: "", label: copy(language, "选择服务地址", "Choose a service address"), disabled: true },
            ...review.candidates.filter((candidate) => candidate.kind !== "public").map((candidate) => ({ value: candidate.address, label: `${candidate.address} · ${candidate.interface}` })),
            ...(!previous || previous.serviceAddress === "127.0.0.1" ? [{ value: "127.0.0.1", label: "127.0.0.1" }] : []),
          ]} value={draft.serviceAddress} />
          <FieldDescription>{copy(language, "原地址", "Previous")}: {previous?.serviceAddress || "—"}</FieldDescription>
        </Field>
        {(["lan", "headscale"] as const).map((kind) => {
          const field = kind === "lan" ? "lanAddress" : "headscaleAddress";
          const required = previous?.enabledKinds.includes(kind) ?? false;
          if (!required && !review.candidates.some((candidate) => candidate.kind === kind)) return null;
          return <Field key={kind}>
            <FieldLabel htmlFor={`reinstall-${kind}-address`}>{kind === "lan" ? "LAN" : "Headscale"}</FieldLabel>
            <SelectControl disabled={busy} id={`reinstall-${kind}-address`} onValueChange={(address) => setDraft((value) => ({ ...value, [field]: address }))} options={options(kind, required)} value={draft[field] ?? ""} />
            <FieldDescription>{copy(language, "原地址", "Previous")}: {previous?.[field] || "—"}</FieldDescription>
          </Field>;
        })}
        {draft.directPublic ? <Field><FieldLabel>{copy(language, "公网入口", "Public entry")}</FieldLabel><p className="break-all text-sm">{previous?.publicAddress || "—"} → {egress?.address || copy(language, "等待检测", "Awaiting detection")}</p><FieldDescription>{copy(language, "本机接收地址", "Local receiving address")}: {egress?.bindAddress || "—"}</FieldDescription></Field> : null}
      </FieldGroup>
      <p className="text-xs text-muted-foreground">{copy(language, "确认后保存恢复地址，入口将在应用恢复后重新验证。", "Confirmation saves recovery addresses. Access entries will be verified after application restoration.")}</p>
      {enabledKinds.includes("headscale") && review.privatePeer?.address !== draft.headscaleAddress ? <p role="status" className="text-xs text-muted-foreground">{copy(language, "等待新机器上报所选私网地址的身份。", "Waiting for the replacement identity for the selected private address.")}</p> : null}
      <Button className="self-end" disabled={busy || incomplete} onClick={() => void onApprove(profile)} size="sm">{copy(language, "确认恢复地址", "Confirm recovery addresses")}</Button>
    </>}
  </section>;
}
