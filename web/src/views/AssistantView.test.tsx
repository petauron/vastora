// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api";
import { ThemeProvider } from "../components/theme";
import type { AssistantConversation, AssistantProposal, AssistantRun } from "../types";
import { AssistantView } from "./AssistantView";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  QuietEventSource.instances = [];
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

class QuietEventSource extends EventTarget {
  static instances: QuietEventSource[] = [];
  closed = false;
  constructor(readonly url: string, readonly init?: EventSourceInit) {
    super();
    QuietEventSource.instances.push(this);
  }
  close() { this.closed = true; }
  emit(name: string) { if (!this.closed) this.dispatchEvent(new Event(name)); }
  onerror: ((event: Event) => void) | null = null;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<T>((onResolve, onReject) => { resolve = onResolve; reject = onReject; });
  return { promise, resolve, reject };
}

const timestamp = "2026-09-12T00:00:00Z";

function conversationFixture(id: string): AssistantConversation {
  return {
    id, title: `Conversation ${id}`, createdAt: timestamp, updatedAt: timestamp,
    messages: [{ id: `${id}-message`, role: "user", content: `Visible message in ${id}`, createdAt: timestamp }],
    proposals: [], runs: []
  };
}

function runFixture(conversationId: string, status: AssistantRun["status"] = "running"): AssistantRun {
  return { id: `${conversationId}-run`, conversationId, status, createdAt: timestamp, updatedAt: timestamp };
}

function proposalFixture(conversationId: string, status: AssistantProposal["status"] = "pending"): AssistantProposal {
  return {
    id: `${conversationId}-proposal`, conversationId, runId: `${conversationId}-run`,
    kind: "install_application", status, risk: "medium", digest: conversationId.repeat(64),
    summary: { appKey: "vastora-official/3x-ui", agentId: `${conversationId}-node`, agentName: `${conversationId} target` },
    targets: [{ kind: "agent", id: `${conversationId}-node` }], expectedRevision: "revision-1", policyVersion: "policy-1",
    expiresAt: "2099-01-01T00:00:00Z", createdAt: timestamp, updatedAt: timestamp
  };
}

function mockConversations(conversations: AssistantConversation[]) {
  vi.stubGlobal("EventSource", QuietEventSource);
  vi.spyOn(api, "assistantProvider").mockResolvedValue({ apiUrl: "https://provider.example/v1", model: "model", apiKeySet: true, allowPrivate: false, status: "verified" });
  vi.spyOn(api, "assistantConversations").mockResolvedValue({ conversations });
  return vi.spyOn(api, "assistantConversation").mockImplementation(async (id) => {
    const value = conversations.find((item) => item.id === id);
    if (!value) throw new Error(`Unexpected conversation: ${id}`);
    return value;
  });
}

function messageInput(container: HTMLElement): HTMLTextAreaElement {
  const input = container.querySelector<HTMLTextAreaElement>('textarea[aria-label="发送给集群助手的消息"]');
  if (!input) throw new Error("Assistant message input is missing");
  return input;
}

function buttonNamed(container: HTMLElement, label: string): HTMLButtonElement {
  const button = [...container.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.getAttribute("aria-label") === label || item.textContent?.trim() === label);
  if (!button) throw new Error(`Missing button: ${label}`);
  return button;
}

async function selectConversation(container: HTMLElement, id: string) {
  const button = [...container.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.textContent?.startsWith(`Conversation ${id}`));
  if (!button) throw new Error(`Missing conversation: ${id}`);
  await act(async () => { button.click(); });
}

function enterMessage(container: HTMLElement, content: string) {
  const input = messageInput(container);
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, content);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  return input;
}

async function clickButton(container: HTMLElement, label: string) {
  await act(async () => { buttonNamed(container, label).click(); });
}

function keyboard(input: HTMLTextAreaElement, type: "keydown" | "keyup", init: KeyboardEventInit = {}) {
  const event = new KeyboardEvent(type, { key: "Enter", bubbles: true, cancelable: true, ...init });
  input.dispatchEvent(event);
  return event;
}

it("renders the exact proposal and uses only the trusted approval action", async () => {
  vi.stubGlobal("EventSource", QuietEventSource);
  const proposal = {
    id: "proposal-1",
    conversationId: "conversation-1",
    runId: "run-1",
    kind: "install_application" as const,
    summary: { action: "install", agentId: "agent-1", agentName: "上海节点", appKey: "vastora-official/3x-ui", appName: { en: "3x-ui", "zh-CN": "3x-ui" }, version: "3.7.0", impact: "Install one app", dataRetention: "Data is retained" },
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
  expect(container.textContent).toContain("3.7.0");
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

it.each(["success", "failure"] as const)("ignores an old conversation's late SSE refresh %s after selecting another conversation", async (outcome) => {
  const a = conversationFixture("A");
  const b = conversationFixture("B");
  const load = mockConversations([a, b]);
  const container = await renderAssistant();
  expect(container.textContent).toContain(a.messages[0].content);
  const stale = deferred<AssistantConversation>();
  // Deliberately ignore AbortSignal: even a transport that resolves after abort
  // must not be allowed to replace the selected conversation.
  load.mockImplementation((id) => id === a.id ? stale.promise : Promise.resolve(b));
  vi.useFakeTimers();
  await act(async () => {
    const source = QuietEventSource.instances.find((item) => item.url.includes("/A/events"));
    if (!source) throw new Error("Conversation A event stream is missing");
    source.emit("message.delta");
    await vi.advanceTimersByTimeAsync(80);
  });
  expect(load.mock.calls.filter(([id]) => id === a.id)).toHaveLength(2);
  await selectConversation(container, b.id);
  expect(container.textContent).toContain(b.messages[0].content);
  enterMessage(container, "Draft belongs to B");
  const beforeStaleResponse = container.textContent;
  await act(async () => {
    if (outcome === "success") stale.resolve(a);
    else stale.reject(new Error("Old A refresh failed"));
  });
  expect(container.textContent).toBe(beforeStaleResponse);
  expect(container.textContent).not.toContain(a.messages[0].content);
  expect(messageInput(container).value).toBe("Draft belongs to B");
  expect(messageInput(container).disabled).toBe(false);
});

it("does not reuse an old request lifetime after switching A to B and back to A", async () => {
  const a = conversationFixture("A");
  const b = conversationFixture("B");
  const load = mockConversations([a, b]);
  const container = await renderAssistant();
  const staleA = deferred<AssistantConversation>();
  const freshA = { ...a, messages: [{ ...a.messages[0], content: "Fresh A after returning" }] };
  let aRefreshCount = 0;
  load.mockImplementation((id) => {
    if (id === b.id) return Promise.resolve(b);
    aRefreshCount += 1;
    return aRefreshCount === 1 ? staleA.promise : Promise.resolve(freshA);
  });
  vi.useFakeTimers();
  await act(async () => {
    const source = QuietEventSource.instances.find((item) => item.url.includes("/A/events"));
    if (!source) throw new Error("Conversation A event stream is missing");
    source.emit("message.delta");
    await vi.advanceTimersByTimeAsync(80);
  });
  await selectConversation(container, b.id);
  await selectConversation(container, a.id);
  expect(container.textContent).toContain("Fresh A after returning");
  enterMessage(container, "New A lifetime draft");
  await act(async () => { staleA.resolve(a); });
  expect(container.textContent).toContain("Fresh A after returning");
  expect(container.textContent).not.toContain(a.messages[0].content);
  expect(messageInput(container).value).toBe("New A lifetime draft");
  expect(container.querySelector('[aria-current="true"]')?.textContent).toContain(a.title);
});

it("keeps the newest GET result when two refreshes of the same conversation finish in reverse order", async () => {
  const a = conversationFixture("A");
  const load = mockConversations([a]);
  const container = await renderAssistant();
  const older = deferred<AssistantConversation>();
  const newer = deferred<AssistantConversation>();
  load.mockImplementationOnce(() => older.promise).mockImplementationOnce(() => newer.promise);
  const source = QuietEventSource.instances.find((item) => item.url.includes("/A/events"));
  if (!source) throw new Error("Conversation A event stream is missing");
  vi.useFakeTimers();
  await act(async () => {
    source.emit("message.delta");
    await vi.advanceTimersByTimeAsync(80);
    source.emit("run.completed");
    await vi.advanceTimersByTimeAsync(80);
  });
  expect(load).toHaveBeenCalledTimes(3);
  await act(async () => {
    newer.resolve({ ...a, messages: [{ ...a.messages[0], content: "Newest server snapshot" }] });
  });
  expect(container.textContent).toContain("Newest server snapshot");
  await act(async () => {
    older.resolve({ ...a, messages: [{ ...a.messages[0], content: "Obsolete server snapshot" }] });
  });
  expect(container.textContent).toContain("Newest server snapshot");
  expect(container.textContent).not.toContain("Obsolete server snapshot");
});

it("adds a late newly created conversation to the list without stealing the user's newer selection", async () => {
  const a = conversationFixture("A");
  const b = conversationFixture("B");
  const created = conversationFixture("C");
  const load = mockConversations([a, b]);
  const creation = deferred<AssistantConversation>();
  const create = vi.spyOn(api, "createAssistantConversation").mockReturnValue(creation.promise);
  const container = await renderAssistant();
  await clickButton(container, "新对话");
  expect(create).toHaveBeenCalledExactlyOnceWith("新对话");
  await selectConversation(container, b.id);
  enterMessage(container, "Keep B selected and keep this draft");
  await act(async () => { creation.resolve(created); });
  expect(container.textContent).toContain(created.title);
  expect(container.textContent).toContain(b.messages[0].content);
  expect(container.textContent).not.toContain(created.messages[0].content);
  expect(container.querySelector('[aria-current="true"]')?.textContent).toContain(b.title);
  expect(messageInput(container).value).toBe("Keep B selected and keep this draft");
  expect(load.mock.calls.some(([id]) => id === created.id)).toBe(false);
});

it("clears an initial conversation read error after a successful reload", async () => {
  const a = conversationFixture("A");
  const load = mockConversations([a]);
  const firstRead = deferred<AssistantConversation>();
  load.mockImplementationOnce(() => firstRead.promise);
  const container = await renderAssistant();
  await act(async () => { firstRead.reject(new Error("Failed to fetch")); });
  expect(container.querySelector('[role="alert"]')?.textContent).toContain("无法连接 Center，请检查网络后重试。");
  expect(container.textContent).not.toContain(a.messages[0].content);
  await clickButton(container, "重新加载对话");
  expect(load).toHaveBeenCalledTimes(2);
  expect(container.textContent).toContain(a.messages[0].content);
  expect(container.querySelector('[role="alert"]')).toBeNull();
  expect(messageInput(container).disabled).toBe(false);
});

it("preserves a mutation failure when a later SSE refresh succeeds", async () => {
  const a = conversationFixture("A");
  const load = mockConversations([a]);
  const mutation = deferred<AssistantRun>();
  vi.spyOn(api, "createAssistantMessage").mockReturnValue(mutation.promise);
  const container = await renderAssistant();
  enterMessage(container, "Keep the failed request draft");
  await clickButton(container, "发送");
  await act(async () => { mutation.reject(new Error("Mutation timed out")); });
  const failureText = "操作等待超时，系统可能仍在后台处理。请稍后刷新后重试。";
  expect(container.querySelector('[role="alert"]')?.textContent).toContain(failureText);
  load.mockResolvedValue({ ...a, messages: [{ ...a.messages[0], content: "Fresh snapshot after operation failure" }] });
  vi.useFakeTimers();
  await act(async () => {
    const source = QuietEventSource.instances.find((item) => item.url.includes("/A/events"));
    if (!source) throw new Error("Conversation A event stream is missing");
    source.emit("message.delta");
    await vi.advanceTimersByTimeAsync(80);
  });
  expect(load).toHaveBeenCalledTimes(2);
  expect(container.textContent).toContain("Fresh snapshot after operation failure");
  expect(container.querySelector('[role="alert"]')?.textContent).toContain(failureText);
  expect(messageInput(container).value).toBe("Keep the failed request draft");
});

it("removes old approval and run actions while the newly selected conversation is loading", async () => {
  const a = conversationFixture("A");
  a.runs = [runFixture(a.id)];
  a.proposals = [proposalFixture(a.id)];
  const b = conversationFixture("B");
  const load = mockConversations([a, b]);
  const pendingB = deferred<AssistantConversation>();
  load.mockImplementation((id) => id === b.id ? pendingB.promise : Promise.resolve(a));
  const cancel = vi.spyOn(api, "cancelAssistantRun").mockResolvedValue({ cancelled: true });
  const decide = vi.spyOn(api, "decideAssistantProposal").mockResolvedValue(a.proposals[0]);
  const send = vi.spyOn(api, "createAssistantMessage").mockResolvedValue(runFixture(b.id));
  const container = await renderAssistant();
  expect(container.textContent).toContain(a.messages[0].content);
  await selectConversation(container, b.id);
  expect(container.textContent).not.toContain(a.messages[0].content);
  expect(container.textContent).not.toContain("A target");
  for (const label of ["停止", "批准此提案", "发送"]) {
    const button = [...container.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.getAttribute("aria-label") === label || item.textContent?.trim() === label);
    if (button) expect(button.disabled).toBe(true);
  }
  expect(cancel).not.toHaveBeenCalled();
  expect(decide).not.toHaveBeenCalled();
  expect(send).not.toHaveBeenCalled();
  await act(async () => { pendingB.resolve(b); });
  enterMessage(container, "Request for B");
  await clickButton(container, "发送");
  expect(send).toHaveBeenCalledWith(b.id, "Request for B");
});

const staleOperationCases = (["send", "cancel", "approve", "reject", "apply"] as const)
  .flatMap((action) => (["success", "failure"] as const).map((outcome) => ({ action, outcome })));

it.each(staleOperationCases)("isolates a late $action $outcome from the new conversation's content and busy state", async ({ action, outcome }) => {
  const a = conversationFixture("A");
  if (action === "cancel") a.runs = [runFixture(a.id)];
  const proposal = proposalFixture(a.id, action === "apply" ? "approved" : "pending");
  if (action === "approve" || action === "reject" || action === "apply") a.proposals = [proposal];
  const b = conversationFixture("B");
  mockConversations([a, b]);
  const oldOperation = deferred<void>();
  const newOperation = deferred<AssistantRun>();
  const send = vi.spyOn(api, "createAssistantMessage").mockImplementation((id) => id === a.id
    ? oldOperation.promise.then(() => runFixture(a.id)) : newOperation.promise);
  const cancel = vi.spyOn(api, "cancelAssistantRun").mockImplementation(() => oldOperation.promise.then(() => ({ cancelled: true })));
  const decide = vi.spyOn(api, "decideAssistantProposal").mockImplementation(() => oldOperation.promise.then((): AssistantProposal => ({ ...proposal, status: action === "reject" ? "rejected" : "approved" })));
  const apply = vi.spyOn(api, "applyAssistantProposal").mockImplementation(() => oldOperation.promise.then(() => ({ id: "A-execution", kind: "install_application" as const, state: "pending" })));
  const container = await renderAssistant();
  if (action === "send") {
    enterMessage(container, "Request for A");
    await clickButton(container, "发送");
    expect(send).toHaveBeenCalledWith(a.id, "Request for A");
  } else if (action === "cancel") {
    await clickButton(container, "停止");
    expect(cancel).toHaveBeenCalledWith(a.runs[0].id);
  } else if (action === "apply") {
    await clickButton(container, "执行已批准变更");
    expect(apply).toHaveBeenCalledWith(proposal.id, proposal.digest);
  } else {
    await clickButton(container, action === "approve" ? "批准此提案" : "拒绝");
    expect(decide).toHaveBeenCalledWith(proposal.id, action, proposal.digest);
  }
  await selectConversation(container, b.id);
  expect(container.textContent).toContain(b.messages[0].content);
  expect(messageInput(container).disabled).toBe(false);
  enterMessage(container, "Request for B");
  await clickButton(container, "发送");
  expect(send).toHaveBeenCalledWith(b.id, "Request for B");
  expect(messageInput(container).disabled).toBe(true);
  const beforeOldCompletion = container.textContent;
  const draftBeforeOldCompletion = messageInput(container).value;
  await act(async () => {
    if (outcome === "success") oldOperation.resolve();
    else oldOperation.reject(new Error("Old A operation failed"));
  });
  expect(container.textContent).toBe(beforeOldCompletion);
  expect(container.textContent).not.toContain(a.messages[0].content);
  expect(messageInput(container).value).toBe(draftBeforeOldCompletion);
  // A's finally block must not unlock B while B's own request is still pending.
  expect(messageInput(container).disabled).toBe(true);
  expect(buttonNamed(container, "发送").disabled).toBe(true);
  await act(async () => { newOperation.resolve(runFixture(b.id, "completed")); });
});

it("does not submit candidate-confirmation Enter while native IME composition is active", async () => {
  const conversation = conversationFixture("A");
  mockConversations([conversation]);
  const send = vi.spyOn(api, "createAssistantMessage").mockResolvedValue(runFixture(conversation.id, "completed"));
  const container = await renderAssistant();
  const input = enterMessage(container, "尚未发送的中文");
  act(() => {
    input.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true, data: "中" }));
    keyboard(input, "keydown", { isComposing: true, keyCode: 229 });
    input.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: "中文" }));
    keyboard(input, "keyup");
  });
  expect(send).not.toHaveBeenCalled();
  expect(messageInput(container).value).toBe("尚未发送的中文");
  await act(async () => { keyboard(input, "keydown"); });
  expect(send).toHaveBeenCalledExactlyOnceWith(conversation.id, "尚未发送的中文");
});

it("uses composition lifecycle events when the browser reports isComposing false on confirmation Enter", async () => {
  const conversation = conversationFixture("A");
  mockConversations([conversation]);
  const send = vi.spyOn(api, "createAssistantMessage").mockResolvedValue(runFixture(conversation.id, "completed"));
  const container = await renderAssistant();
  const input = enterMessage(container, "输入法候选词");
  act(() => {
    input.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true, data: "候选词" }));
    keyboard(input, "keydown", { isComposing: false, keyCode: 13 });
    input.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: "候选词" }));
    keyboard(input, "keyup");
  });
  expect(send).not.toHaveBeenCalled();
  await act(async () => { keyboard(input, "keydown"); });
  expect(send).toHaveBeenCalledExactlyOnceWith(conversation.id, "输入法候选词");
});

it("ignores a Safari-style keyCode 229 Enter after compositionend without swallowing the next ordinary Enter", async () => {
  const conversation = conversationFixture("A");
  mockConversations([conversation]);
  const send = vi.spyOn(api, "createAssistantMessage").mockResolvedValue(runFixture(conversation.id, "completed"));
  const container = await renderAssistant();
  const input = enterMessage(container, "确认中文后再发送");
  act(() => {
    input.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true, data: "中文" }));
    input.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: "中文" }));
    keyboard(input, "keydown", { isComposing: false, keyCode: 229 });
    keyboard(input, "keyup");
  });
  expect(send).not.toHaveBeenCalled();
  await act(async () => { keyboard(input, "keydown", { keyCode: 13 }); });
  expect(send).toHaveBeenCalledExactlyOnceWith(conversation.id, "确认中文后再发送");
});

it("keeps Shift+Enter available for line breaks and sends only an ordinary Enter", async () => {
  const conversation = conversationFixture("A");
  mockConversations([conversation]);
  const send = vi.spyOn(api, "createAssistantMessage").mockResolvedValue(runFixture(conversation.id, "completed"));
  const container = await renderAssistant();
  const input = enterMessage(container, "第一行\n第二行");
  act(() => {
    const lineBreak = keyboard(input, "keydown", { shiftKey: true });
    expect(lineBreak.defaultPrevented).toBe(false);
  });
  expect(send).not.toHaveBeenCalled();
  await act(async () => { keyboard(input, "keydown"); });
  expect(send).toHaveBeenCalledExactlyOnceWith(conversation.id, "第一行\n第二行");
});

it("sends the first ordinary Enter after choosing an IME candidate with the mouse", async () => {
  const conversation = conversationFixture("A");
  mockConversations([conversation]);
  const send = vi.spyOn(api, "createAssistantMessage").mockResolvedValue(runFixture(conversation.id, "completed"));
  const container = await renderAssistant();
  const input = enterMessage(container, "鼠标选词后发送");
  act(() => {
    input.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true, data: "选词" }));
    // Mouse candidate selection ends composition without a confirmation Enter.
    input.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: "选词" }));
  });
  expect(send).not.toHaveBeenCalled();
  await act(async () => { keyboard(input, "keydown", { keyCode: 13 }); });
  expect(send).toHaveBeenCalledExactlyOnceWith(conversation.id, "鼠标选词后发送");
});

it("does not submit key-repeat Enter events", async () => {
  const conversation = conversationFixture("A");
  mockConversations([conversation]);
  const send = vi.spyOn(api, "createAssistantMessage").mockResolvedValue(runFixture(conversation.id, "completed"));
  const container = await renderAssistant();
  const input = enterMessage(container, "不要因长按回车重复发送");
  act(() => { keyboard(input, "keydown", { repeat: true }); });
  expect(send).not.toHaveBeenCalled();
  await act(async () => { keyboard(input, "keydown", { repeat: false }); });
  expect(send).toHaveBeenCalledExactlyOnceWith(conversation.id, "不要因长按回车重复发送");
});
