// @vitest-environment jsdom

import { act } from "react";
import { describe, expect, it } from "vitest";
import { NodesView } from "./NodesView";
import { dashboard, render } from "./views.test-support";

describe("landing-only node enrollment", () => {
  it("labels an enrolled landing-only node and hides incompatible egress controls", () => {
    const data = dashboard();
    data.agents[0].capabilities = { ...data.agents[0].capabilities, docker: false, gateway: false, tunnel: false, landingOnly: true };
    const container = render(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    expect(container.querySelector('table')?.textContent).toContain("仅落地");
    act(() => container.querySelector<HTMLButtonElement>(`[aria-label="管理 ${data.agents[0].name}"]`)?.click());
    expect(document.body.textContent).not.toContain("出口 IP 策略");
    expect(document.querySelector<HTMLButtonElement>("#manage-node-gateway")?.disabled).toBe(true);
    expect(document.querySelector<HTMLButtonElement>("#manage-node-tunnel")?.disabled).toBe(true);
  });
  it("requires managed private networking before enabling the purpose", () => {
    const data = dashboard();
    data.integrations = [];
    const container = render(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent === "添加节点")?.click());
    const toggle = document.querySelector<HTMLButtonElement>("#new-node-landing-only");
    expect(toggle).not.toBeNull();
    expect(toggle?.disabled).toBe(true);
    expect(document.body.textContent).toContain("请先配置 Headscale 安全私网");
  });

  it("hides incompatible gateway and tunnel choices for landing-only enrollment", () => {
    const data = dashboard();
    data.integrations = [{ kind: "headscale", status: "configured", secretSet: true, endpoint: "https://network.example.com", updatedAt: "2026-10-18T00:00:00Z" }];
    const container = render(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    act(() => [...container.querySelectorAll("button")].find((button) => button.textContent === "添加节点")?.click());
    act(() => document.querySelector<HTMLButtonElement>("#new-node-landing-only")?.click());
    expect(document.querySelector("#new-node-gateway")).toBeNull();
    expect(document.querySelector("#new-node-tunnel")).toBeNull();
    expect(document.body.textContent).toContain("不会卸载已有 Docker");
  });
});
