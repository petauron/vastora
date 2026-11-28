import type { Language } from "../../translations";
import type { AppView, Application } from "../../types";
import { copy } from "../shared";

const officialPlatformCapabilities = new Set([
  "shared-caddy",
  "cpa-tunnel-identity",
  "host-tcp-budget",
  "meridian-runtime",
]);

export function capabilityLabel(capability: string, language: Language) {
  const labels: Record<string, [string, string]> = {
    root: ["以 root 用户运行", "Run as root"],
    "native-services": ["管理宿主机原生服务", "Manage native host services"],
    "host-network": ["使用宿主机网络", "Use host networking"],
    "host-tcp-budget": ["按内存档位应用或恢复主机 TCP 总预算", "Apply or restore the host TCP memory budget"],
    "shared-caddy": ["使用共享网站与网关服务", "Use the shared website and gateway service"],
    "host-path": ["挂载声明的宿主机路径", "Mount declared host paths"],
    devices: ["访问声明的宿主机设备", "Access declared host devices"],
  };
  const label = labels[capability];
  return label ? copy(language, ...label) : capability;
}

export function isBuiltInPlatformCapability(appKey: string, capability: string): boolean {
  if (capability === "native-services") return true;
  return appKey.startsWith("vastora-official/") && officialPlatformCapabilities.has(capability);
}

export function installApprovalCapabilities(app: AppView): string[] {
  if (app.approvalRequiredCapabilities !== undefined) {
    return app.approvalRequiredCapabilities;
  }
  return (app.app.runtime?.requiredCapabilities ?? []).filter(
    (capability) => !isBuiltInPlatformCapability(app.key, capability),
  );
}

export function upgradeApprovalCapabilities(app: AppView, application?: Application): string[] {
  if (application?.upgradeApprovalCapabilities !== undefined) {
    return application.upgradeApprovalCapabilities;
  }
  const required = installApprovalCapabilities(app);
  if (!application?.authorizedCapabilities) {
    return required;
  }
  const granted = new Set(application.authorizedCapabilities);
  return required.filter((capability) => !granted.has(capability));
}

export function upgradeApprovalPermissionDetails(app: AppView, application?: Application): string[] {
  const required = upgradeApprovalCapabilities(app, application);
  if (required.length === 0) return [];
  if (application?.upgradePermissionDetails !== undefined) {
    return application.upgradePermissionDetails;
  }
  return app.permissionDetails ?? [];
}
