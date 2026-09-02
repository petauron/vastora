// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api";
import { ThemeProvider } from "../components/theme";
import type { AssistantConversation } from "../types";
import { AssistantView } from "./AssistantView";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function renderAssistant() {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(<ThemeProvider><AssistantView language="zh-CN" /></ThemeProvider>);
    await Promise.resolve();
  });
  return container;
}

class QuietEventSource {
  constructor(readonly url: string, readonly init?: EventSourceInit) {}
  addEventListener() {}
  removeEventListener() {}
  close() {}
  onerror: ((event: Event) => void) | null = null;
}

it("renders the exact proposal and uses only the trusted approval action", async () => {
  vi.stubGlobal("EventSource", QuietEventSource);
  const proposal = {
    id: "proposal-1",
    conversationId: "conversation-1",
    runId: "run-1",
    kind: "install_application" as const,
    summary: { action: "install", agentId: "agent-1", agentName: "上海节点", appKey: "vastora-official/3x-ui", appName: { en: "3x-ui", "zh-CN": "3x-ui" }, version: "3.6.0", impact: "Install one app", dataRetention: "Data is retained" },
    digest: "a".repeat(64),
    targets: [{ kind: "agent", id: "agent-1" }],
    expectedRevision: "revision-1",
    policyVersion: "install-application-v1",
    risk: "medium" as const,
    status: "pending" as const,
    expiresAt: "2026-08-30T14:00:00Z",
    createdAt: "2026-08-30T13:00:00Z",
    updatedAt: "2026-08-30T13:00:00Z"
  };
  const conversation: AssistantConversation = {
    id: "conversation-1", title: "安装 3x-ui",
    messages: [{ id: "message-1", role: "user", content: "请安装 3x-ui", createdAt: "2026-08-30T13:00:00Z" }],
    runs: [{ id: "run-1", conversationId: "conversation-1", status: "approval_required", createdAt: "2026-08-30T13:00:00Z", updatedAt: "2026-08-30T13:00:00Z" }],
    proposals: [proposal], createdAt: "2026-08-30T13:00:00Z", updatedAt: "2026-08-30T13:00:00Z"
  };
  vi.spyOn(api, "assistantProvider").mockResolvedValue({ apiUrl: "https://provider.example/v1", model: "model", apiKeySet: true, allowPrivate: false, status: "verified", updatedAt: "2026-08-30T13:00:00Z" });
  vi.spyOn(api, "assistantConversations").mockResolvedValue({ conversations: [conversation] });
  vi.spyOn(api, "assistantConversation").mockResolvedValue(conversation);
  const decide = vi.spyOn(api, "decideAssistantProposal").mockResolvedValue({ ...proposal, status: "approved" });

  const container = await renderAssistant();
  await vi.waitFor(() => expect(container.textContent).toContain("变更审批"));
  expect(container.textContent).toContain("上海节点");
  expect(container.textContent).toContain("3.6.0");
  expect(container.textContent).toContain(proposal.digest);
  expect(container.textContent).toContain("聊天中的“确认”不会执行操作");
  const approve = [...container.querySelectorAll("button")].find((button) => button.textContent?.includes("批准此提案"));
  await act(async () => { approve?.click(); await Promise.resolve(); });
  expect(decide).toHaveBeenCalledWith(proposal.id, "approve", proposal.digest);
});

it("keeps assistant input disabled until a provider is configured", async () => {
  vi.stubGlobal("EventSource", QuietEventSource);
  vi.spyOn(api, "assistantProvider").mockResolvedValue({ apiUrl: "", model: "", apiKeySet: false, allowPrivate: false, status: "disabled" });
  const conversation: AssistantConversation = { id: "conversation-1", title: "未配置", messages: [], runs: [], proposals: [], createdAt: "2026-08-30T13:00:00Z", updatedAt: "2026-08-30T13:00:00Z" };
  vi.spyOn(api, "assistantConversations").mockResolvedValue({ conversations: [conversation] });
  vi.spyOn(api, "assistantConversation").mockResolvedValue(conversation);
  const container = await renderAssistant();
  await vi.waitFor(() => expect(container.textContent).toContain("尚未配置模型服务"));
  expect(container.querySelector<HTMLTextAreaElement>('textarea[aria-label="发送给集群助手的消息"]')?.disabled).toBe(true);
  expect(container.textContent).toContain("系统保管的凭据不会作为聊天内容或工具数据提供给模型");
  const input = container.querySelector("textarea");
  const help = container.querySelector("#assistant-message-security");
  expect(input?.getAttribute("aria-describedby")).toBe(help?.id);
  expect(help?.textContent).toContain("无法识别全部秘密");
  expect(help?.textContent).toContain("通过检查的消息会保存并发送给已配置的模型服务");
});
