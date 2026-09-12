// @vitest-environment jsdom
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { ThreeXUIClient, ThreeXUIClientInbound } from "../types";
import { ThreeXUIClientCard } from "./ThreeXUIClientCard";

const client: ThreeXUIClient = { email: "My MacBook", enabled: true, usedBytes: 64 * 1024 ** 3, totalBytes: 0, expiryTime: 0, resetDays: 0, limitIp: 0, inboundIds: [1, 2, 3, 4, 5, 6, 7], hasSubscription: true };
const inbounds: ThreeXUIClientInbound[] = client.inboundIds.map((id) => ({ id, name: `inbound-${id}`, nodeName: `Node ${id}`, displayName: `🇺🇸 美国｜Node ${id}`, connectHostname: `node-${id}.example.test` }));

function card(value = client, subscriptionAvailable = true, language: "en" | "zh-CN" = "zh-CN") {
  const container = document.createElement("div");
  container.innerHTML = renderToStaticMarkup(<ThreeXUIClientCard
    client={value} inbounds={inbounds} language={language} expiryLabel={language === "zh-CN" ? "永不过期" : "Never"}
    busy={false} linksDisabled={false} subscriptionAvailable={subscriptionAvailable}
    onEnabledChange={vi.fn()} onCopySubscription={vi.fn()} onCopyLink={vi.fn()} onEdit={vi.fn()} onReset={vi.fn()} onDelete={vi.fn()}
  />);
  return container;
}

describe("subscription client card", () => {
  it("collapses the node list without losing full names", () => {
    const container = card();
    const details = container.querySelector("details")!;
    expect(details.open).toBe(false);
    expect(details.querySelector("summary")?.textContent).toBe("已接入 7 个节点");
    expect(details.querySelectorAll("li")).toHaveLength(7);
    expect(details.textContent).toContain("🇺🇸 美国｜Node 7");
    expect(container.querySelectorAll("h3")).toHaveLength(1);
    expect(container.querySelector('[data-slot="card-header"]')?.textContent).not.toContain("已接入 7 个节点：");
  });

  it("separates usage from limits and prioritizes the subscription action", () => {
    const container = card();
    expect(container.textContent).toContain("64.0 GB");
    expect(container.textContent).toContain("不限流量");
    expect(container.querySelectorAll("dl dt")).toHaveLength(3);
    const buttons = [...container.querySelectorAll<HTMLButtonElement>('[data-slot="card-footer"] button')];
    expect(buttons[0].textContent).toBe("复制订阅");
    expect(buttons[0].disabled).toBe(false);
    expect(buttons[1].textContent).toBe("编辑");
    expect(buttons[2].getAttribute("aria-label")).toBe("更多操作：My MacBook");
    expect(container.textContent).not.toContain("复制 VLESS");
    expect(container.textContent).not.toContain("删除客户端");
    expect(container.textContent).not.toContain("同一地址会自动适配");
  });

  it("handles no nodes, a disabled client and unavailable subscriptions", () => {
    const container = card({ ...client, email: "A very long client name without truncation", inboundIds: [], enabled: false, totalBytes: 100 * 1024 ** 3, limitIp: 3, resetDays: 30 }, false);
    expect(container.querySelector("h3")?.textContent).toBe("A very long client name without truncation");
    expect(container.querySelector("details")).toBeNull();
    expect(container.textContent).toContain("未连接节点");
    expect(container.textContent).toContain("已停用");
    expect(container.textContent).toContain("/ 100.0 GB");
    expect(container.textContent).toContain("每 30 天");
    expect(container.textContent).toContain("开启公网订阅后可复制地址");
    expect(container.querySelector<HTMLButtonElement>('[data-slot="card-footer"] button')?.disabled).toBe(true);
  });

  it("keeps the English card concise", () => {
    const container = card(client, true, "en");
    expect(container.querySelector("summary")?.textContent).toBe("7 connected node(s)");
    expect(container.textContent).toContain("Data used");
    expect(container.textContent).toContain("Copy subscription");
    expect(container.querySelectorAll("dl dd")[0]?.textContent).toBe("Never");
  });
});
