// @vitest-environment jsdom
import { act } from "react";
import { describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { AgentReinstallAccess, AgentReinstallDNS, AgentReinstallEntryCheck, AgentReinstallPlan } from "../types";
import { NodesView } from "./NodesView";
import { dashboard, render, rerender } from "./views.test-support";

const review = (): AgentReinstallPlan => ({
  agentId: "agent", revision: "a".repeat(64), checkedAt: "2026-10-01T00:00:00Z", identityFingerprint: "b".repeat(64), credentialRevoked: false,
  privateNetwork: { ownership: "managed", serviceAddress: "100.64.0.2", privateAddress: "100.64.0.2", profileRetained: false, addressRecovery: "explicit_migration_required", landingRoutes: 1, publications: 2 },
  applications: [{ applicationId: "app", name: "Meridian", appKey: "vastora-official/meridian", deploymentId: "deployment", version: "0.1.0-alpha.12", operation: "install", state: "succeeded", recovery: "rebuild_configuration", sharedEntry: false, requirements: [] }],
  pendingWork: [{ agentId: "agent", kind: "application.apply", count: 1 }], unclaimedLocalWork: [], executions: [], monitoring: [], requirements: [],
});
const command = { token: "replacement-token", siteId: "site", centerUrl: "https://center.example.com", installerUrl: "https://center.example.com", expiresAt: "2099-01-01T00:00:00Z" };
const recovery = { id: "replacement-operation", planRevision: "a".repeat(64), state: "awaiting_enrollment" as const, privateIsolation: "withdrawn" as const, attempt: 1, authorizedBy: "admin", previousFingerprint: "b".repeat(64), replacementFingerprint: "", lastError: "", createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-01T00:00:00Z" };
const button = (label: string) => [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.textContent === label);
const show = async () => {
  const data = dashboard(); data.agents[0].connected = false;
  const container = render(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
  await act(async () => container.querySelector<HTMLButtonElement>('[aria-label="重新接入 home-server"]')!.click());
  return { data, container };
};

describe("node reinstall review", () => {
  it("requests final server verification and keeps the sheet open on rejection", async () => {
    const plan = { ...review(), recovery: { ...recovery, state: "review_required" as const } };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    const complete = vi.spyOn(api, "completeAgentReinstall").mockRejectedValue(new Error("center: current authenticated client verification is incomplete"));
    await show();
    expect(complete).not.toHaveBeenCalled();
    await act(async () => button("校验并完成恢复")!.click());
    expect(complete).toHaveBeenCalledWith("agent", { operationId: recovery.id, planRevision: plan.revision });
    expect(button("校验并完成恢复")).toBeDefined();
    expect(document.body.textContent).toContain("真实客户端验收未通过或已过期");
  });

  it("shows server-reported remaining work without offering premature completion", async () => {
    const plan = { ...review(), recovery: { ...recovery, state: "review_required" as const }, remaining: [
      { code: "client_acceptance", applicationId: "app" }, { code: "completion_review" },
    ] };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    await show();
    const section = document.body.querySelector('[aria-label="恢复尚缺"]');
    expect(section?.textContent).toContain("Meridian：待验收原生线路及已配置落地线路的真实客户端请求");
    expect(section?.textContent).toContain("由服务端核对全部证据后完成恢复");
    expect(button("完成恢复")).toBeUndefined();
  });

  it("offers explicit cancellation when only unissued work remains", async () => {
    const unclaimedWork = [{ taskId: "unissued-install", kind: "application.apply", revision: 0 }];
    const plan = { ...review(), recovery: { ...recovery, state: "review_required" as const }, unclaimedLocalWork: unclaimedWork };
    const receipt = { planRevision: plan.revision, executionIds: [], unclaimedWork, authorizedBy: "admin", disposedAt: "2026-10-01T00:00:00Z" };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValueOnce(plan).mockResolvedValue({ ...plan, unclaimedLocalWork: [], localWorkDisposition: receipt });
    const settle = vi.spyOn(api, "settleAgentReinstallLocalWork").mockResolvedValue(receipt);
    await show();
    expect(document.body.textContent).toContain("从未下发的排队任务");
    expect(settle).not.toHaveBeenCalled();
    await act(async () => button("终止 1 条旧本机任务")!.click());
    expect(settle).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, planRevision: plan.revision, confirmLocal: true });
    expect(document.body.textContent).toContain("本次已终止 1 条旧本机任务");
    expect(button("终止 1 条旧本机任务")).toBeUndefined();
  });
  it("settles reviewed local executions only after an explicit click", async () => {
    const execution: AgentReinstallPlan["executions"][number] = { id: "old-local", agentId: "agent", taskId: "old-task", attempt: 1, kind: "agent.update", state: "unknown", phase: "started", identityRetired: true, resolution: "local_after_isolation" };
    const plan = { ...review(), recovery: { ...recovery, state: "review_required" as const, replacementFingerprint: "c".repeat(64) }, executions: [execution] };
    const receipt = { planRevision: plan.revision, executionIds: [execution.id], unclaimedWork: [], authorizedBy: "admin", disposedAt: "2026-10-01T00:00:00Z" };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValueOnce(plan).mockResolvedValue({ ...plan, executions: [], localWorkDisposition: receipt });
    const settle = vi.spyOn(api, "settleAgentReinstallLocalWork").mockResolvedValue(receipt);
    await show();
    expect(settle).not.toHaveBeenCalled();
    await act(async () => button("终止 1 条旧本机任务")!.click());
    expect(settle).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, planRevision: plan.revision, confirmLocal: true });
    expect(document.body.textContent).toContain("已终止 1 条旧本机任务，历史记录已保留");
    expect(document.body.textContent).toContain("业务验证尚未完成");
    expect(button("终止 1 条旧本机任务")).toBeUndefined();
  });
  it("does not retry uncertain local settlement or count remote work as local", async () => {
    const local: AgentReinstallPlan["executions"][number] = { id: "old-local", agentId: "agent", taskId: "old-task", attempt: 1, kind: "agent.update", state: "unknown", phase: "started", identityRetired: true, resolution: "local_after_isolation" };
    const remote = { ...local, id: "remote", agentId: "monitor", resolution: "manual_review" as const };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue({ ...review(), recovery: { ...recovery, state: "review_required" }, executions: [local, remote] });
    const settle = vi.spyOn(api, "settleAgentReinstallLocalWork").mockRejectedValue(new Error("connection lost"));
    await show();
    expect(button("终止 2 条旧本机任务")).toBeUndefined();
    await act(async () => button("终止 1 条旧本机任务")!.click());
    await act(async () => button("刷新状态")!.click());
    expect(settle).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).not.toContain("已终止");
  });
  it("keeps local settlement disabled until old private isolation is recorded", async () => {
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue({ ...review(), recovery: { ...recovery, state: "review_required", privateIsolation: "pending" }, executions: [{ id: "old-local", agentId: "agent", taskId: "old-task", attempt: 1, kind: "agent.update", state: "unknown", phase: "started", identityRetired: true, resolution: "local_after_isolation" }] });
    const settle = vi.spyOn(api, "settleAgentReinstallLocalWork");
    await show();
    expect(button("终止 1 条旧本机任务")!.disabled).toBe(true);
    expect(settle).not.toHaveBeenCalled();
  });
  it("identifies related work on other hosts before replacement", async () => {
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue({ ...review(), pendingWork: [{ agentId: "monitor", kind: "pulse.enrollment.create", count: 1 }], requirements: ["inspect_remote_effects_before_restore"] });
    const post = vi.spyOn(api, "createAgentReconnectEnrollment").mockResolvedValue(command);
    await show();
    expect(document.body.textContent).toContain("包含其他主机上关联此节点的任务");
    expect(post).not.toHaveBeenCalled();
  });
  it("reads the review before mutation and allows closing without replacing credentials", async () => {
    const get = vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(review());
    const post = vi.spyOn(api, "createAgentReconnectEnrollment").mockResolvedValue(command);
    await show();
    expect(get).toHaveBeenCalledWith("agent"); expect(post).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("需明确确认地址迁移");
    expect(document.body.textContent).toContain("重建配置");
    await act(async () => button("关闭")!.click());
    expect(post).not.toHaveBeenCalled();
  });
  it("binds explicit confirmation to the reviewed revision and shows one command", async () => {
    const get = vi.spyOn(api, "agentReinstallPlan").mockResolvedValueOnce(review()).mockResolvedValue({ ...review(), recovery });
    const post = vi.spyOn(api, "createAgentReconnectEnrollment").mockResolvedValue(command);
    await show();
    await act(async () => button("确认接替并生成命令")!.click());
    expect(post).toHaveBeenCalledExactlyOnceWith("agent", { operationId: expect.any(String), planRevision: "a".repeat(64), confirmReplacement: true });
    expect(get).toHaveBeenCalledTimes(2);
    expect(document.body.textContent).toContain("replacement-token");
    expect(document.body.textContent).toContain("恢复已暂停任务执行");
    expect(document.body.textContent).not.toContain("已确认网络保持不变");
  });
  it("keeps saved recovery available on an online node without claiming completion", async () => {
    const saved = { ...recovery, state: "review_required" as const, replacementFingerprint: "c".repeat(64) };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue({ ...review(), recovery: saved });
    const post = vi.spyOn(api, "createAgentReconnectEnrollment").mockResolvedValue(command);
    const { data } = await show();
    data.agents[0].connected = true; data.agents[0].reinstall = saved;
    await act(async () => rerender(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />));
    expect(document.body.textContent).toContain("新身份已接入，业务待恢复");
    expect(document.body.textContent).toContain("业务验证尚未完成");
    expect(button("确认接替并生成命令")).toBeUndefined(); expect(post).not.toHaveBeenCalled();
    await act(async () => button("关闭")!.click());
    expect(document.querySelector('[aria-label="查看 home-server 的恢复进度"]')).not.toBeNull();
  });
  it("does not retry a rejected or uncertain mutation automatically", async () => {
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(review());
    const post = vi.spyOn(api, "createAgentReconnectEnrollment").mockRejectedValue(new Error("recovery plan changed"));
    await show();
    await act(async () => button("确认接替并生成命令")!.click());
    expect(post).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toContain("操作未完成"); expect(document.body.textContent).not.toContain("replacement-token");
    await act(async () => button("刷新状态")!.click());
    expect(post).toHaveBeenCalledTimes(1);
  });
  it("retrieves the saved operation after reopening instead of creating another", async () => {
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue({ ...review(), recovery });
    const post = vi.spyOn(api, "createAgentReconnectEnrollment").mockResolvedValue(command);
    await show();
    await act(async () => button("取回接入命令")!.click());
    expect(post).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, planRevision: recovery.planRevision, confirmReplacement: true });
  });
  it("shows the saved isolation failure after reopening without offering another command", async () => {
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue({ ...review(), recovery: { ...recovery, state: "failed", privateIsolation: "pending", lastError: "center: authenticated previous private identity evidence is missing" } });
    const post = vi.spyOn(api, "createAgentReconnectEnrollment").mockResolvedValue(command);
    await show();
    expect(document.body.textContent).toContain("缺少旧机器已认证的私网身份记录");
    expect(button("确认接替并生成命令")).toBeUndefined();
    expect(button("取回接入命令")).toBeUndefined();
    expect(post).not.toHaveBeenCalled();
  });
  it("continues isolation only on explicit action bound to the inspected attempt", async () => {
    const paused = { ...review(), recovery: { ...recovery, state: "failed" as const, privateIsolation: "pending" as const, attempt: 2, lastError: "center: private identity withdrawal was not confirmed; inspect the saved identity before continuing" } };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValueOnce(paused).mockResolvedValue({ ...review(), recovery: { ...recovery, attempt: 3 } });
    const proceed = vi.spyOn(api, "continueAgentReinstallIsolation").mockResolvedValue(command);
    await show();
    expect(proceed).not.toHaveBeenCalled();
    await act(async () => button("核对并继续隔离")!.click());
    expect(proceed).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, expectedAttempt: 2, confirmIsolation: true });
    expect(document.body.textContent).toContain("旧私网身份撤销");
    expect(document.body.textContent).toContain("replacement-token");
    expect(button("核对并继续隔离")).toBeUndefined();
  });
});

const networkReview = (): NonNullable<AgentReinstallPlan["networkReview"]> => ({
  previous: { serviceAddress: "100.64.0.2", headscaleAddress: "100.64.0.2", enabledKinds: ["headscale"], directPublic: false },
  candidates: [{ address: "100.64.0.8", interface: "tailscale0", kind: "headscale", observedAt: "2026-10-01T00:00:00Z" }],
  privatePeer: { id: "replacement-peer", publicKey: "nodekey:replacement", address: "100.64.0.8" }, ready: true, approvalCurrent: false, profileActive: false,
});

describe("replacement network review", () => {
  it("binds address approval to the reviewed revision and keeps restoration pending", async () => {
    const plan = { ...review(), recovery: { ...recovery, state: "review_required" as const }, networkReview: networkReview() };
    const approval = { planRevision: plan.revision, previous: plan.networkReview.previous, profile: { serviceAddress: "100.64.0.8", headscaleAddress: "100.64.0.8", enabledKinds: ["headscale" as const], directPublic: false }, authorizedBy: "admin", approvedAt: "2026-10-01T00:00:01Z" };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValueOnce(plan).mockResolvedValue({ ...plan, revision: "c".repeat(64), networkReview: { ...plan.networkReview, approval, approvalCurrent: true } });
    const post = vi.spyOn(api, "approveAgentReinstallNetwork").mockResolvedValue(approval);
    await show();
    expect(post).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("原地址: 100.64.0.2");
    await act(async () => button("确认恢复地址")!.click());
    expect(post).toHaveBeenCalledExactlyOnceWith("agent", {
      operationId: recovery.id, planRevision: plan.revision, confirmMigration: true,
      profile: expect.objectContaining({ serviceAddress: "100.64.0.8", headscaleAddress: "100.64.0.8", directPublic: false }),
    });
    expect(document.body.textContent).toContain("100.64.0.2 → 100.64.0.8");
    expect(document.body.textContent).toContain("待应用恢复时启用");
    expect(document.body.textContent).toContain("业务验证尚未完成");
    expect(button("确认恢复地址")).toBeUndefined();
  });
  it("does not approve a private address without a replacement identity", async () => {
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue({ ...review(), recovery: { ...recovery, state: "review_required" }, networkReview: { ...networkReview(), privatePeer: undefined } });
    const post = vi.spyOn(api, "approveAgentReinstallNetwork");
    await show();
    expect(button("确认恢复地址")!.disabled).toBe(true);
    expect(document.body.textContent).toContain("等待新机器上报所选私网地址的身份");
    expect(post).not.toHaveBeenCalled();
  });
  it("keeps changed network evidence for explicit review without automatic retry", async () => {
    const network = networkReview();
    network.approval = { planRevision: "f".repeat(64), profile: network.previous!, authorizedBy: "admin", approvedAt: "2026-10-01T00:00:00Z" };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue({ ...review(), recovery: { ...recovery, state: "review_required" }, networkReview: network });
    const post = vi.spyOn(api, "approveAgentReinstallNetwork").mockRejectedValue(new Error("network recovery evidence changed"));
    await show();
    expect(document.body.textContent).toContain("当前网络与已确认记录不符");
    await act(async () => button("确认恢复地址")!.click());
    expect(post).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toContain("操作未完成");
    expect(button("确认恢复地址")).not.toBeUndefined();
  });
});

const monitorPlan = (): AgentReinstallPlan => ({ ...review(), recovery: { ...recovery, state: "review_required" }, monitoring: [{ applicationId: "collector", serviceApplicationId: "monitor-service", serviceAgentId: "service-agent", state: "inspection_required", enrollments: [{ enrollmentId: "original", executionId: "historical" }] }] });

describe("original monitoring identity inspection", () => {
  it("requires a click bound to the review and displays pending progress without repeating it", async () => {
    const plan = monitorPlan();
    const inspection = { commandId: "inspection", state: "pending" };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValueOnce(plan).mockResolvedValue({ ...plan, monitoring: [{ ...plan.monitoring[0], inspection }] });
    const post = vi.spyOn(api, "inspectAgentReinstallMonitor").mockResolvedValue(inspection);
    await show();
    expect(post).not.toHaveBeenCalled();
    await act(async () => button("核验原监控身份")!.click());
    expect(post).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, planRevision: plan.revision, applicationId: "collector" });
    expect(document.body.textContent).toContain("等待原监控服务");
    expect(button("等待核验结果")!.disabled).toBe(true);
    await act(async () => button("刷新状态")!.click());
    expect(post).toHaveBeenCalledTimes(1);
  });
  it("shows verified identity without presenting business recovery as complete", async () => {
    const plan = monitorPlan();
    plan.monitoring[0].inspection = { commandId: "inspection", state: "verified", nodeId: "original-monitor-node" };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    const post = vi.spyOn(api, "inspectAgentReinstallMonitor");
    await show();
    expect(document.body.textContent).toContain("原监控身份已确认");
    expect(document.body.textContent).toContain("凭据恢复和数据上报仍待完成");
    expect(button("核验原监控身份")).toBeUndefined();
    expect(post).not.toHaveBeenCalled();
  });
  it("retains failed operations for explicit inspection without automatic retries", async () => {
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(monitorPlan());
    const post = vi.spyOn(api, "inspectAgentReinstallMonitor").mockRejectedValue(new Error("monitoring review changed"));
    await show();
    await act(async () => button("核验原监控身份")!.click());
    expect(document.body.textContent).toContain("操作未完成");
    await act(async () => button("刷新状态")!.click());
    expect(post).toHaveBeenCalledTimes(1);
  });
  it.each(["evidence_missing", "evidence_invalid", "service_missing"])("does not inspect incomplete evidence: %s", async (state) => {
    const plan = monitorPlan(); plan.monitoring[0].state = state;
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    await show();
    expect(button("核验原监控身份")).toBeUndefined();
  });
  it("requires the local monitoring service to be restored first", async () => {
    const plan = monitorPlan(); plan.monitoring[0].serviceAgentId = "agent";
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    await show();
    expect(document.body.textContent).toContain("先恢复本机的 Pulse 服务和数据");
    expect(button("核验原监控身份")).toBeUndefined();
  });
});

describe("replacement application preparation", () => {
  const landingPlan = (state: string) => {
    const plan = runtimePlan();
    plan.applications[0].preparation!.landing = { state, identity: { previousAddress: "100.64.0.7", currentAddress: "100.64.0.8", previousFingerprint: "old", currentFingerprint: "new", endpointRevision: 1, observedAt: plan.checkedAt }, landings: [{ nodeId: "landing", revision: 2, peerFingerprint: "landing-peer" }] };
    return plan;
  };
  it.each([false, true])("requires an explicit landing action (authorize=%s)", async (authorize) => {
    const plan = landingPlan(authorize ? "withdrawn" : "pending");
    const next = landingPlan(authorize ? "authorizing" : "withdrawing");
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValueOnce(plan).mockResolvedValue(next);
    const action = vi.spyOn(api, authorize ? "authorizeAgentReinstallLanding" : "withdrawAgentReinstallLanding").mockResolvedValue(next.applications[0].preparation!.landing!);
    await show();
    expect(action).not.toHaveBeenCalled();
    expect(button("恢复运行配置")!.disabled).toBe(true);
    await act(async () => button(authorize ? "授权新机器身份" : "撤销旧落地授权")!.click());
    expect(action).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, planRevision: plan.revision, applicationId: "app" });
    expect(document.body.textContent).toContain(authorize ? "等待落地确认新授权" : "等待落地确认撤销");
    expect(document.body.textContent).toContain("业务验证尚未完成");
  });
  it("distinguishes restored landing configuration from client acceptance", async () => {
    const plan = landingPlan("authorized");
    plan.applications[0].preparation!.runtime = { commandId: "restored-runtime", state: "succeeded" };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    await show();
    expect(document.body.textContent).toContain("原生与落地配置已恢复 · 客户端访问待验证");
    expect(document.body.textContent).toContain("业务验证尚未完成");
  });
  it.each(["withdrawing", "authorizing", "needs_review", "authorized"])("does not repeat a %s landing operation on refresh", async (state) => {
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(landingPlan(state));
    const withdraw = vi.spyOn(api, "withdrawAgentReinstallLanding");
    const authorize = vi.spyOn(api, "authorizeAgentReinstallLanding");
    await show();
    await act(async () => button("刷新状态")!.click());
    expect(withdraw).not.toHaveBeenCalled(); expect(authorize).not.toHaveBeenCalled();
    expect(button("撤销旧落地授权")).toBeUndefined(); expect(button("授权新机器身份")).toBeUndefined();
    expect(button("恢复运行配置")!.disabled).toBe(state !== "authorized");
  });
  it("does not retry a landing action after a lost response", async () => {
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(landingPlan("pending"));
    const action = vi.spyOn(api, "withdrawAgentReinstallLanding").mockRejectedValue(new Error("connection lost"));
    await show();
    await act(async () => button("撤销旧落地授权")!.click());
    expect(document.body.textContent).toContain("操作未完成");
    await act(async () => button("刷新状态")!.click());
    expect(action).toHaveBeenCalledTimes(1);
  });


  const preparedPlan = (): AgentReinstallPlan => ({ ...review(), recovery: { ...recovery, state: "review_required", replacementFingerprint: "c".repeat(64) }, networkReview: { ...networkReview(), approvalCurrent: true } });
  const runtimePlan = () => {
    const plan = preparedPlan();
    plan.applications[0].preparation = { deploymentId: "fresh-preparation", state: "succeeded" };
    return plan;
  };
  const listenerPlan = () => {
    const plan = runtimePlan();
    plan.applications[0].sharedEntry = true;
    plan.applications[0].preparation!.runtime = { commandId: "approved-runtime", state: "succeeded" };
    plan.networkReview!.approval = { planRevision: plan.revision, profile: { serviceAddress: "100.64.0.8", headscaleAddress: "100.64.0.8", publicAddress: "198.51.100.8", publicBindAddress: "100.64.0.8", publicMode: "nat", directPublic: true, enabledKinds: ["headscale", "public"] }, authorizedBy: "admin", approvedAt: "2026-10-01T00:00:00Z" };
    return plan;
  };
  const entryCheckPlan = () => {
    const plan = listenerPlan();
    plan.applications[0].preparation!.listener = { taskId: "approved-listener", state: "succeeded" };
    plan.applications[0].preparation!.access = { state: "applied", serviceAddress: "100.64.0.8", publicAddress: "198.51.100.8", activatedAt: "2026-10-01T00:00:00Z" };
    return plan;
  };
  const entryCheck = (): AgentReinstallEntryCheck => ({ id: "entry-check", state: "passed", current: true, checkedAt: "2026-10-01T00:00:00Z", entries: [{ publicationId: "entry", hostname: "entry.example.test", publicAddress: "198.51.100.8", sniHostname: "www.example.com", state: "passed" }] });
  const dnsPlan = () => {
    const plan = entryCheckPlan();
    plan.applications[0].preparation!.dns = { id: "", state: "pending", attempt: 0, current: true, canContinue: false, checkedAt: "2026-10-01T00:00:00Z", entries: [{ publicationId: "entry", hostname: "entry.example.test", provider: "cloudflare", previousAddress: "198.51.100.7", address: "198.51.100.8", state: "pending" }] };
    return plan;
  };
  it("migrates owned DNS only after an explicit click and leaves client verification pending", async () => {
    const plan = dnsPlan();
    const dns = plan.applications[0].preparation!.dns!;
    const result = { ...dns, id: "migration", state: "succeeded", attempt: 1, entries: dns.entries.map((entry) => ({ ...entry, state: "applied" })) };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValueOnce(plan).mockResolvedValue({ ...plan, applications: plan.applications.map((app) => ({ ...app, preparation: { ...app.preparation!, dns: result } })) });
    let finish!: (value: AgentReinstallDNS) => void;
    const migrate = vi.spyOn(api, "migrateAgentReinstallDNS").mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    await show();
    expect(migrate).not.toHaveBeenCalled();
    await act(async () => button("迁移 DNS")!.click());
    expect(migrate).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, planRevision: plan.revision, applicationId: "app", expectedAttempt: 0 });
    expect(button("迁移 DNS")!.disabled).toBe(true);
    expect(document.body.textContent).toContain("正在处理 DNS");
    await act(async () => finish(result));
    expect(document.body.textContent).toContain("受管 DNS 已更新 · 公网访问待验证");
    expect(document.body.textContent).toContain("业务验证尚未完成");
    expect(button("迁移 DNS")).toBeUndefined();
    expect(document.body.textContent).toContain("entry.example.test · A · 198.51.100.8");
  });
  it("inspects uncertainty before offering an explicit DNS continuation", async () => {
    const plan = dnsPlan();
    const dns = plan.applications[0].preparation!.dns!;
    Object.assign(dns, { id: "migration", state: "needs_review", attempt: 1 });
    const inspected = { ...dns, canContinue: true };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValueOnce(plan).mockResolvedValue({ ...plan, applications: plan.applications.map((app) => ({ ...app, preparation: { ...app.preparation!, dns: inspected } })) });
    const inspect = vi.spyOn(api, "inspectAgentReinstallDNS").mockResolvedValue(inspected);
    const migrate = vi.spyOn(api, "migrateAgentReinstallDNS").mockResolvedValue({ ...dns, attempt: 2 });
    await show();
    expect(button("继续 DNS 迁移")).toBeUndefined();
    expect(migrate).not.toHaveBeenCalled();
    await act(async () => button("核对 DNS 结果")!.click());
    expect(inspect).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, planRevision: plan.revision, applicationId: "app", expectedAttempt: 1 });
    expect(migrate).not.toHaveBeenCalled();
    await act(async () => button("继续 DNS 迁移")!.click());
    expect(migrate).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, planRevision: plan.revision, applicationId: "app", expectedAttempt: 1 });
  });
  it("shows manual DNS instructions without offering provider writes", async () => {
    const plan = dnsPlan();
    plan.applications[0].preparation!.dns!.entries[0].provider = "manual";
    plan.applications[0].preparation!.dns!.entries[0].state = "manual";
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    const migrate = vi.spyOn(api, "migrateAgentReinstallDNS");
    await show();
    expect(document.body.textContent).toContain("按下方地址更新 DNS 后，验证公网入口");
    expect(button("迁移 DNS")).toBeUndefined();
    expect(button("验证公网入口")).not.toBeUndefined();
    expect(migrate).not.toHaveBeenCalled();
  });
  it.each(["stale", "blocked", "network"])("disables DNS migration for %s evidence", async (mode) => {
    const plan = dnsPlan();
    if (mode === "stale") plan.applications[0].preparation!.dns!.current = false;
    else if (mode === "blocked") plan.applications[0].preparation!.dns!.entries[0].state = "blocked";
    else plan.networkReview!.approvalCurrent = false;
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    await show();
    expect(button("迁移 DNS")!.disabled).toBe(true);
  });
  it("refreshes a lost DNS response without replaying migration", async () => {
    const plan = dnsPlan();
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    const migrate = vi.spyOn(api, "migrateAgentReinstallDNS").mockRejectedValue(new Error("response lost"));
    await show();
    await act(async () => button("迁移 DNS")!.click());
    expect(document.body.textContent).toContain("操作未完成");
    await act(async () => button("刷新状态")!.click());
    expect(migrate).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).not.toContain("受管 DNS 已更新");
  });
  it("activates the reviewed address explicitly and keeps business recovery pending", async () => {
    const plan = entryCheckPlan();
    const receipt = plan.applications[0].preparation!.access!;
    delete plan.applications[0].preparation!.access;
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValueOnce(plan).mockResolvedValue({ ...plan, networkReview: { ...plan.networkReview!, profileActive: true }, applications: plan.applications.map((app) => ({ ...app, preparation: { ...app.preparation!, access: receipt } })) });
    let finish!: (value: AgentReinstallAccess) => void;
    const activate = vi.spyOn(api, "activateAgentReinstallAccess").mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    await show();
    expect(activate).not.toHaveBeenCalled();
    expect(button("验证公网入口")).toBeUndefined();
    await act(async () => button("启用恢复地址")!.click());
    expect(activate).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, planRevision: plan.revision, applicationId: "app" });
    expect(button("启用恢复地址")!.disabled).toBe(true);
    await act(async () => finish(receipt));
    expect(button("启用恢复地址")).toBeUndefined();
    expect(document.body.textContent).toContain("恢复地址已启用 · DNS、落地与客户端访问待验证");
    expect(document.body.textContent).toContain("新网络地址已启用，各应用继续独立验证");
    expect(document.body.textContent).toContain("业务验证尚未完成");
    expect(button("验证公网入口")).not.toBeUndefined();
    await act(async () => button("刷新状态")!.click());
    expect(activate).toHaveBeenCalledTimes(1);
  });
  it.each(["runtime", "listener", "approval", "old-work"])("blocks address activation without %s", async (mode) => {
    const plan = entryCheckPlan();
    delete plan.applications[0].preparation!.access;
    if (mode === "runtime") plan.applications[0].preparation!.runtime!.state = "pending";
    else if (mode === "listener") plan.applications[0].preparation!.listener!.state = "needs_review";
    else if (mode === "approval") plan.networkReview!.approvalCurrent = false;
    else plan.unclaimedLocalWork = [{ taskId: "previous-task", kind: "application.apply", revision: 0 }];
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    const activate = vi.spyOn(api, "activateAgentReinstallAccess");
    await show();
    const action = button("启用恢复地址");
    expect(!action || action.disabled).toBe(true);
    expect(button("验证公网入口")).toBeUndefined();
    expect(activate).not.toHaveBeenCalled();
  });
  it("shows changed active bindings without a reapply action", async () => {
    const plan = entryCheckPlan(); plan.applications[0].preparation!.access!.state = "needs_review";
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    const activate = vi.spyOn(api, "activateAgentReinstallAccess");
    await show();
    expect(document.body.textContent).toContain("恢复地址已变化 · 需核对，未重新应用");
    expect(button("启用恢复地址")).toBeUndefined();
    expect(button("验证公网入口")).toBeUndefined();
    expect(activate).not.toHaveBeenCalled();
  });
  it("does not retry address activation after losing its response", async () => {
    const plan = entryCheckPlan(); delete plan.applications[0].preparation!.access;
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    const activate = vi.spyOn(api, "activateAgentReinstallAccess").mockRejectedValue(new Error("connection lost"));
    await show();
    await act(async () => button("启用恢复地址")!.click());
    expect(document.body.textContent).toContain("操作未完成");
    await act(async () => button("刷新状态")!.click());
    expect(activate).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).not.toContain("恢复地址已启用");
  });
  it("checks public entry explicitly and separates TLS from real client verification", async () => {
    const plan = entryCheckPlan();
    const result = entryCheck();
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValueOnce(plan).mockResolvedValue({ ...plan, applications: plan.applications.map((app) => ({ ...app, preparation: { ...app.preparation!, entryCheck: result } })) });
    let finish!: (value: AgentReinstallEntryCheck) => void;
    const verify = vi.spyOn(api, "verifyAgentReinstallEntry").mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    await show();
    expect(verify).not.toHaveBeenCalled();
    await act(async () => button("验证公网入口")!.click());
    expect(verify).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, planRevision: plan.revision, applicationId: "app" });
    expect(button("验证公网入口")!.disabled).toBe(true);
    expect(document.body.textContent).toContain("正在验证 DNS 与 TLS 入口");
    await act(async () => finish(result));
    expect(document.body.textContent).toContain("DNS 与 TLS 已通过 · 真实客户端访问待验证");
    expect(document.body.textContent).toContain("业务验证尚未完成");
    expect(button("重新验证入口")!.disabled).toBe(false);
    expect(document.body.textContent).toContain("entry.example.test → 198.51.100.8");
  });
  it.each(["dns_pending", "tls_pending", "not_checked"] as const)("shows persisted %s evidence without rechecking on refresh", async (state) => {
    const plan = entryCheckPlan();
    const result = entryCheck(); result.state = "pending"; result.entries[0].state = state;
    plan.applications[0].preparation!.entryCheck = result;
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    const verify = vi.spyOn(api, "verifyAgentReinstallEntry");
    await show();
    await act(async () => button("刷新状态")!.click());
    expect(verify).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("公网入口未通过");
    expect(document.body.textContent).toContain(state === "dns_pending" ? "DNS 尚未全部指向新公网地址" : state === "tls_pending" ? "TLS 1.3 校验未通过" : "前项未通过，尚未检查");
  });
  it("labels historical success as stale and disables checks without current network approval", async () => {
    const plan = entryCheckPlan();
    plan.applications[0].preparation!.entryCheck = { ...entryCheck(), current: false };
    plan.networkReview!.approvalCurrent = false;
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    await show();
    expect(document.body.textContent).toContain("检查结果已失效 · 请重新验证");
    expect(document.body.textContent).not.toContain("DNS 与 TLS 已通过 · 真实客户端访问待验证");
    expect(button("重新验证入口")!.disabled).toBe(true);
  });
  it("does not retry a lost entry check response", async () => {
    const plan = entryCheckPlan();
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    const verify = vi.spyOn(api, "verifyAgentReinstallEntry").mockRejectedValue(new Error("request interrupted"));
    await show();
    await act(async () => button("验证公网入口")!.click());
    expect(verify).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toContain("操作未完成");
    await act(async () => button("刷新状态")!.click());
    expect(verify).toHaveBeenCalledTimes(1);
  });
  it("restores a saved shared entry explicitly without claiming public reachability", async () => {
    const plan = listenerPlan();
    const listener = { taskId: "approved-listener", state: "succeeded" };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValueOnce(plan).mockResolvedValue({ ...plan, applications: plan.applications.map((app) => ({ ...app, preparation: { ...app.preparation!, listener } })) });
    const restore = vi.spyOn(api, "restoreAgentReinstallListener").mockResolvedValue(listener);
    await show();
    expect(restore).not.toHaveBeenCalled();
    await act(async () => button("恢复入口")!.click());
    expect(restore).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, planRevision: plan.revision, applicationId: "app" });
    expect(document.body.textContent).toContain("入口已应用 · 公网访问待验证");
    expect(document.body.textContent).toContain("业务验证尚未完成");
    expect(button("恢复入口")).toBeUndefined();
  });
  it.each(["network", "runtime", "no-entry"])("does not offer entry restoration with %s missing", async (mode) => {
    const plan = listenerPlan();
    if (mode === "network") plan.networkReview!.approval!.profile.directPublic = false;
    else if (mode === "runtime") plan.applications[0].preparation!.runtime!.state = "pending";
    else plan.applications[0].sharedEntry = false;
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    await show();
    expect(button("恢复入口")).toBeUndefined();
  });
  it.each(["pending", "running", "failed", "needs_review"])("keeps the %s listener outcome without replaying", async (state) => {
    const plan = listenerPlan();
    plan.applications[0].preparation!.listener = { taskId: "approved-listener", state };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    const restore = vi.spyOn(api, "restoreAgentReinstallListener");
    await show();
    await act(async () => button("刷新状态")!.click());
    expect(restore).not.toHaveBeenCalled();
    expect(button("恢复入口")).toBeUndefined();
    expect(document.body.textContent).toContain(state === "pending" || state === "running" ? "正在恢复入口" : "入口恢复结果需核对");
  });
  it("restores runtime only on explicit approval and retains pending business verification", async () => {
    const plan = runtimePlan();
    const runtime = { commandId: "approved-runtime", state: "succeeded" };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValueOnce(plan).mockResolvedValue({ ...plan, applications: plan.applications.map((app) => ({ ...app, preparation: { ...app.preparation!, runtime } })) });
    const restore = vi.spyOn(api, "restoreAgentReinstallRuntime").mockResolvedValue(runtime);
    await show();
    expect(restore).not.toHaveBeenCalled();
    await act(async () => button("恢复运行配置")!.click());
    expect(restore).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, planRevision: plan.revision, applicationId: "app" });
    expect(document.body.textContent).toContain("本机配置已恢复 · 入口与落地待验证");
    expect(document.body.textContent).toContain("业务验证尚未完成");
    expect(button("恢复运行配置")).toBeUndefined();
  });
  it("requires explicit review before replacing a completed obsolete runtime", async () => {
    const plan = runtimePlan();
    plan.applications[0].preparation!.runtime = { commandId: "old-runtime", state: "review_changed" };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    const restore = vi.spyOn(api, "restoreAgentReinstallRuntime").mockResolvedValue({ commandId: "new-runtime", state: "pending" });
    await show();
    await act(async () => button("刷新状态")!.click());
    expect(restore).not.toHaveBeenCalled();
    await act(async () => button("审核当前配置并继续")!.click());
    expect(restore).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, planRevision: plan.revision, applicationId: "app" });
  });
  it.each(["network", "old-work"])("keeps runtime disabled for %s prerequisites", async (mode) => {
    const plan = runtimePlan();
    if (mode === "network") plan.networkReview!.approvalCurrent = false;
    else plan.unclaimedLocalWork = [{ taskId: "previous-task", kind: "application.apply", revision: 0 }];
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    await show();
    expect(button("恢复运行配置")!.disabled).toBe(true);
  });
  it.each(["pending", "running", "failed", "needs_review"])("does not replay a %s runtime receipt", async (state) => {
    const plan = runtimePlan();
    plan.applications[0].preparation!.runtime = { commandId: "approved-runtime", state };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    const restore = vi.spyOn(api, "restoreAgentReinstallRuntime");
    await show();
    await act(async () => button("刷新状态")!.click());
    expect(restore).not.toHaveBeenCalled();
    expect(button("恢复运行配置")).toBeUndefined();
    expect(document.body.textContent).toContain(state === "pending" || state === "running" ? "正在恢复运行配置" : "运行配置结果需核对");
  });
  it("does not retry runtime restoration after a lost response", async () => {
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(runtimePlan());
    const restore = vi.spyOn(api, "restoreAgentReinstallRuntime").mockRejectedValue(new Error("connection lost"));
    await show();
    await act(async () => button("恢复运行配置")!.click());
    await act(async () => button("刷新状态")!.click());
    expect(restore).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).not.toContain("本机配置已恢复");
  });
  it("requires an explicit click and distinguishes a prepared image from restored runtime", async () => {
    const plan = preparedPlan();
    const receipt = { deploymentId: "fresh-preparation", state: "succeeded" };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValueOnce(plan).mockResolvedValue({ ...plan, applications: plan.applications.map((app) => ({ ...app, preparation: receipt })) });
    const prepare = vi.spyOn(api, "prepareAgentReinstallApplication").mockResolvedValue(receipt);
    await show();
    expect(prepare).not.toHaveBeenCalled();
    await act(async () => button("准备原版本")!.click());
    expect(prepare).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, planRevision: plan.revision, applicationId: "app" });
    expect(document.body.textContent).toContain("镜像已准备 · 运行配置待恢复");
    expect(document.body.textContent).toContain("业务验证尚未完成");
    expect(button("准备原版本")).toBeUndefined();
  });
  it.each(["network", "old-work"])("keeps preparation disabled for %s prerequisites", async (mode) => {
    const plan = preparedPlan();
    if (mode === "network") plan.networkReview!.approvalCurrent = false;
    else plan.unclaimedLocalWork = [{ taskId: "previous-task", kind: "application.apply", revision: 0 }];
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    const prepare = vi.spyOn(api, "prepareAgentReinstallApplication");
    await show();
    expect(button("准备原版本")!.disabled).toBe(true);
    expect(prepare).not.toHaveBeenCalled();
  });
  it.each(["pending", "failed", "needs_review"])("displays the durable %s receipt without replaying", async (state) => {
    const plan = preparedPlan(); plan.applications[0].preparation = { deploymentId: "fresh-preparation", state };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    const prepare = vi.spyOn(api, "prepareAgentReinstallApplication");
    await show();
    await act(async () => button("刷新状态")!.click());
    expect(prepare).not.toHaveBeenCalled();
    expect(button("准备原版本")).toBeUndefined();
    expect(document.body.textContent).toContain(state === "pending" ? "正在准备" : state === "needs_review" ? "结果未确认" : "准备失败");
  });
});

describe("original monitoring credential rotation", () => {
  const rotationPlan = (): AgentReinstallPlan => {
    const plan = monitorPlan();
    plan.monitoring[0].inspection = { commandId: "inspection", state: "verified", nodeId: "original-monitor-node" };
    return plan;
  };
  it("requires explicit rotation and retains the pending operation across refreshes", async () => {
    const plan = rotationPlan();
    const rotation = { commandId: "rotation", state: "pending", nodeId: "original-monitor-node" };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValueOnce(plan).mockResolvedValue({ ...plan, monitoring: [{ ...plan.monitoring[0], rotation }] });
    const post = vi.spyOn(api, "rotateAgentReinstallMonitor").mockResolvedValue(rotation);
    await show();
    expect(post).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("旧凭据立即失效，监控历史保留");
    await act(async () => button("轮换原监控凭据")!.click());
    expect(post).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, planRevision: plan.revision, applicationId: "collector" });
    expect(document.body.textContent).toContain("等待轮换监控凭据");
    expect(button("轮换原监控凭据")).toBeUndefined();
    await act(async () => button("刷新状态")!.click());
    expect(post).toHaveBeenCalledTimes(1);
  });
  it.each(["needs_review", "failed", "rotated"])("does not reissue a retained credential operation: %s", async (state) => {
    const plan = rotationPlan();
    plan.monitoring[0].rotation = { commandId: "rotation", state, nodeId: "original-monitor-node" };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    const post = vi.spyOn(api, "rotateAgentReinstallMonitor");
    await show();
    expect(button("轮换原监控凭据")).toBeUndefined();
    expect(button("核验原监控身份")).toBeUndefined();
    expect(document.body.textContent).toContain(state === "rotated" ? "采集端恢复与上报待验证" : "凭据轮换结果需核对");
    expect(post).not.toHaveBeenCalled();
  });
  it("does not retry a lost rotation response", async () => {
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(rotationPlan());
    const post = vi.spyOn(api, "rotateAgentReinstallMonitor").mockRejectedValue(new Error("response lost"));
    await show();
    await act(async () => button("轮换原监控凭据")!.click());
    expect(document.body.textContent).toContain("操作未完成");
    await act(async () => button("刷新状态")!.click());
    expect(post).toHaveBeenCalledTimes(1);
  });
});

describe("restore original monitoring collector", () => {
  const readyPlan = (): AgentReinstallPlan => {
    const plan = monitorPlan();
    plan.monitoring[0].inspection = { commandId: "inspection", state: "verified", nodeId: "original-monitor-node" };
    plan.monitoring[0].rotation = { commandId: "rotation", state: "rotated", nodeId: "original-monitor-node" };
    plan.networkReview = { candidates: [], ready: true, approvalCurrent: true, profileActive: false };
    return plan;
  };
  it("restores only after an explicit reviewed click", async () => {
    const plan = readyPlan();
    const restoration = { deploymentId: "restoration", state: "pending" };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValueOnce(plan).mockResolvedValue({ ...plan, monitoring: [{ ...plan.monitoring[0], restoration }] });
    const post = vi.spyOn(api, "restoreAgentReinstallMonitor").mockResolvedValue(restoration);
    await show();
    expect(post).not.toHaveBeenCalled();
    await act(async () => button("恢复原监控采集端")!.click());
    expect(post).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, planRevision: plan.revision, applicationId: "collector" });
    expect(document.body.textContent).toContain("等待恢复采集端");
    expect(button("恢复原监控采集端")).toBeUndefined();
    await act(async () => button("刷新状态")!.click());
    expect(post).toHaveBeenCalledTimes(1);
  });
  it("verifies actual original-node reporting only on an explicit click", async () => {
    const plan = readyPlan();
    plan.monitoring[0].restoration = { deploymentId: "restoration", state: "succeeded" };
    const reporting = { commandId: "report", state: "verified", checkedAt: "2026-10-01T00:00:00Z" };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValueOnce(plan).mockResolvedValue({ ...plan, monitoring: [{ ...plan.monitoring[0], reporting }] });
    const post = vi.spyOn(api, "inspectAgentReinstallMonitorReporting").mockResolvedValue(reporting);
    await show();
    expect(post).not.toHaveBeenCalled();
    await act(async () => button("验证原节点上报")!.click());
    expect(post).toHaveBeenCalledExactlyOnceWith("agent", { operationId: recovery.id, planRevision: plan.revision, applicationId: "collector" });
    expect(document.body.textContent).toContain("原节点已恢复上报");
    expect(button("验证原节点上报")).toBeUndefined();
    expect(document.body.textContent).not.toContain("原节点数据上报待验证");
  });
  it.each(["pending", "running", "not_reporting", "stale"])("retains honest report state and explicit retry: %s", async (state) => {
    const plan = readyPlan();
    plan.monitoring[0].restoration = { deploymentId: "restoration", state: "succeeded" };
    plan.monitoring[0].reporting = { commandId: "report", state };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    const post = vi.spyOn(api, "inspectAgentReinstallMonitorReporting");
    await show();
    expect(button("验证原节点上报")!.disabled).toBe(state === "pending" || state === "running");
    expect(document.body.textContent).not.toContain("原节点已恢复上报");
    expect(post).not.toHaveBeenCalled();
  });
  it("requires an approved current network", async () => {
    const plan = readyPlan();
    plan.networkReview = { ...plan.networkReview!, approvalCurrent: false };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    await show();
    expect(button("恢复原监控采集端")!.disabled).toBe(true);
  });
  it.each(["succeeded", "needs_review"])("keeps reporting verification separate: %s", async (state) => {
    const plan = readyPlan();
    plan.monitoring[0].restoration = { deploymentId: "restoration", state };
    vi.spyOn(api, "agentReinstallPlan").mockResolvedValue(plan);
    await show();
    expect(button("恢复原监控采集端")).toBeUndefined();
    expect(document.body.textContent).toContain(state === "succeeded" ? "采集端已启动" : "采集端恢复结果需核对");
  });
});
