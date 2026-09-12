import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import { BotIcon, CircleAlertIcon, CircleStopIcon, MessageSquarePlusIcon, SendIcon, ShieldCheckIcon, SparklesIcon, XIcon } from "lucide-react";
import { api } from "../api";
import type { AssistantConversation, AssistantProposal, AssistantProvider } from "../types";
import type { Language } from "../translations";
import { PageHeading, StateBadge, copy, formatDate, userError } from "./shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";

const assistantEventNames = [
  "message.delta",
  "tool.started",
  "tool.completed",
  "proposal.created",
  "approval.required",
  "proposal.approved",
  "proposal.rejected",
  "execution.queued",
  "execution.pending",
  "execution.running",
  "execution.completed",
  "execution.failed",
  "run.completed",
  "run.failed",
  "run.cancelled"
] as const;

export function AssistantView({ language }: { language: Language }) {
  const [provider, setProvider] = useState<AssistantProvider | null>(null);
  const [conversations, setConversations] = useState<AssistantConversation[]>([]);
  const [selectedID, setSelectedID] = useState("");
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState("");
  const selectionVersion = useRef(0);
  const creationRequest = useRef<AbortController | null>(null);

  const updateSummary = useCallback((value: AssistantConversation) => {
    setConversations((current) => current.map((item) => item.id === value.id ? { ...item, title: value.title, updatedAt: value.updatedAt } : item));
  }, []);

  const selectConversation = (id: string) => {
    selectionVersion.current += 1;
    setSelectedID(id);
    setError("");
  };

  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([api.assistantProvider(controller.signal), api.assistantConversations(controller.signal)])
      .then(([providerValue, result]) => {
        if (controller.signal.aborted) return;
        setProvider(providerValue);
        setConversations(result.conversations);
        setSelectedID((current) => current || result.conversations[0]?.id || "");
      })
      .catch((loadError) => { if (!controller.signal.aborted) setError(userError(language, loadError)); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [language]);

  useEffect(() => () => creationRequest.current?.abort(), []);

  const createConversation = async () => {
    if (creationRequest.current) return;
    const controller = new AbortController();
    const selection = selectionVersion.current;
    creationRequest.current = controller;
    setCreating(true); setError("");
    try {
      const created = await api.createAssistantConversation(copy(language, "新对话", "New conversation"));
      if (controller.signal.aborted) return;
      setConversations((current) => [created, ...current]);
      if (selectionVersion.current === selection) selectConversation(created.id);
    } catch (createError) {
      if (!controller.signal.aborted && selectionVersion.current === selection) setError(userError(language, createError));
    } finally {
      if (!controller.signal.aborted) {
        creationRequest.current = null;
        setCreating(false);
      }
    }
  };

  if (loading) return <div className="flex min-h-72 items-center justify-center"><Spinner className="size-6" /></div>;

  const providerReady = provider?.status === "verified" || provider?.status === "configured";
  return (
    <section className="flex min-w-0 flex-col gap-6">
      <PageHeading title={copy(language, "集群助手", "Cluster assistant")} description={copy(language, "通过受限工具检查集群并创建需要人工审批的变更提案。模型不能直接执行节点操作。", "Inspect the cluster through restricted tools and create changes that require explicit human approval. The model cannot execute node operations directly.")} action={<Button disabled={creating} onClick={() => void createConversation()}><MessageSquarePlusIcon data-icon="inline-start" />{copy(language, "新对话", "New conversation")}</Button>} />
      {!providerReady ? <Alert variant="destructive"><CircleAlertIcon /><AlertTitle>{copy(language, "尚未配置模型服务", "Model provider is not configured")}</AlertTitle><AlertDescription>{copy(language, "请先在“设置”中保存并验证 OpenAI 兼容服务。API Key 只会加密保存在 Center。", "Save and validate an OpenAI-compatible provider in Settings first. The API key is encrypted and stored only by Center.")}</AlertDescription></Alert> : null}
      {error ? <Alert variant="destructive"><CircleAlertIcon /><AlertTitle>{error}</AlertTitle></Alert> : null}
      <div className="grid min-h-[34rem] min-w-0 gap-4 lg:grid-cols-[15rem_minmax(0,1fr)]">
        <Card className="min-w-0 lg:max-h-[44rem]">
          <CardHeader><CardTitle>{copy(language, "对话", "Conversations")}</CardTitle><CardDescription>{copy(language, "只显示当前管理员的记录", "Visible only to the current administrator")}</CardDescription></CardHeader>
          <CardContent className="flex min-h-0 flex-col gap-2 overflow-y-auto">
            {conversations.length === 0 ? <p className="text-sm text-muted-foreground">{copy(language, "还没有对话。", "No conversations yet.")}</p> : conversations.map((item) => <button aria-current={selectedID === item.id ? "true" : undefined} className={`min-w-0 rounded-lg border px-3 py-2 text-left transition-colors ${selectedID === item.id ? "border-primary bg-primary/5" : "hover:bg-muted/60"}`} key={item.id} onClick={() => selectConversation(item.id)} type="button"><span className="block truncate font-medium">{item.title}</span><span className="mt-1 block text-xs text-muted-foreground">{formatDate(language, item.updatedAt)}</span></button>)}
          </CardContent>
        </Card>
        {selectedID ? <AssistantConversationPanel key={`${selectedID}:${language}`} id={selectedID} language={language} onUpdate={updateSummary} providerReady={providerReady} /> : <Card className="min-h-[34rem] min-w-0 lg:max-h-[44rem]"><Empty><EmptyHeader><EmptyMedia variant="icon"><BotIcon /></EmptyMedia><EmptyTitle>{copy(language, "创建一个对话", "Create a conversation")}</EmptyTitle><EmptyDescription>{copy(language, "对话、工具调用、提案和执行结果都会保存在 Center 审计链中。", "Conversations, tool calls, proposals, and execution results are retained in Center's audit trail.")}</EmptyDescription></EmptyHeader><EmptyContent><Button disabled={creating} onClick={() => void createConversation()}><MessageSquarePlusIcon data-icon="inline-start" />{copy(language, "创建对话", "Create conversation")}</Button></EmptyContent></Empty></Card>}
      </div>
    </section>
  );
}

// A keyed panel owns every request, action and draft for exactly one selection.
// Returning to the same conversation creates a new lifetime, not a reused scope.
function AssistantConversationPanel({ id, language, onUpdate, providerReady }: { id: string; language: Language; onUpdate: (conversation: AssistantConversation) => void; providerReady: boolean }) {
  const [conversation, setConversation] = useState<AssistantConversation | null>(null);
  const [content, setContent] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [loadError, setLoadError] = useState("");
  const lifetime = useRef<AbortController | null>(null);
  const refreshRequest = useRef<AbortController | null>(null);
  const mutationPending = useRef(false);
  const composing = useRef(false);

  const refreshConversation = useCallback(async () => {
    const scope = lifetime.current;
    if (!scope || scope.signal.aborted) return;
    refreshRequest.current?.abort();
    const controller = new AbortController();
    refreshRequest.current = controller;
    try {
      const value = await api.assistantConversation(id, controller.signal);
      if (scope.signal.aborted || controller.signal.aborted) return;
      if (value.id !== id) throw new Error(copy(language, "对话响应不匹配，请重新加载。", "Conversation response did not match. Reload and try again."));
      setConversation(value);
      setLoadError("");
      onUpdate(value);
    } catch (fetchError) {
      if (!scope.signal.aborted && !controller.signal.aborted) setLoadError(userError(language, fetchError));
    } finally {
      if (!scope.signal.aborted && !controller.signal.aborted) setLoading(false);
    }
  }, [id, language, onUpdate]);

  useEffect(() => {
    const scope = new AbortController();
    lifetime.current = scope;
    void refreshConversation();
    const source = new EventSource(`/api/v1/assistant/conversations/${encodeURIComponent(id)}/events`, { withCredentials: true });
    let timer = 0;
    const refresh = () => {
      window.clearTimeout(timer);
      timer = window.setTimeout(() => { void refreshConversation(); }, 80);
    };
    assistantEventNames.forEach((name) => source.addEventListener(name, refresh));
    return () => {
      scope.abort();
      refreshRequest.current?.abort();
      window.clearTimeout(timer);
      assistantEventNames.forEach((name) => source.removeEventListener(name, refresh));
      source.close();
    };
  }, [id, refreshConversation]);

  const activeRun = useMemo(() => conversation?.runs.slice().reverse().find((run) => run.status === "queued" || run.status === "running"), [conversation?.runs]);
  const ready = conversation?.id === id;

  const mutateConversation = async (operation: () => Promise<unknown>, onSuccess?: () => void) => {
    const scope = lifetime.current;
    if (!ready || !scope || scope.signal.aborted || mutationPending.current) return;
    mutationPending.current = true;
    setBusy(true); setError("");
    try {
      await operation();
      if (scope.signal.aborted) return;
      onSuccess?.();
      await refreshConversation();
    } catch (mutationError) {
      if (!scope.signal.aborted) setError(userError(language, mutationError));
    } finally {
      if (!scope.signal.aborted) {
        mutationPending.current = false;
        setBusy(false);
      }
    }
  };

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const message = content.trim();
    if (!providerReady || !message || busy || activeRun || composing.current) return;
    await mutateConversation(() => api.createAssistantMessage(id, message), () => setContent(""));
  };

  const cancelRun = async () => {
    if (!activeRun || activeRun.conversationId !== id) return;
    await mutateConversation(() => api.cancelAssistantRun(activeRun.id));
  };

  const actOnProposal = async (proposal: AssistantProposal, action: "approve" | "reject" | "apply") => {
    if (proposal.conversationId !== id) return;
    await mutateConversation(() => action === "apply" ? api.applyAssistantProposal(proposal.id, proposal.digest) : api.decideAssistantProposal(proposal.id, action, proposal.digest));
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key !== "Enter" || event.shiftKey) return;
    // WebKit can end composition before this keydown; 229 still identifies
    // the IME confirmation event. Leave it untouched for candidate selection.
    if (composing.current || event.nativeEvent.isComposing || event.nativeEvent.keyCode === 229) return;
    event.preventDefault();
    if (!event.repeat) event.currentTarget.form?.requestSubmit();
  };

  return <Card className="min-h-[34rem] min-w-0 lg:max-h-[44rem]" aria-busy={loading}>
          {error || loadError ? <CardContent><Alert variant="destructive"><CircleAlertIcon /><AlertTitle>{error || loadError}</AlertTitle></Alert></CardContent> : null}
          {conversation ? <>
            <CardHeader className="border-b"><CardTitle className="flex min-w-0 items-center gap-2"><BotIcon className="shrink-0" /><span className="truncate">{conversation.title}</span></CardTitle><CardDescription>{copy(language, "只有审批卡中的按钮能授权变更。聊天中的“确认”不会执行操作。", "Only buttons in a trusted approval card can authorize a change. Saying “confirm” in chat never executes it.")}</CardDescription>{activeRun ? <CardAction><Button disabled={busy} onClick={() => void cancelRun()} size="sm" variant="outline"><CircleStopIcon data-icon="inline-start" />{copy(language, "停止", "Stop")}</Button></CardAction> : null}</CardHeader>
            <CardContent className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto" aria-live="polite">
			  {conversation.messages.length === 0 && conversation.proposals.length === 0 ? <Empty className="my-auto"><EmptyHeader><EmptyMedia variant="icon"><SparklesIcon /></EmptyMedia><EmptyTitle>{copy(language, "询问集群状态或创建受控变更", "Ask about the cluster or create a controlled change")}</EmptyTitle><EmptyDescription>{copy(language, "助手可读取集群状态，并为应用安装或 CPA 密钥轮换生成审批提案；系统保管的凭据不会作为聊天内容或工具数据提供给模型。", "The assistant can inspect cluster state and propose app installations or CPA credential rotations. System-managed credentials are excluded from chat content and tool data sent to the model.")}</EmptyDescription></EmptyHeader></Empty> : null}
              {conversation.messages.map((message) => <div className={`max-w-[88%] rounded-2xl px-4 py-3 leading-6 ${message.role === "user" ? "ml-auto bg-primary text-primary-foreground" : "bg-muted"}`} key={message.id}><p className="whitespace-pre-wrap break-words">{message.content}</p><span className={`mt-1 block text-[0.68rem] ${message.role === "user" ? "text-primary-foreground/70" : "text-muted-foreground"}`}>{formatDate(language, message.createdAt)}</span></div>)}
              {conversation.proposals.map((proposal) => <ProposalCard busy={busy} key={proposal.id} language={language} onAction={(action) => void actOnProposal(proposal, action)} proposal={proposal} />)}
              {activeRun ? <div className="flex items-center gap-2 text-sm text-muted-foreground"><Spinner />{copy(language, "助手正在处理…", "Assistant is working…")}</div> : null}
              {conversation.runs.filter((run) => run.status === "failed").slice(-1).map((run) => <Alert key={run.id} variant="destructive"><CircleAlertIcon /><AlertTitle>{copy(language, "请求失败", "Request failed")}</AlertTitle><AlertDescription>{run.lastError || copy(language, "模型服务没有完成响应。", "The model provider did not complete the response.")}</AlertDescription></Alert>)}
            </CardContent>
			<CardFooter>
			  <form className="flex w-full min-w-0 items-end gap-2" onSubmit={(event) => void submit(event)}>
				<FieldGroup className="min-w-0 flex-1"><Field data-disabled={!providerReady || !ready || busy || Boolean(activeRun)}><FieldLabel className="sr-only" htmlFor="assistant-message">{copy(language, "发送给集群助手的消息", "Message to the cluster assistant")}</FieldLabel><Textarea id="assistant-message" aria-describedby="assistant-message-security" aria-label={copy(language, "发送给集群助手的消息", "Message to the cluster assistant")} autoComplete="off" className="max-h-36 min-h-11 resize-y" disabled={!providerReady || !ready || busy || Boolean(activeRun)} maxLength={8000} onChange={(event) => setContent(event.target.value)} onCompositionStart={() => { composing.current = true; }} onCompositionEnd={() => { composing.current = false; }} onBlur={() => { composing.current = false; }} onKeyDown={handleKeyDown} placeholder={copy(language, "例如：列出离线节点、安装 CPA，或轮换 CPA 客户端密钥", "For example: list offline nodes, install CPA, or rotate a CPA client key")} value={content} /><FieldDescription id="assistant-message-security">{copy(language, "不要粘贴密码、密钥或 Token，请使用专用凭据表单。疑似凭据会被拦截，但无法识别全部秘密；通过检查的消息会保存并发送给已配置的模型服务。", "Do not paste passwords, keys, or tokens; use dedicated credential forms. Suspected credentials are blocked, but not every secret can be detected. Accepted messages are saved and sent to your configured model provider.")}</FieldDescription></Field></FieldGroup>
				<Button aria-label={copy(language, "发送", "Send")} disabled={!providerReady || !ready || busy || Boolean(activeRun) || !content.trim()} size="icon" type="submit">{busy ? <Spinner /> : <SendIcon />}</Button>
			  </form>
            </CardFooter>
          </> : <CardContent className="flex flex-1 items-center justify-center">{loading ? <div className="flex items-center gap-2" role="status"><Spinner />{copy(language, "正在加载对话…", "Loading conversation…")}</div> : <Button onClick={() => { setLoading(true); void refreshConversation(); }} variant="outline">{copy(language, "重新加载对话", "Reload conversation")}</Button>}</CardContent>}
        </Card>;
}

function ProposalCard({ busy, language, onAction, proposal }: { busy: boolean; language: Language; onAction: (action: "approve" | "reject" | "apply") => void; proposal: AssistantProposal }) {
	const isCredentialRotation = proposal.kind === "rotate_cpa_credential";
	const name = proposal.summary.appName ? copy(language, proposal.summary.appName["zh-CN"], proposal.summary.appName.en) : proposal.summary.appKey || copy(language, "应用", "Application");
	const action = isCredentialRotation
		? copy(language, proposal.summary.credentialTarget === "management" ? "轮换管理密钥" : "轮换客户端密钥", proposal.summary.credentialTarget === "management" ? "Rotate management key" : "Rotate client key")
		: `${name} ${proposal.summary.version || "—"}`;
	return <Card className="border-primary/40 bg-primary/[0.025]" size="sm">
    <CardHeader><CardTitle className="flex items-center gap-2"><ShieldCheckIcon />{copy(language, "变更审批", "Change approval")}</CardTitle><CardDescription>{copy(language, "核对下列固定内容。参数、目标、策略或资源状态变化都会使审批失效。", "Review the exact values below. Any parameter, target, policy, or resource-state change invalidates approval.")}</CardDescription><CardAction><Badge variant={proposal.risk === "high" ? "destructive" : "outline"}>{copy(language, `风险：${proposal.risk}`, `Risk: ${proposal.risk}`)}</Badge></CardAction></CardHeader>
		<CardContent><dl className="grid gap-3 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">{copy(language, isCredentialRotation ? "应用与操作" : "应用与版本", isCredentialRotation ? "Application and action" : "Application and version")}</dt><dd className="mt-1 font-medium">{isCredentialRotation ? `${name} · ${action}` : action}</dd></div><div><dt className="text-muted-foreground">{copy(language, "目标节点", "Target node")}</dt><dd className="mt-1 font-medium">{proposal.summary.agentName || proposal.summary.agentId || "—"}</dd></div><div><dt className="text-muted-foreground">{copy(language, "影响", "Impact")}</dt><dd className="mt-1">{proposal.summary.impact || "—"}</dd></div><div><dt className="text-muted-foreground">{copy(language, "数据保留", "Data retention")}</dt><dd className="mt-1">{proposal.summary.dataRetention || "—"}</dd></div><div><dt className="text-muted-foreground">{copy(language, "有效期", "Expires")}</dt><dd className="mt-1">{formatDate(language, proposal.expiresAt)}</dd></div><div><dt className="text-muted-foreground">{copy(language, "状态", "Status")}</dt><dd className="mt-1"><StateBadge language={language} value={proposal.status} /></dd></div></dl><p className="mt-3 break-all font-mono text-[0.68rem] text-muted-foreground">{copy(language, "提案摘要", "Proposal digest")}: {proposal.digest}</p></CardContent>
    {proposal.status === "pending" ? <CardFooter className="flex-wrap justify-end gap-2"><Button disabled={busy} onClick={() => onAction("reject")} variant="outline"><XIcon data-icon="inline-start" />{copy(language, "拒绝", "Reject")}</Button><Button disabled={busy} onClick={() => onAction("approve")}><ShieldCheckIcon data-icon="inline-start" />{copy(language, "批准此提案", "Approve proposal")}</Button></CardFooter> : proposal.status === "approved" ? <CardFooter className="justify-end"><Button disabled={busy} onClick={() => onAction("apply")}><SendIcon data-icon="inline-start" />{copy(language, "执行已批准变更", "Apply approved change")}</Button></CardFooter> : null}
  </Card>;
}
