import { useEffect, useRef, useState } from "react";
import { api } from "../api";
import type { AgentReinstallPlan, ReinstallCurrentMaterial, ReinstallCurrentVerification } from "../types";
import type { Language } from "../translations";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { copy, userError } from "./shared";

type App = AgentReinstallPlan["applications"][number];
const supported = new Set(["vastora-official/glivela-entry", "vastora-official/senlun-agent", "vastora-official/komari-agent", "vastora-official/daylight"]);

export function ReinstallCurrentApplications({ plan, busy, language, onChanged, onBusy }: { plan: AgentReinstallPlan; busy: boolean; language: Language; onChanged: () => void; onBusy: (value: boolean) => void }) {
  const apps = plan.applications.filter((app) => supported.has(app.appKey) && !["keep_stopped", "keep_retired", "reenroll_monitor"].includes(app.recovery));
  if (!apps.length) return null;
  return <section className="flex flex-col gap-3 rounded-xl border p-4" aria-label={copy(language, "恢复现有应用", "Restore existing applications")}>
    <h3 className="text-sm font-medium">{copy(language, "恢复现有应用", "Restore existing applications")}</h3>
    <p className="text-xs text-muted-foreground">{copy(language, "探针先在原服务轮换凭据；静态站点使用原配置与签名包。保留原节点、账号和订阅；安装后仍须验证实际业务。", "Rotate collector credentials on the original service; static sites use their original settings and signed package. Preserve original identities and verify real business access after installation.")}</p>
    {apps.map((app) => <CurrentApplication key={`${app.applicationId}:${plan.revision}`} app={app} plan={plan} busy={busy} language={language} onChanged={onChanged} onBusy={onBusy} />)}
  </section>;
}

function CurrentApplication({ app, plan, busy, language, onChanged, onBusy }: { app: App; plan: AgentReinstallPlan; busy: boolean; language: Language; onChanged: () => void; onBusy: (value: boolean) => void }) {
  const [file, setFile] = useState<File | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const [error, setError] = useState("");
  const active = useRef(true);
  const submitting = useRef(false);
  const fileInput = useRef<HTMLInputElement>(null);
  useEffect(() => { active.current = true; return () => { active.current = false; }; }, []);
  const website = app.appKey === "vastora-official/daylight";
  const restoration = app.currentRestoration;
  const verification = restoration?.state === "succeeded";
  const allowed = !restoration || verification;
  const submit = async () => {
    if (!file || !confirmed || busy || submitting.current || !allowed || !plan.recovery || !plan.networkReview?.approvalCurrent) return;
    submitting.current = true; onBusy(true); setError("");
    try {
      if (file.size > 256 * 1024) throw new Error(copy(language, "恢复材料不得超过 256 KiB", "Recovery material must not exceed 256 KiB"));
      let value: unknown;
      try { value = JSON.parse(await file.text()); } catch { throw new Error(copy(language, "请选择有效的 JSON 恢复材料", "Select valid JSON recovery material")); }
      if (!active.current) return;
      if (!isRecord(value) || value.applicationId !== app.applicationId) throw new Error(copy(language, "材料与当前应用不匹配", "Material does not match this application"));
      const identity = { operationId: plan.recovery.id, planRevision: plan.revision, applicationId: app.applicationId };
      if (verification) {
        const keys = ["originalIdentity", "evidenceSha256", "rotationReference", "businessReference", "verifiedAt"] as const;
        if (!keys.every((key) => typeof value[key] === "string" && value[key] !== "")) throw new Error(copy(language, "验收记录缺少原身份、证据摘要、轮换记录或验证时间", "Verification requires original identity, evidence digest, rotation reference and verification time"));
        const receipt = Object.fromEntries(keys.map((key) => [key, value[key]])) as Omit<ReinstallCurrentVerification, "applicationId">;
        await api.verifyCurrentApplication(plan.agentId, { ...identity, ...receipt });
      } else {
        if (!isRecord(value.config)) throw new Error(copy(language, "恢复材料缺少完整应用配置", "Complete application configuration is required"));
        const material: ReinstallCurrentMaterial = { applicationId: app.applicationId, config: value.config };
        if (value.senulunRestore !== undefined) {
          if (!isRecord(value.senulunRestore) || typeof value.senulunRestore.nodeId !== "string" || typeof value.senulunRestore.token !== "string") throw new Error(copy(language, "原监控节点和新凭据格式不正确", "Invalid original monitoring identity or new credential"));
          material.senulunRestore = { nodeId: value.senulunRestore.nodeId, token: value.senulunRestore.token };
        }
        await api.restoreCurrentApplication(plan.agentId, { ...material, ...identity });
      }
      if (!active.current) return;
      setFile(null); setConfirmed(false);
      if (fileInput.current) fileInput.current.value = "";
      onChanged();
    } catch (cause) {
      if (active.current) setError(userError(language, cause));
    } finally {
      submitting.current = false;
      if (active.current) onBusy(false);
    }
  };
  const status = !restoration ? copy(language, "待恢复", "Awaiting restoration") : restoration.state === "succeeded" ? copy(language, "安装成功，仍需业务验收", "Installed; business verification required") : ["pending", "running"].includes(restoration.state) ? copy(language, "任务处理中，请刷新状态，不要重复安装", "Task in progress; refresh status without reinstalling") : copy(language, "结果需核对，请勿重复安装", "Inspect the outcome before any further installation");
  return <div className="flex flex-col gap-2 border-t pt-3">
    <p className="text-sm font-medium">{app.name}</p>
    <p className="text-xs text-muted-foreground" role="status">{restoration?.verification === "operator_attested" ? copy(language, "管理员已记录业务验收；完成恢复时会重新检查有效期", "Operator verification recorded; freshness is checked at completion") : status}</p>
    {allowed ? <>
      <label className="flex flex-col gap-1 text-sm">{copy(language, `${app.name} · ${verification ? "业务验收记录" : "恢复材料"}`, `${app.name} · ${verification ? "Business verification" : "Recovery material"}`)}
        <Input ref={fileInput} type="file" accept=".json,application/json" disabled={busy} onChange={(event) => { setFile(event.target.files?.[0] ?? null); setConfirmed(false); setError(""); }} />
      </label>
      <p className="text-xs text-muted-foreground">{verification ? copy(language, "仅导入实际完成后的验收记录。记录需包含原身份、凭据轮换记录、访问或上报证据摘要及真实验证时间；系统不会代替人工业务验收。", "Import only completed verification, including original identity, rotation reference, evidence digest and actual time. This records operator verification, not an automated business check.") : website ? copy(language, "导入原站点配置；仅从已签名的原包重建页面，证书由 Center 管理，恢复后仍须验证域名与 HTTPS。", "Import original site settings. Rebuild only the signed original page; Center manages certificates. Verify the domain and HTTPS after restoration.") : copy(language, "导入原应用配置及已轮换的新凭据。材料仅用于本次请求，不保存到浏览器；不要导入旧凭据。", "Import original application settings and newly rotated credentials. Material is used only for this request and is not persisted in the browser. Do not import old credentials.")}</p>
      <label className="flex items-start gap-2 text-sm"><Checkbox checked={confirmed} disabled={busy} onCheckedChange={setConfirmed} />{verification ? copy(language, "已核对原业务身份，并完成实际访问或新数据上报验证", "Original business identity and real access or fresh reporting have been verified") : website ? copy(language, "已核对原站点配置、签名包和 Center 证书恢复范围", "Original site settings, signed package and Center certificate recovery scope reviewed") : copy(language, "已核对原应用配置，旧凭据已撤销，材料使用新凭据", "Original configuration reviewed; old credentials revoked and new credentials supplied")}</label>
      <Button className="self-start" variant="outline" disabled={busy || !file || !confirmed || !plan.networkReview?.approvalCurrent} onClick={() => void submit()}>{copy(language, verification ? `记录 ${app.name} 验收` : `恢复 ${app.name}`, verification ? `Record ${app.name} verification` : `Restore ${app.name}`)}</Button>
    </> : null}
    {error ? <Alert variant="destructive"><AlertDescription>{error}</AlertDescription></Alert> : null}
  </div>;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
