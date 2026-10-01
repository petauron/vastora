// @vitest-environment jsdom
import { act } from "react";
import { describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { AgentReinstallPlan } from "../types";
import { NodesView } from "./NodesView";
import { dashboard, render, rerender } from "./views.test-support";

const review = (): AgentReinstallPlan => ({
  agentId: "agent", revision: "a".repeat(64), checkedAt: "2026-10-01T00:00:00Z", identityFingerprint: "b".repeat(64), credentialRevoked: false,
  privateNetwork: { ownership: "managed", serviceAddress: "100.64.0.2", privateAddress: "100.64.0.2", profileRetained: false, addressRecovery: "explicit_migration_required", landingRoutes: 1, publications: 2 },
  applications: [{ applicationId: "app", name: "Meridian", appKey: "vastora-official/meridian", deploymentId: "deployment", version: "0.1.0-alpha.12", operation: "install", state: "succeeded", recovery: "rebuild_configuration", requirements: [] }],
  pendingWork: [{ agentId: "agent", kind: "application.apply", count: 1 }], unclaimedLocalWork: [], executions: [], requirements: [],
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
  privatePeer: { id: "replacement-peer", publicKey: "nodekey:replacement", address: "100.64.0.8" }, ready: true, approvalCurrent: false,
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
