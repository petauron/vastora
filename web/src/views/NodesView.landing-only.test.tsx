// @vitest-environment jsdom

import { act } from "react";
import { describe, expect, it, vi } from "vitest";
import { api } from "../api";
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

  it("renders node settings with SheetBody and a destructive footer action", () => {
    const data = dashboard();
    const container = render(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    act(() => container.querySelector<HTMLButtonElement>(`[aria-label="管理 ${data.agents[0].name}"]`)?.click());
    expect(document.querySelector('[data-slot="sheet-body"]')).not.toBeNull();
    const footer = document.querySelector<HTMLElement>('[data-slot="sheet-footer"]');
    const disableButton = [...(footer?.querySelectorAll<HTMLButtonElement>("button") ?? [])].find((button) => button.textContent?.includes("停用节点"));
    expect(disableButton?.getAttribute("data-variant")).toBe("destructive");
    expect(disableButton?.getAttribute("data-size")).toBe("default");
  });

  it("partitions node settings into tabs, defers secondary subsystem fetches, and scopes save to General", async () => {
    const data = dashboard();
    data.agents[0].capabilities = { ...data.agents[0].capabilities, nodeSecurity: true, nodeSwap: true, osReinstallPreflight: true };
    const securitySpy = vi.spyOn(api, "nodeSecurity").mockResolvedValue({ id: "", state: "unconfigured" });
    const swapSpy = vi.spyOn(api, "nodeSwap").mockResolvedValue({ id: "", state: "unconfigured" });
    const inspectSpy = vi.spyOn(api, "osReinstallInspection").mockResolvedValue({ check: null });
    const taskSpy = vi.spyOn(api, "osReinstallTask").mockResolvedValue({ task: null });

    const container = render(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    act(() => container.querySelector<HTMLButtonElement>(`[aria-label="管理 ${data.agents[0].name}"]`)?.click());

    const tabs = [...document.querySelectorAll<HTMLElement>('[role="tab"]')];
    expect(tabs.map((tab) => tab.textContent)).toEqual(["常规", "安全", "系统与重装"]);
    expect(securitySpy).not.toHaveBeenCalled();
    expect(swapSpy).not.toHaveBeenCalled();
    expect(inspectSpy).not.toHaveBeenCalled();
    const visibleFooter = () => [...document.querySelectorAll<HTMLElement>('[data-slot="sheet-footer"]')].find((el) => !el.closest("[hidden]"));
    expect(visibleFooter()?.textContent).toContain("保存信息");

    await act(async () => { tabs[1]?.click(); await Promise.resolve(); });
    expect(securitySpy).toHaveBeenCalledOnce();
    expect(document.body.textContent).toContain("公网安全基线");
    expect(document.body.textContent).toContain("SSH 保护策略");
    expect(visibleFooter()?.textContent).not.toContain("保存信息");

    // Draft inputs in Security survive switching back to General and returning, without refetching.
    const ifaceInput = document.querySelector<HTMLInputElement>('input[placeholder="eth0"]')!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(ifaceInput, "enp1s0");
      ifaceInput.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => { tabs[0]?.click(); await Promise.resolve(); });
    await act(async () => { tabs[1]?.click(); await Promise.resolve(); });
    expect(securitySpy).toHaveBeenCalledOnce();
    expect(document.querySelector<HTMLInputElement>('input[placeholder="eth0"]')?.value).toBe("enp1s0");

    await act(async () => { tabs[2]?.click(); await Promise.resolve(); });
    expect(swapSpy).toHaveBeenCalledOnce();
    expect(inspectSpy).toHaveBeenCalledOnce();
    expect(taskSpy).toHaveBeenCalledOnce();
    expect(document.body.textContent).toContain("主机 Zram 与 Swap 优化");
    expect(document.body.textContent).toContain("系统重装");

    // Closing while on a visited secondary tab and reopening the same node resets to General without Render-1 refetches.
    await act(async () => {
      [...(visibleFooter()?.querySelectorAll<HTMLButtonElement>("button") ?? [])].find((button) => button.textContent === "关闭")?.click();
      await Promise.resolve();
    });
    await act(async () => {
      container.querySelector<HTMLButtonElement>(`[aria-label="管理 ${data.agents[0].name}"]`)?.click();
      await Promise.resolve();
    });
    const reopenedTabs = [...document.querySelectorAll<HTMLElement>('[role="tab"]')];
    expect(reopenedTabs[0]?.getAttribute("aria-selected")).toBe("true");
    expect(visibleFooter()?.textContent).toContain("保存信息");
    expect(securitySpy).toHaveBeenCalledOnce();
    expect(swapSpy).toHaveBeenCalledOnce();
    expect(inspectSpy).toHaveBeenCalledOnce();
    expect(taskSpy).toHaveBeenCalledOnce();

    // Visiting secondary tabs, returning to General, and cancelling danger mode does not remount hidden secondary tabs.
    await act(async () => { reopenedTabs[1]?.click(); await Promise.resolve(); });
    await act(async () => { reopenedTabs[2]?.click(); await Promise.resolve(); });
    expect(securitySpy).toHaveBeenCalledTimes(2);
    expect(swapSpy).toHaveBeenCalledTimes(2);
    expect(inspectSpy).toHaveBeenCalledTimes(2);
    expect(taskSpy).toHaveBeenCalledTimes(2);
    await act(async () => { reopenedTabs[0]?.click(); await Promise.resolve(); });
    act(() => [...(visibleFooter()?.querySelectorAll<HTMLButtonElement>("button") ?? [])].find((button) => button.textContent?.includes("停用节点"))?.click());
    expect(document.querySelector("#disable-node-confirmation")).not.toBeNull();
    await act(async () => {
      [...document.querySelectorAll<HTMLButtonElement>('[data-slot="sheet-footer"] button')].find((button) => button.textContent === "取消")?.click();
      await Promise.resolve();
    });
    expect(securitySpy).toHaveBeenCalledTimes(2);
    expect(swapSpy).toHaveBeenCalledTimes(2);
    expect(inspectSpy).toHaveBeenCalledTimes(2);
    expect(taskSpy).toHaveBeenCalledTimes(2);
  });

  it("opens location management directly from the Nodes page header and returns focus after editing", async () => {
    const data = dashboard();
    const container = render(<NodesView data={data} language="zh-CN" mutate={async () => undefined} onNavigate={() => undefined} />);
    const manageLocations = [...container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("管理位置"))!;
    expect(manageLocations).toBeDefined();
    act(() => {
      manageLocations.focus();
      manageLocations.click();
    });
    expect(document.body.textContent).toContain("位置管理");
    expect(document.body.textContent).toContain(data.sites[0].name);
    const newLocation = [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("新建位置"));
    await act(async () => {
      newLocation?.click();
      await Promise.resolve();
    });
    const siteNameInput = document.querySelector<HTMLInputElement>("#site-name");
    expect(siteNameInput).not.toBeNull();
    expect(document.activeElement).toBe(siteNameInput);
    await act(async () => {
      [...document.querySelectorAll<HTMLButtonElement>('[data-slot="sheet-footer"] button')].find((button) => button.textContent === "取消")?.click();
      await Promise.resolve();
    });
    expect(document.activeElement).toBe(manageLocations);
  });
});
