import { Suspense } from "react";
import { appWorkspace } from "./registry";
import type { AppManagerProps } from "./types";
import { Spinner } from "@/components/ui/spinner";

export function AppManager({ moduleKey, ...props }: AppManagerProps & { moduleKey: string }) {
  const module = props.application ? appWorkspace(moduleKey) : undefined;
  if (!module) return null;
  return <Suspense fallback={<Spinner />}><module.Manager {...props} /></Suspense>;
}
