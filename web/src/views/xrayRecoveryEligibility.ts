import type { AppData } from "../App";

// Match Center's xrayWorkerApplicationID: native/retired Meridian runtimes
// must never enter the legacy Xray configuration recovery flow.
export function canInspectXrayConfiguration(data: Pick<AppData, "applications" | "meridian">, nodeId: string) {
  return data.applications.some((application) => {
    if (application.nodeId !== nodeId || !["running", "failed"].includes(application.status)) return false;
    if (application.appKey === "vastora-official/3x-ui") return application.role === "worker";
    return application.appKey === "vastora-official/meridian"
      && data.meridian.cutover.subscriptionAuthority === "meridian"
      && ["project", "verify", "retire"].includes(data.meridian.cutover.state)
      && data.meridian.endpoints.some((endpoint) => endpoint.applicationId === application.id && !endpoint.legacyRetired);
  });
}
