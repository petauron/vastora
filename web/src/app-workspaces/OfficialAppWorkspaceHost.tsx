import { useEffect, useRef, useState } from "react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { copy } from "@/views/shared";
import { ApplicationStatus } from "@/views/apps/InstalledApplicationPrimitives";
import type { AppManagerProps, AppWorkspaceProps } from "./types";

type MountedUI<Props> = { update: (props: Props) => void; unmount: () => void };
type OfficialUIModule = {
  apiVersion: number;
  mount: (element: HTMLElement, props: AppWorkspaceProps) => MountedUI<AppWorkspaceProps>;
  mountManager: (element: HTMLElement, props: AppManagerProps) => MountedUI<AppManagerProps>;
};
type StyleLease = { link: HTMLLinkElement; ready: Promise<void>; references: number };

const styles = new Map<string, StyleLease>();

function acquireStyle(url: string) {
  const existing = styles.get(url);
  if (existing) {
    existing.references++;
    return existing;
  }
  const link = document.createElement("link");
  link.rel = "stylesheet";
  link.href = url;
  const ready = new Promise<void>((resolve, reject) => {
    link.onload = () => resolve();
    link.onerror = () => reject(new Error("Official application stylesheet failed to load"));
  });
  const lease = { link, ready, references: 1 };
  styles.set(url, lease);
  document.head.append(link);
  return lease;
}

function releaseStyle(url: string) {
  const lease = styles.get(url);
  if (!lease || --lease.references > 0) return;
  lease.link.remove();
  styles.delete(url);
}

function useOfficialMeridianUI(version: string | undefined) {
  const [module, setModule] = useState<OfficialUIModule | null>(null);
  const [state, setState] = useState<"loading" | "ready" | "failed">("loading");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    setModule(null);
    setState("loading");
    if (!version) {
      setState("failed");
      return;
    }
    let cancelled = false;
    const prefix = `/api/v1/official-app-ui/meridian/${encodeURIComponent(version)}`;
    const suffix = attempt ? `?attempt=${attempt}` : "";
    const styleURL = `${prefix}/bundle.css${suffix}`;
    const lease = acquireStyle(styleURL);
    void lease.ready.then(() => import(/* @vite-ignore */ `${prefix}/bundle.js${suffix}`)).then((loaded: unknown) => {
      if (cancelled) return;
      const candidate = loaded as Partial<OfficialUIModule>;
      if (candidate.apiVersion !== 1 || typeof candidate.mount !== "function" || typeof candidate.mountManager !== "function") {
        throw new Error("Unsupported Meridian UI contract");
      }
      setModule(candidate as OfficialUIModule);
      setState("ready");
    }).catch(() => { if (!cancelled) setState("failed"); });
    return () => {
      cancelled = true;
      releaseStyle(styleURL);
    };
  }, [version, attempt]);
  return { module, state, retry: () => setAttempt((value) => value + 1) };
}

function LoadingOrError({ state, retry, language, runtimeFailed }: { state: "loading" | "ready" | "failed"; retry: () => void; language: AppWorkspaceProps["language"]; runtimeFailed: boolean }) {
  if (state === "loading") return <Spinner />;
  if (state === "failed") return <Alert variant="destructive"><AlertTitle>{copy(language, "Meridian 界面未就绪", "Meridian interface unavailable")}</AlertTitle><AlertDescription>{runtimeFailed
    ? copy(language, "界面启动失败，请重试。", "The interface failed to start. Try again.")
    : copy(language, "请确认该版本的界面资源已发布并刷新官方应用目录。", "Confirm this version's interface was published and refresh the official catalog.")}
    <Button className="ml-2" onClick={retry} size="sm" variant="outline">{copy(language, "重试加载", "Retry")}</Button></AlertDescription></Alert>;
  return null;
}

export function OfficialAppWorkspaceHost(props: AppWorkspaceProps) {
  const target = useRef<HTMLDivElement>(null);
  const mounted = useRef<MountedUI<AppWorkspaceProps> | null>(null);
  const latest = useRef(props);
  const { module, state, retry } = useOfficialMeridianUI(props.group.app?.app.version);
  const [runtimeFailed, setRuntimeFailed] = useState(false);
  latest.current = props;
  useEffect(() => {
    if (!module || !target.current) return;
    setRuntimeFailed(false);
    try {
      const instance = module.mount(target.current, latest.current);
      if (typeof instance?.update !== "function" || typeof instance?.unmount !== "function") throw new Error("Invalid Meridian UI mount contract");
      mounted.current = instance;
    } catch {
      setRuntimeFailed(true);
    }
    return () => {
      try { mounted.current?.unmount(); } catch { /* An application failure must not break Center cleanup. */ }
      mounted.current = null;
    };
  }, [module]);
  useEffect(() => {
    try { mounted.current?.update(props); } catch {
      try { mounted.current?.unmount(); } catch { /* The interface is already failing. */ }
      mounted.current = null;
      setRuntimeFailed(true);
    }
  }, [props]);
  const unavailable = runtimeFailed || state === "failed";
  return <section aria-label="Meridian" data-app-workspace="vastora-official/meridian">
    <LoadingOrError state={runtimeFailed ? "failed" : state} retry={retry} language={props.language} runtimeFailed={runtimeFailed} />
    {unavailable ? <div aria-label={copy(props.language, "已安装的 Meridian 节点", "Installed Meridian nodes")} className="mt-4 max-h-72 divide-y overflow-y-auto rounded-lg border">
      {props.group.instances.map((instance) => <div className="flex flex-wrap items-center gap-3 px-3 py-2" key={instance.application.id}>
        <span className="min-w-32 flex-1 text-sm">{instance.agent?.name ?? instance.application.nodeId}</span>
        <ApplicationStatus instance={instance} language={props.language} onUpgrade={props.onUpgrade} />
        <Button onClick={() => props.onManage(instance.application)} size="sm" variant="outline">{copy(props.language, "管理安装", "Manage installation")}</Button>
      </div>)}
    </div> : null}
    <div ref={target} />
  </section>;
}

export function OfficialAppManagerHost(props: AppManagerProps & { version?: string }) {
  const target = useRef<HTMLDivElement>(null);
  const mounted = useRef<MountedUI<AppManagerProps> | null>(null);
  const latest = useRef(props);
  const { module, state, retry } = useOfficialMeridianUI(props.version);
  const [runtimeFailed, setRuntimeFailed] = useState(false);
  latest.current = props;
  useEffect(() => {
    if (!module || !target.current) return;
    setRuntimeFailed(false);
    try {
      const instance = module.mountManager(target.current, latest.current);
      if (typeof instance?.update !== "function" || typeof instance?.unmount !== "function") throw new Error("Invalid Meridian manager mount contract");
      mounted.current = instance;
    } catch {
      setRuntimeFailed(true);
    }
    return () => {
      try { mounted.current?.unmount(); } catch { /* An application failure must not break Center cleanup. */ }
      mounted.current = null;
    };
  }, [module]);
  useEffect(() => {
    try { mounted.current?.update(props); } catch {
      try { mounted.current?.unmount(); } catch { /* The interface is already failing. */ }
      mounted.current = null;
      setRuntimeFailed(true);
    }
  }, [props]);
  return <section aria-label="Meridian 管理"><LoadingOrError state={runtimeFailed ? "failed" : state} retry={retry} language={props.language} runtimeFailed={runtimeFailed} />{runtimeFailed || state === "failed" ? <Button className="mt-3" onClick={props.onClose} size="sm" variant="outline">{copy(props.language, "关闭", "Close")}</Button> : null}<div ref={target} /></section>;
}
