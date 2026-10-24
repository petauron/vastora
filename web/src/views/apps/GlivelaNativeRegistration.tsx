import { useState, type FormEvent } from "react";
import { api } from "../../api";
import type { Mutate } from "../../App";
import type { Language } from "../../translations";
import { copy, userError } from "../shared";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Field, FieldLabel, FieldDescription, FieldError } from "@/components/ui/field";

export function GlivelaNativeRegistration({ applicationId, language, mutate, disabled }: {
  applicationId: string; language: Language; mutate: Mutate; disabled: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [region, setRegion] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy || disabled) return;
    setBusy(true); setError("");
    try {
      await mutate(async () => {
        await api.registerGlivelaNativeEntry({ applicationId, name: name.trim(), regionCode: region.trim().toUpperCase() });
      }, undefined, { reportError: false });
    } catch (failure) { setError(userError(language, failure)); }
    finally { setBusy(false); }
  };
  if (!open) return <Button disabled={disabled} size="sm" variant="outline" onClick={() => setOpen(true)}>{copy(language, "登记原生入口", "Register native entry")}</Button>;
  return <form className="space-y-3" onSubmit={(event) => void submit(event)}>
    <FieldDescription>{copy(language, "先在 Glivela 完成新入口配置并核验运行。登记会读取真实回执，加入现有订阅；公网验证通过后才会显示节点。", "Configure and verify the new entry in Glivela first. Registration reads its authenticated receipt and adds it to the existing subscription after public verification.")}</FieldDescription>
    <Field><FieldLabel htmlFor="native-entry-name">{copy(language, "节点名称", "Node name")}</FieldLabel><Input id="native-entry-name" value={name} onChange={(event) => setName(event.target.value)} required maxLength={80} disabled={busy || disabled} /></Field>
    <Field><FieldLabel htmlFor="native-entry-region">{copy(language, "地区代码（如 US）", "Region code (for example US)")}</FieldLabel><Input id="native-entry-region" value={region} onChange={(event) => setRegion(event.target.value)} required minLength={2} maxLength={2} disabled={busy || disabled} /></Field>
    {error ? <FieldError>{error}</FieldError> : null}
    <Button type="submit" size="sm" disabled={busy || disabled}>{copy(language, busy ? "核验中…" : "核验并登记", busy ? "Verifying…" : "Verify and register")}</Button>
  </form>;
}
