import type { AppUIManifest } from "@/app-workspaces/types";

// Application-owned presentation contract. This is bundled reviewed code;
// it does not modify an immutable, signed runtime package manifest.
export const manifest = {
  appKey: "vastora-official/meridian",
  apiVersion: 1,
  pages: [
    { id: "nodes", title: { "zh-CN": "节点概览", en: "Nodes" }, surface: "tab", capabilities: ["ip-quality", "landing"] },
    { id: "network", title: { "zh-CN": "线路测速", en: "Link tests" }, surface: "tab", capabilities: ["landing"] },
    { id: "accounts", title: { "zh-CN": "账号与订阅", en: "Accounts & subscriptions" }, surface: "manager", capabilities: ["accounts"] },
  ],
} as const satisfies AppUIManifest;
