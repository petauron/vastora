// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { emptyAppData } from "../app-data";
import { ThemeProvider } from "../components/theme";
import type { AgentView } from "../types";
import { NodesView } from "./NodesView";
import { RuntimeRecoveryAlert } from "./RuntimeRecoveryAlert";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

const agentFixture = (): AgentView => ({
  id: "agent-a", name: "Node A", version: "test", operatingSystem: "linux", architecture: "amd64",
  status: "active", appliedInstallations: 1, enrolledAt: "2026-09-12T00:00:00Z", lastSeenAt: "2026-09-12T00:00:00Z",
  connected: true, credentialRevoked: false, siteId: "site-a", roles: ["worker"],
  capabilities: { docker: true, gateway: false, tunnel: false, metrics: false, logs: false },
  networkCandidates: [], gatewayHealthy: false, remoteUpdateSupported: true, runtimeRecovery: "application",
  runtimeRecoveryApplications: [
    { appKey: "vastora-official/cpa", applicationId: "app-a", reason: "image_unavailable" },
    { appKey: "vastora-official/keeper", applicationId: "app-b", reason: "health_check_failed" },
    { appKey: "vastora-official/3x-ui", reason: "state_incomplete" }
  ]
});

it("shows affected applications and repair guidance even while the node is connected", () => {
  const agent = agentFixture();
  const data = emptyAppData({ version: "test", agentInstallerAvailable: false, agentConnectionMode: "lan", agentConnectUrl: "https://center.example" });
  data.agents = [agent];
  data.sites = [{ id: "site-a", organizationId: "org-a", name: "Test site", code: "test", description: "", timezone: "UTC", domainSuffix: "site.example", status: "active", gatewayNodes: [], gatewayStatus: "ready", createdAt: "2026-09-12T00:00:00Z", updatedAt: "2026-09-12T00:00:00Z" }];
  const navigate = vi.fn();
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root?.render(<ThemeProvider><NodesView data={data} language="zh-CN" mutate={async () => {}} onNavigate={navigate} /></ThemeProvider>));
  expect(container.textContent).toContain("应用恢复受阻");
  expect(container.textContent).toContain("节点管理连接正常");
  expect(container.textContent).toContain("vastora-official/cpa");
  expect(container.textContent).toContain("离线恢复所需镜像不可用");
  expect(container.textContent).toContain("应用健康检查未通过");
  expect(container.textContent).toContain("本地安装状态不完整");
  expect(container.textContent).toContain("所有权或本地状态无法确认时仍需人工处理");
  const apps = [...container.querySelectorAll("button")].find((button) => button.textContent === "查看应用");
  expect(apps).toBeDefined();
  act(() => apps?.click());
  expect(navigate).toHaveBeenCalledExactlyOnceWith("apps");

  data.agents = [{ ...agent, runtimeRecovery: undefined, runtimeRecoveryApplications: undefined }];
  act(() => root?.render(<ThemeProvider><NodesView data={data} language="zh-CN" mutate={async () => {}} onNavigate={navigate} /></ThemeProvider>));
  expect(container.textContent).not.toContain("应用恢复受阻");
  expect(container.textContent).not.toContain("离线恢复所需镜像不可用");
});

it("keeps landing recovery separate from application repairs and explains the operator boundary", () => {
  const agent = { ...agentFixture(), runtimeRecovery: "landing" as const };
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root?.render(<RuntimeRecoveryAlert agent={agent} language="en" onApplications={vi.fn()} />));
  expect(container.textContent).toContain("landing configuration");
  expect(container.textContent).toContain("operator intervention");
  expect(container.textContent).not.toContain("vastora-official/cpa");
  expect(container.querySelector("button")).toBeNull();
});

it("does not present the last heartbeat's recovery details as live while disconnected", () => {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root?.render(<RuntimeRecoveryAlert agent={{ ...agentFixture(), connected: false }} language="en" onApplications={vi.fn()} />));
  expect(container.textContent).toBe("");
});

it.each(["gateway", "application"] as const)("explains the manual recovery boundary for %s without an identified repair", (stage) => {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  const agent = { ...agentFixture(), runtimeRecovery: stage, runtimeRecoveryApplications: undefined };
  act(() => root?.render(<RuntimeRecoveryAlert agent={agent} language="en" onApplications={vi.fn()} />));
  expect(container.textContent).toContain("operator");
  expect(container.querySelector("button")).toBeNull();
});
