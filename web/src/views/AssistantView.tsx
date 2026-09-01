import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";
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
  const [conversation, setConversation] = useState<AssistantConversation | null>(null);
  const [selectedID, setSelectedID] = useState("");
  const [content, setContent] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const refreshTimer = useRef(0);

  const refreshConversation = useCallback(async (id: string, signal?: AbortSignal) => {
    const value = await api.assistantConversation(id, signal);
    setConversation(value);
    setConversations((current) => current.map((item) => item.id === value.id ? { ...item, title: value.title, updatedAt: value.updatedAt } : item));
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([api.assistantProvider(controller.signal), api.assistantConversations(controller.signal)])
      .then(([providerValue, result]) => {
        setProvider(providerValue);
        setConversations(result.conversations);
        setSelectedID((current) => current || result.conversations[0]?.id || "");
      })
      .catch((loadError) => { if (!controller.signal.aborted) setError(userError(language, loadError)); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [language]);

  useEffect(() => {
    if (!selectedID) {
      setConversation(null);
      return;
    }
    const controller = new AbortController();
    void refreshConversation(selectedID, controller.signal).catch((loadError) => {
      if (!controller.signal.aborted) setError(userError(language, loadError));
    });
    return () => controller.abort();
  }, [language, refreshConversation, selectedID]);

  useEffect(() => {
    if (!selectedID) return;
    const source = new EventSource(`/api/v1/assistant/conversations/${encodeURIComponent(selectedID)}/events`, { withCredentials: true });
    const refresh = () => {
      window.clearTimeout(refreshTimer.current);
      refreshTimer.current = window.setTimeout(() => {
        void refreshConversation(selectedID).catch((loadError) => setError(userError(language, loadError)));
      }, 80);
    };
    assistantEventNames.forEach((name) => source.addEventListener(name, refresh));
    source.onerror = () => {
      // EventSource reconnects with Last-Event-ID. The full conversation load below
      // remains authoritative if the browser has to establish a new stream.
    };
    return () => {
      window.clearTimeout(refreshTimer.current);
      assistantEventNames.forEach((name) => source.removeEventListener(name, refresh));
      source.close();
    };
  }, [language, refreshConversation, selectedID]);

  const activeRun = useMemo(() => conversation?.runs.slice().reverse().find((run) => run.status === "queued" || run.status === "running"), [conversation?.runs]);

  const createConversation = async () => {
    setBusy(true); setError("");
    try {
      const created = await api.createAssistantConversation(copy(language, "新对话", "New conversation"));
      setConversations((current) => [created, ...current]);
      setSelectedID(created.id);
      setConversation(created);
    } catch (createError) {
      setError(userError(language, createError));
    } finally {
      setBusy(false);
    }
  };

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const message = content.trim();
    if (!selectedID || !message || busy || activeRun) return;
    setBusy(true); setError("");
    try {
      await api.createAssistantMessage(selectedID, message);
      setContent("");
      await refreshConversation(selectedID);
    } catch (submitError) {
      setError(userError(language, submitError));
    } finally {
      setBusy(false);
    }
  };

  const cancelRun = async () => {
    if (!activeRun) return;
    setBusy(true); setError("");
    try {
      await api.cancelAssistantRun(activeRun.id);
      await refreshConversation(selectedID);
    } catch (cancelError) {
      setError(userError(language, cancelError));
    } finally {
      setBusy(false);
    }
  };

  const actOnProposal = async (proposal: AssistantProposal, action: "approve" | "reject" | "apply") => {
    setBusy(true); setError("");
    try {
      if (action === "apply") await api.applyAssistantProposal(proposal.id, proposal.digest);
      else await api.decideAssistantProposal(proposal.id, action, proposal.digest);
      await refreshConversation(selectedID);
    } catch (proposalError) {
      setError(userError(language, proposalError));
    } finally {
      setBusy(false);
    }
  };

  if (loading) return <div className="flex min-h-72 items-center justify-center"><Spinner className="size-6" /></div>;

  const providerReady = provider?.status === "verified" || provider?.status === "configured";
  return (
    <section className="flex min-w-0 flex-col gap-6">
      <PageHeading title={copy(language, "集群助手", "Cluster assistant")} description={copy(language, "通过受限工具检查集群并创建需要人工审批的变更提案。模型不能直接执行节点操作。", "Inspect the cluster through restricted tools and create changes that require explicit human approval. The model cannot execute node operations directly.")} action={<Button disabled={busy} onClick={() => void createConversation()}><MessageSquarePlusIcon data-icon="inline-start" />{copy(language, "新对话", "New conversation")}</Button>} />
      {!providerReady ? <Alert variant="destructive"><CircleAlertIcon /><AlertTitle>{copy(language, "尚未配置模型服务", "Model provider is not configured")}</AlertTitle><AlertDescription>{copy(language, "请先在“设置”中保存并验证 OpenAI 兼容服务。API Key 只会加密保存在 Center。", "Save and validate an OpenAI-compatible provider in Settings first. The API key is encrypted and stored only by Center.")}</AlertDescription></Alert> : null}
      {error ? <Alert variant="destructive"><CircleAlertIcon /><AlertTitle>{error}</AlertTitle></Alert> : null}
      <div className="grid min-h-[34rem] min-w-0 gap-4 lg:grid-cols-[15rem_minmax(0,1fr)]">
        <Card className="min-w-0 lg:max-h-[44rem]">
          <CardHeader><CardTitle>{copy(language, "对话", "Conversations")}</CardTitle><CardDescription>{copy(language, "只显示当前管理员的记录", "Visible only to the current administrator")}</CardDescription></CardHeader>
          <CardContent className="flex min-h-0 flex-col gap-2 overflow-y-auto">
            {conversations.length === 0 ? <p className="text-sm text-muted-foreground">{copy(language, "还没有对话。", "No conversations yet.")}</p> : conversations.map((item) => <button className={`min-w-0 rounded-lg border px-3 py-2 text-left transition-colors ${selectedID === item.id ? "border-primary bg-primary/5" : "hover:bg-muted/60"}`} key={item.id} onClick={() => setSelectedID(item.id)} type="button"><span className="block truncate font-medium">{item.title}</span><span className="mt-1 block text-xs text-muted-foreground">{formatDate(language, item.updatedAt)}</span></button>)}
          </CardContent>
        </Card>
        <Card className="min-h-[34rem] min-w-0 lg:max-h-[44rem]">
          {conversation ? <>
            <CardHeader className="border-b"><CardTitle className="flex min-w-0 items-center gap-2"><BotIcon className="shrink-0" /><span className="truncate">{conversation.title}</span></CardTitle><CardDescription>{copy(language, "只有审批卡中的按钮能授权变更。聊天中的“确认”不会执行操作。", "Only buttons in a trusted approval card can authorize a change. Saying “confirm” in chat never executes it.")}</CardDescription>{activeRun ? <CardAction><Button disabled={busy} onClick={() => void cancelRun()} size="sm" variant="outline"><CircleStopIcon data-icon="inline-start" />{copy(language, "停止", "Stop")}</Button></CardAction> : null}</CardHeader>
            <CardContent className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto" aria-live="polite">
			  {conversation.messages.length === 0 && conversation.proposals.length === 0 ? <Empty className="my-auto"><EmptyHeader><EmptyMedia variant="icon"><SparklesIcon /></EmptyMedia><EmptyTitle>{copy(language, "询问集群状态或创建受控变更", "Ask about the cluster or create a controlled change")}</EmptyTitle><EmptyDescription>{copy(language, "助手可读取集群状态，并为应用安装或 CPA 密钥轮换生成审批提案；密钥值不会进入模型。", "The assistant can inspect cluster state and propose app installations or CPA credential rotations; credential values never enter the model.")}</EmptyDescription></EmptyHeader></Empty> : null}
              {conversation.messages.map((message) => <div className={`max-w-[88%] rounded-2xl px-4 py-3 leading-6 ${message.role === "user" ? "ml-auto bg-primary text-primary-foreground" : "bg-muted"}`} key={message.id}><p className="whitespace-pre-wrap break-words">{message.content}</p><span className={`mt-1 block text-[0.68rem] ${message.role === "user" ? "text-primary-foreground/70" : "text-muted-foreground"}`}>{formatDate(language, message.createdAt)}</span></div>)}
              {conversation.proposals.map((proposal) => <ProposalCard busy={busy} key={proposal.id} language={language} onAction={(action) => void actOnProposal(proposal, action)} proposal={proposal} />)}
              {activeRun ? <div className="flex items-center gap-2 text-sm text-muted-foreground"><Spinner />{copy(language, "助手正在处理…", "Assistant is working…")}</div> : null}
              {conversation.runs.filter((run) => run.status === "failed").slice(-1).map((run) => <Alert key={run.id} variant="destructive"><CircleAlertIcon /><AlertTitle>{copy(language, "请求失败", "Request failed")}</AlertTitle><AlertDescription>{run.lastError || copy(language, "模型服务没有完成响应。", "The model provider did not complete the response.")}</AlertDescription></Alert>)}
            </CardContent>
            <CardFooter>
              <form className="flex w-full min-w-0 items-end gap-2" onSubmit={(event) => void submit(event)}>
				<Textarea aria-label={copy(language, "发送给集群助手的消息", "Message to the cluster assistant")} className="max-h-36 min-h-11 resize-y" disabled={!providerReady || busy || Boolean(activeRun)} maxLength={8000} onChange={(event) => setContent(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} placeholder={copy(language, "例如：列出离线节点、安装 CPA，或轮换 CPA 客户端密钥", "For example: list offline nodes, install CPA, or rotate a CPA client key")} value={content} />
                <Button aria-label={copy(language, "发送", "Send")} disabled={!providerReady || busy || Boolean(activeRun) || !content.trim()} size="icon" type="submit">{busy ? <Spinner /> : <SendIcon />}</Button>
              </form>
            </CardFooter>
          </> : <Empty><EmptyHeader><EmptyMedia variant="icon"><BotIcon /></EmptyMedia><EmptyTitle>{copy(language, "创建一个对话", "Create a conversation")}</EmptyTitle><EmptyDescription>{copy(language, "对话、工具调用、提案和执行结果都会保存在 Center 审计链中。", "Conversations, tool calls, proposals, and execution results are retained in Center's audit trail.")}</EmptyDescription></EmptyHeader><EmptyContent><Button disabled={busy} onClick={() => void createConversation()}><MessageSquarePlusIcon data-icon="inline-start" />{copy(language, "创建对话", "Create conversation")}</Button></EmptyContent></Empty>}
        </Card>
      </div>
    </section>
  );
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
