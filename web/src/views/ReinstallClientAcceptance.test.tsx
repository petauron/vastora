// @vitest-environment jsdom
import { act } from "react";
import { describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { AgentReinstallPlan } from "../types";
import { ReinstallClientAcceptance } from "./ReinstallClientAcceptance";
import { render, rerender } from "./views.test-support";

const plan = { agentId: "target", revision: "review", recovery: { id: "operation" }, applications: [] } as unknown as AgentReinstallPlan;
describe("automatic client acceptance", () => {
  it("reads evidence without automatically issuing requests, including refresh", async () => {
    vi.spyOn(api, "agents").mockResolvedValue({ agents: [] });
    const read = vi.spyOn(api, "agentReinstallClientChecks").mockResolvedValue([{commandId:"check",state:"verified",accountName:"Saved account",protocol:"vless",egressName:"Landing"}]);
    const issue = vi.spyOn(api, "verifyAgentReinstallClients").mockResolvedValue([]);
    const container = render(<ReinstallClientAcceptance plan={plan} language="zh-CN" revision={0} />);
    await act(async () => {});
    expect(container.textContent).toContain("真实请求通过");
    expect(container.textContent).toContain("Saved account");
    expect(container.textContent).toContain("Landing");
    expect(issue).not.toHaveBeenCalled();
    await act(async () => rerender(<ReinstallClientAcceptance plan={plan} language="zh-CN" revision={1} />));
    expect(read).toHaveBeenCalledTimes(2);
    expect(issue).not.toHaveBeenCalled();
  });
  it("does not display unverified success as acceptance", async () => {
    vi.spyOn(api,"agents").mockResolvedValue({agents:[]});
    vi.spyOn(api,"agentReinstallClientChecks").mockResolvedValue([{commandId:"check",state:"succeeded"}]);
    const container=render(<ReinstallClientAcceptance plan={plan} language="zh-CN" revision={0} />);
    await act(async()=>{});
    expect(container.textContent).toContain("正在核对证据");
    expect(container.textContent).not.toContain("真实请求通过");
  });
});
