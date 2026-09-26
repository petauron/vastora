import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import {
  KeyRoundIcon,
  RadioTowerIcon,
  ShieldAlertIcon,
  ShieldCheckIcon,
  UsersIcon,
} from "lucide-react";
import { api } from "../../api";
import type { AppData } from "../../App";
import type {
  AgentView,
  AppView,
  CreatePublicationInput,
  PublicationIngressInput,
  PublicationKind,
  Service,
  ThreeXUIRole,
} from "../../types";
import type { Language } from "../../translations";
import { normalizeHostname, validHostname } from "../../lib/network";
import {
  defaultPublicationHostname,
  eligibleAppNodes,
  gatewaysForKind,
  isActiveApplication,
  localized,
  publicationIntentOptions,
  publicationKindLabel,
  publicationKindsForIntent,
  publicationOptions,
  type PublicationIntent,
} from "../appAccess";
import { AppHostAccessNote, AppIdentityBadge } from "../AppIdentity";
import { catalogInstallBlocked, copy, userError } from "../shared";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { SelectControl } from "@/components/SelectControl";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Checkbox } from "@/components/ui/checkbox";
import { packageNodeBlocker } from "../appAccess";

export type DeploymentEditor = {
  app: AppView;
  agent?: AgentView;
  operation: "install" | "upgrade" | "configure";
} | null;

function capabilityLabel(capability: string, language: Language) {
  const labels: Record<string, [string, string]> = {
    root: ["以 root 用户运行", "Run as root"],
    "host-network": ["使用宿主机网络", "Use host networking"],
    "host-path": ["挂载声明的宿主机路径", "Mount declared host paths"],
    devices: ["访问声明的宿主机设备", "Access declared host devices"],
  };
  const label = labels[capability];
  return label ? copy(language, ...label) : capability;
}

export function DeploymentSheet({
  data,
  editor,
  language,
  onClose,
  onSubmit,
}: {
  data: AppData;
  editor: DeploymentEditor;
  language: Language;
  onClose: () => void;
  onSubmit: (
    agent: AgentView,
    app: AppView,
    config: Record<string, string | boolean | number>,
    operation: "install" | "upgrade" | "configure",
    role?: ThreeXUIRole,
    registryCredentialId?: string,
    authorizedCapabilities?: string[],
  ) => Promise<void>;
}) {
  const [agentID, setAgentID] = useState("");
  const [config, setConfig] = useState<
    Record<string, string | boolean | number>
  >({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [registryCredentialID, setRegistryCredentialID] = useState("");
  const [approvedCapabilities, setApprovedCapabilities] = useState<string[]>([]);
  const requiredCapabilities = editor?.app.app.runtime?.requiredCapabilities ?? [];
  const permissionsApproved = requiredCapabilities.every((capability) => approvedCapabilities.includes(capability));
  const currentCatalogApp = editor
    ? data.apps.find((app) => app.key === editor.app.key)
    : undefined;
  const requiresCatalog = Boolean(editor && editor.operation !== "configure");
  const catalogChanged =
    requiresCatalog &&
    (currentCatalogApp?.app.version !== editor?.app.app.version || currentCatalogApp?.app.packageRevision !== editor?.app.app.packageRevision || currentCatalogApp?.manifestSha256 !== editor?.app.manifestSha256);
  const catalogBlocked =
    requiresCatalog && catalogInstallBlocked(currentCatalogApp);
  const catalogMessage = catalogBlocked
    ? currentCatalogApp?.installBlockedReason || copy(
        language,
        "请先在设置中刷新应用目录，再安装或升级。已安装应用不受影响。",
        "Refresh the app catalog in Settings before installing or upgrading. Installed apps are unaffected.",
      )
    : catalogChanged
      ? copy(
          language,
          "目录中的版本已更新，请关闭并重新打开此窗口，确认新版本后继续。",
          "The catalog version changed. Reopen this window to review the new version before continuing.",
        )
      : "";
  const candidates = editor
    ? editor.operation === "install"
      ? eligibleAppNodes(data, editor.app.key)
      : data.agents.filter((agent) => agent.id === editor.agent?.id)
    : [];
  const selectedAgent = candidates.find((agent) => agent.id === agentID);
  const installed = data.applications.find((application) => application.nodeId === agentID && application.appKey === editor?.app.key);
  const adoptionBlocked = installed?.adoptionState === "pending" || installed?.adoptionState === "blocked";
  const runtimeBlocker = selectedAgent && editor ? packageNodeBlocker(selectedAgent, editor.app, language) : "";
  const retryInstall = editor?.operation === "install" && Boolean(editor.agent);
  const nodeUnavailable = Boolean(agentID) && !selectedAgent;
  const nodeUnavailableMessage = retryInstall
    ? copy(
        language,
        "原节点暂时无法安装，请确认节点在线且满足安装条件后重试。",
        "The original node cannot install this app right now. Check that it is online and meets the installation requirements, then retry.",
      )
    : copy(
        language,
        "所选节点暂时不可用，请重新选择。",
        "The selected node is unavailable. Select a node again.",
      );
  const nodeOptions = [
    {
      value: "",
      label: copy(language, "选择节点", "Select a node"),
      disabled: true,
    },
    ...(retryInstall && editor?.agent && nodeUnavailable
      ? [{ value: editor.agent.id, label: editor.agent.name, disabled: true }]
      : []),
    ...candidates.map((agent) => ({ value: agent.id, label: agent.name })),
  ];
  const isThreeXUIInstall =
    editor?.operation === "install" &&
    editor.app.key === "vastora-official/3x-ui";
  const isPulseHost = editor?.app.key === "vastora-official/pulse";
  const globalController = isThreeXUIInstall
    ? data.applications.find(
        (application) =>
          application.appKey === "vastora-official/3x-ui" &&
          application.role === "master" &&
          application.id === application.controllerApplicationId &&
          isActiveApplication(application.status),
      )
    : undefined;
  const role: ThreeXUIRole | undefined = isThreeXUIInstall
    ? globalController
      ? "worker"
      : "master"
    : undefined;
  const controllerReady =
    !globalController || globalController.status === "running";
  const controllerNode = globalController
    ? data.agents.find((agent) => agent.id === globalController.nodeId)
    : undefined;
  useEffect(() => {
    if (!editor) return;
    const defaults: Record<string, string | boolean | number> = {};
    for (const field of editor.app.app.config) {
      if (editor.app.managedConfigFields?.includes(field.key)) continue;
      if (field.key === "timezone")
        defaults[field.key] =
          Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
      else if (field.default !== undefined) defaults[field.key] = field.default;
      else defaults[field.key] = field.type === "boolean" ? false : "";
    }
    setAgentID(editor.agent?.id ?? candidates[0]?.id ?? "");
    setConfig(editor.operation === "install" ? defaults : {});
    setRegistryCredentialID(
      editor.operation === "install" ? "" : "__preserve__",
    );
    setError("");
    setApprovedCapabilities([]);
  }, [editor]);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!editor || busy) return;
    if (!permissionsApproved || runtimeBlocker || adoptionBlocked) {
      setError(runtimeBlocker || copy(language, "请先完成资源接管，并确认此配方需要的每项权限。", "Complete resource adoption and approve each permission required by this recipe."));
      return;
    }
    if (
      requiresCatalog &&
      (catalogInstallBlocked(currentCatalogApp) || catalogChanged)
    ) {
      setError(
        catalogMessage ||
          copy(
            language,
            "目录需要重新验证，请先刷新应用目录。",
            "Refresh the app catalog to verify it again.",
          ),
      );
      return;
    }
    const agent = selectedAgent;
    if (!agent) {
      setError(
        agentID
          ? nodeUnavailableMessage
          : copy(language, "请选择节点。", "Select a node."),
      );
      return;
    }
    if (!controllerReady) {
      setError(
        copy(
          language,
          "请等待全局订阅主机启动完成。",
          "Wait for the global subscription controller to finish starting.",
        ),
      );
      return;
    }
    setBusy(true);
    setError("");
    try {
      await onSubmit(
        agent,
        requiresCatalog && currentCatalogApp ? currentCatalogApp : editor.app,
        config,
        editor.operation,
        role,
        registryCredentialID === "__preserve__"
          ? undefined
          : registryCredentialID,
        approvedCapabilities,
      );
    } catch (submitError) {
      setError(userError(language, submitError));
    } finally {
      setBusy(false);
    }
  };
  const verbs =
    editor?.operation === "install"
      ? ["安装", "Install"]
      : editor?.operation === "upgrade"
        ? ["升级", "Upgrade"]
        : ["修改配置", "Change settings"];
  return (
    <Sheet
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      open={Boolean(editor)}
    >
      <SheetContent className="sm:max-w-lg">
        <SheetHeader>
          <SheetTitle className="flex flex-wrap items-center gap-2">
            {editor
              ? `${copy(language, verbs[0], verbs[1])} ${localized(editor.app, language, "name")}`
              : ""}
            {editor ? (
              <AppIdentityBadge app={editor.app} language={language} />
            ) : null}
          </SheetTitle>
          <SheetDescription>
            {editor?.operation === "install" && editor.app.app.hostAccess
              ? localized(editor.app, language, "description")
              : editor?.operation === "install"
                ? copy(
                    language,
                    "应用先作为私有源站启动；访问入口稍后单独添加。",
                    "The app starts as a private origin. Add access points separately afterward.",
                  )
                : editor?.operation === "upgrade"
                  ? copy(
                      language,
                      `版本：${data.applications.find((application) => application.nodeId === editor.agent?.id && application.appKey === editor.app.key)?.installedVersion ?? "—"} → ${editor.app.app.version}。确认后更新，可保留现有配置。`,
                      `Version: ${data.applications.find((application) => application.nodeId === editor.agent?.id && application.appKey === editor.app.key)?.installedVersion ?? "—"} → ${editor.app.app.version}. Confirm to update; existing settings can be kept.`,
                    )
                  : copy(
                      language,
                      "只填写至少一项要修改的配置；留空项保持原值。",
                      "Enter at least one setting to change; omitted values keep their previous value.",
                    )}
          </SheetDescription>
        </SheetHeader>
        <form
          className="flex min-h-0 flex-1 flex-col"
          onSubmit={(event) => void submit(event)}
        >
          <div className="flex-1 overflow-y-auto px-4">
            <FieldGroup>
              {adoptionBlocked || runtimeBlocker ? <Alert role="status"><AlertTitle>{copy(language, "暂不能修改此应用", "Application changes are blocked")}</AlertTitle><AlertDescription>{runtimeBlocker || installed?.adoptionError || copy(language, "维护窗口内核实并接管现有资源后才能修改。接管不会重装或重启应用。", "Verify and adopt existing resources during maintenance before changing them. Adoption does not reinstall or restart the application.")}</AlertDescription></Alert> : null}
              {editor ? <FieldDescription>{copy(language, `配方修订：${installed?.installedPackageRevision ?? 0} → ${editor.app.app.packageRevision ?? 0}`, `Recipe revision: ${installed?.installedPackageRevision ?? 0} → ${editor.app.app.packageRevision ?? 0}`)}</FieldDescription> : null}
              {requiredCapabilities.length > 0 ? <FieldSet><FieldLegend>{copy(language, "此配方需要管理员授权", "This recipe requires administrator approval")}</FieldLegend><FieldDescription>{copy(language, "逐项确认。升级不会自动批准新增权限。", "Approve each permission. Upgrades never automatically approve new privileges.")}</FieldDescription><FieldGroup>{requiredCapabilities.map((capability) => <Field orientation="horizontal" key={capability}><Checkbox id={`package-capability-${capability}`} checked={approvedCapabilities.includes(capability)} onCheckedChange={(checked) => setApprovedCapabilities((current) => checked ? [...current, capability] : current.filter((value) => value !== capability))} /><FieldLabel htmlFor={`package-capability-${capability}`}>{capabilityLabel(capability, language)}</FieldLabel></Field>)}</FieldGroup></FieldSet> : null}
              {editor?.app.permissionDetails?.length ? <FieldDescription>{editor.app.permissionDetails.map((detail) => <span className="block break-all font-mono text-xs" key={detail}>{detail}</span>)}</FieldDescription> : null}
              {catalogMessage ? (
                <Alert role="status" id="deployment-catalog-error">
                  <AlertTitle>
                    {copy(language, "暂不能继续", "Cannot continue yet")}
                  </AlertTitle>
                  <AlertDescription>{catalogMessage}</AlertDescription>
                </Alert>
              ) : null}
              {editor ? (
                <AppHostAccessNote app={editor.app} language={language} />
              ) : null}
              <Field data-invalid={nodeUnavailable}>
                <FieldLabel htmlFor="deployment-agent">
                  {copy(language, "节点", "Node")}
                </FieldLabel>
                <SelectControl
                  aria-describedby={
                    nodeUnavailable
                      ? "deployment-agent-error"
                      : "deployment-agent-help"
                  }
                  aria-invalid={nodeUnavailable}
                  disabled={editor?.operation !== "install" || retryInstall}
                  id="deployment-agent"
                  onValueChange={setAgentID}
                  options={nodeOptions}
                  required
                  value={agentID}
                />
                <FieldDescription id="deployment-agent-help">
                  {retryInstall
                    ? copy(
                        language,
                        "重试将在原节点上安装。",
                        "Retry installs on the original node.",
                      )
                    : candidates.length === 0
                      ? copy(
                          language,
                          "没有可用节点。请先确认节点网络。",
                          "No eligible node. Confirm node networking first.",
                        )
                      : editor?.operation === "install"
                        ? copy(
                            language,
                            "同一应用在同一节点只能安装一次。",
                            "An app can be installed only once on the same node.",
                          )
                        : copy(
                            language,
                            "现有安装会在原节点上更新。",
                            "The existing installation is changed on its current node.",
                          )}
                </FieldDescription>
                {nodeUnavailable ? (
                  <FieldError id="deployment-agent-error" role="alert">
                    {nodeUnavailableMessage}
                  </FieldError>
                ) : null}
              </Field>
              {isThreeXUIInstall && role === "master" ? (
                <Alert>
                  <UsersIcon />
                  <AlertTitle>
                    {copy(
                      language,
                      "将作为全局订阅主机",
                      "This will be the global subscription controller",
                    )}
                  </AlertTitle>
                  <AlertDescription>
                    {copy(
                      language,
                      "这是 Center 中第一台 Vastora Proxy。它提供唯一的过渡管理面板和 Vastora 订阅地址，所有地区后续添加的 Xray 节点都会自动接入。",
                      "This is the first Vastora Proxy in this Center. It provides the transitional admin panel and the single Vastora subscription URL; later Xray nodes connect automatically.",
                    )}
                  </AlertDescription>
                </Alert>
              ) : null}
              {isThreeXUIInstall && role === "worker" ? (
                <Alert>
                  <RadioTowerIcon />
                  <AlertTitle>
                    {copy(
                      language,
                      "将作为 Xray 节点",
                      "This will be an Xray node",
                    )}
                  </AlertTitle>
                  <AlertDescription>
                    {copy(
                      language,
                      `安装后自动接入 ${controllerNode?.name ?? "全局订阅主机"}；只运行 Xray，不创建面板或独立订阅地址。`,
                      `After installation it connects to ${controllerNode?.name ?? "the global subscription controller"}; it runs Xray only, without a panel or separate subscription URL.`,
                    )}
                  </AlertDescription>
                </Alert>
              ) : null}
              {isThreeXUIInstall && !controllerReady ? (
                <FieldError role="alert">
                  {copy(
                    language,
                    "全局订阅主机尚未就绪，请稍后再安装节点。",
                    "The global subscription controller is not ready yet. Install the node after it is running.",
                  )}
                </FieldError>
              ) : null}
              {isPulseHost && editor?.operation === "upgrade" ? (
                <Alert>
                  <KeyRoundIcon />
                  <AlertTitle>
                    {copy(
                      language,
                      "Pulse 设置自动补齐",
                      "Pulse settings are filled automatically",
                    )}
                  </AlertTitle>
                  <AlertDescription>
                    {copy(
                      language,
                      "面板地址取自当前就绪的 HTTPS 入口；旧版本没有初始化令牌时，Center 会安全生成并一次性显示。已有设置保持不变。",
                      "The dashboard address comes from its ready HTTPS access point. If the old version has no setup token, Center generates one and shows it once. Existing settings stay unchanged.",
                    )}
                  </AlertDescription>
                </Alert>
              ) : null}
              {editor?.app.app.config
                .filter(
                  (field) =>
                    !editor.app.managedConfigFields?.includes(field.key) &&
                    !(
                      isPulseHost &&
                      editor.operation !== "configure" &&
                      (field.key === "setup_token" ||
                        (editor.operation === "upgrade" &&
                          field.key === "public_url"))
                    ),
                )
                .map((field) => (
                  <ConfigField
                    config={config}
                    field={field}
                    key={field.key}
                    language={language}
                    operation={editor.operation}
                    setConfig={setConfig}
                  />
                ))}
              {editor && !editor.app.app.hostAccess ? (
                <Field>
                  <FieldLabel htmlFor="deployment-registry">
                    {copy(
                      language,
                      "镜像仓库凭据",
                      "Image Registry credential",
                    )}
                  </FieldLabel>
                  <SelectControl
                    id="deployment-registry"
                    onValueChange={setRegistryCredentialID}
                    options={[
                      ...(editor?.operation === "install"
                        ? [
                            {
                              value: "",
                              label: copy(
                                language,
                                "不使用凭据（公开镜像）",
                                "No credential (public image)",
                              ),
                            },
                          ]
                        : [
                            {
                              value: "__preserve__",
                              label: copy(
                                language,
                                "保持当前凭据",
                                "Keep current credential",
                              ),
                            },
                            {
                              value: "",
                              label: copy(
                                language,
                                "清除凭据，改用公开镜像",
                                "Clear credential and use public image",
                              ),
                            },
                          ]),
                      ...data.registryCredentials.map((credential) => ({
                        value: credential.id,
                        label: `${credential.host} — ${credential.username}`,
                      })),
                    ]}
                    value={registryCredentialID}
                  />
                  <FieldDescription>
                    {copy(
                      language,
                      "令牌不会显示或写入节点 Docker 配置；仅在本次拉取时使用。",
                      "Tokens are never displayed or written to the node Docker config; they are used only for this pull.",
                    )}
                  </FieldDescription>
                </Field>
              ) : null}
              {error ? <FieldError role="alert">{error}</FieldError> : null}
            </FieldGroup>
          </div>
          <SheetFooter>
            <Button onClick={onClose} type="button" variant="outline">
              {copy(language, "取消", "Cancel")}
            </Button>
            <Button
              aria-describedby={
                catalogMessage ? "deployment-catalog-error" : undefined
              }
              disabled={
                !permissionsApproved || adoptionBlocked || Boolean(runtimeBlocker) ||
                busy ||
                catalogBlocked ||
                catalogChanged ||
                !selectedAgent ||
                !controllerReady ||
                (editor?.operation === "configure" &&
                  Object.keys(config).length === 0)
              }
              type="submit"
            >
              {busy ? <Spinner data-icon="inline-start" /> : null}
              {editor?.operation === "install"
                ? copy(language, "开始安装", "Install")
                : editor?.operation === "upgrade"
                  ? copy(language, "开始升级", "Upgrade")
                  : copy(language, "应用修改", "Apply changes")}
            </Button>
          </SheetFooter>
        </form>
      </SheetContent>
    </Sheet>
  );
}

function ConfigField({
  config,
  field,
  language,
  operation,
  setConfig,
}: {
  config: Record<string, string | boolean | number>;
  field: AppView["app"]["config"][number];
  language: Language;
  operation: "install" | "upgrade" | "configure";
  setConfig: React.Dispatch<
    React.SetStateAction<Record<string, string | boolean | number>>
  >;
}) {
  const label = field.label[language] || field.label.en;
  const description = field.description[language] || field.description.en;
  if (field.type === "boolean" && operation !== "install")
    return (
      <Field>
        <FieldLabel htmlFor={`config-${field.key}`}>{label}</FieldLabel>
        <SelectControl
          id={"config-" + field.key}
          onValueChange={(value) =>
            setConfig((current) => {
              const next = { ...current };
              if (!value) delete next[field.key];
              else next[field.key] = value === "true";
              return next;
            })
          }
          options={[
            {
              value: "",
              label: copy(language, "保持当前设置", "Keep current setting"),
            },
            { value: "true", label: copy(language, "开启", "On") },
            { value: "false", label: copy(language, "关闭", "Off") },
          ]}
          value={
            config[field.key] === undefined ? "" : String(config[field.key])
          }
        />
        <FieldDescription>{description}</FieldDescription>
      </Field>
    );
  if (field.type === "boolean")
    return (
      <Field orientation="horizontal">
        <div className="flex flex-1 flex-col gap-1">
          <FieldLabel htmlFor={`config-${field.key}`}>{label}</FieldLabel>
          <FieldDescription>{description}</FieldDescription>
        </div>
        <Switch
          checked={Boolean(config[field.key])}
          id={`config-${field.key}`}
          onCheckedChange={(value) =>
            setConfig((current) => ({ ...current, [field.key]: value }))
          }
        />
      </Field>
    );
  return (
    <Field>
      <FieldLabel htmlFor={`config-${field.key}`}>{label}</FieldLabel>
      <Input
        id={`config-${field.key}`}
        min={field.type === "integer" ? 1 : undefined}
        onChange={(event) =>
          setConfig((current) => {
            const next = { ...current };
            if (event.target.value === "") delete next[field.key];
            else
              next[field.key] =
                field.type === "integer"
                  ? Number(event.target.value)
                  : event.target.value;
            return next;
          })
        }
        placeholder={
          operation !== "install"
            ? copy(
                language,
                "留空以保持原值",
                "Leave blank to keep the current value",
              )
            : undefined
        }
        required={operation === "install" && field.required}
        type={
          field.secret
            ? "password"
            : field.type === "integer"
              ? "number"
              : "text"
        }
        value={config[field.key] === undefined ? "" : String(config[field.key])}
      />
      <FieldDescription>{description}</FieldDescription>
    </Field>
  );
}

export function PublicationSheet({ data, language, onClose, onSubmit, service }: { data: AppData; language: Language; onClose: () => void; onSubmit: (input: CreatePublicationInput) => Promise<void>; service: Service | null }) {
  const [intent, setIntent] = useState<PublicationIntent>("private");
  const [kind, setKind] = useState<PublicationKind>("headscale_gateway");
  const [gatewayID, setGatewayID] = useState("");
  const [hostname, setHostname] = useState("");
  const [hostnameTouched, setHostnameTouched] = useState(false);
  const hostnameInput = useRef<HTMLInputElement>(null);
  const [sniHostname, setSNIHostname] = useState("");
  const [dnsProvider, setDNSProvider] = useState<"manual" | "cloudflare" | "headscale">("manual");
  const [highRisk, setHighRisk] = useState(false);
  const [tlsEnabled, setTLSEnabled] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [cloudflareAccessStatus, setCloudflareAccessStatus] = useState(data.centerRemoteAccess);
  const [cloudflareAccessLoadError, setCloudflareAccessLoadError] = useState(data.centerRemoteAccessError);
  const [cloudflareAccessLoading, setCloudflareAccessLoading] = useState(false);
  const cloudflare = data.integrations.find((value) => value.kind === "cloudflare" && value.status === "configured");
  const cloudflareReady = Boolean(cloudflare);
  const cloudflareZone = normalizeHostname(cloudflare?.endpoint ?? "");
  const headscaleReady = data.integrations.some((value) => value.kind === "headscale" && value.status === "configured" && value.mode === "builtin");
  const options = useMemo(() => publicationOptions(data, service, language), [data, service, language]);
  const intents = useMemo(() => publicationIntentOptions(data, service, language), [data, service, language]);
  const application = service ? data.applications.find((value) => value.id === service.applicationId) : undefined;
  const cpaClientAPI = application?.appKey === "vastora-official/cpa" && service?.name === "client-api";
  const gateways = service ? gatewaysForKind(data, service, kind) : [];
  const availableKinds: PublicationKind[] = cpaClientAPI ? ["cloudflare_tunnel"] : service ? publicationKindsForIntent(service, intent) : [];
  const advancedOptions = options.filter((option) => availableKinds.includes(option.kind));
  const selectedOption = options.find((option) => option.kind === kind);
  const applicationNode = data.agents.find((value) => value.id === application?.nodeId);
  const applicationNodeIngress = Boolean(service && (service.protocol !== "http" && service.protocol !== "https" || kind === "public_shared_443"));
  const ingressOwner = applicationNodeIngress ? "application_node" : kind === "cloudflare_tunnel" ? "tunnel_connector" : "site_gateway";
  const managedRealityOnOwnNode = Boolean(service?.appProtocol === "vless/tcp/reality" && application?.appKey === "vastora-official/3x-ui");
  const defaultDNS = (next: PublicationKind) => {
    if (next === "headscale_gateway" && headscaleReady) return "headscale" as const;
    if (next === "cloudflare_tunnel" || (["public_direct", "public_shared_443"] as PublicationKind[]).includes(next) && cloudflareReady) return "cloudflare" as const;
    return "manual" as const;
  };
  useEffect(() => {
    if (!service) return;
    const preferred = cpaClientAPI ? undefined : publicationIntentOptions(data, service, language).find((option) => option.enabled);
    const nextKind = cpaClientAPI ? "cloudflare_tunnel" : preferred?.kind ?? publicationOptions(data, service, language).find((option) => option.enabled)?.kind ?? "lan_gateway";
    setIntent(cpaClientAPI ? "public_web" : preferred?.intent ?? (service.protocol === "http" || service.protocol === "https" ? "private" : "protocol"));
    setKind(nextKind);
    setGatewayID(gatewaysForKind(data, service, nextKind)[0]?.id ?? "");
    setHostname(defaultPublicationHostname(data, service, nextKind));
    setHostnameTouched(false);
    setSNIHostname("");
    setDNSProvider(defaultDNS(nextKind));
    setTLSEnabled(cloudflareReady && (nextKind === "lan_gateway" || nextKind === "headscale_gateway"));
    setHighRisk(false); setError("");
  }, [service?.id, cpaClientAPI]);
  useEffect(() => {
    setCloudflareAccessStatus(data.centerRemoteAccess);
    setCloudflareAccessLoadError(data.centerRemoteAccessError);
    if (!service || cpaClientAPI || data.centerRemoteAccess || data.centerRemoteAccessError) {
      setCloudflareAccessLoading(false);
      return;
    }
    const controller = new AbortController();
    setCloudflareAccessLoading(true);
    void api.centerRemoteAccess(controller.signal).then((status) => {
      setCloudflareAccessStatus(status);
      setCloudflareAccessLoadError(undefined);
    }).catch((loadError) => {
      if (!controller.signal.aborted) setCloudflareAccessLoadError(loadError instanceof Error ? loadError.message : "Center remote access status request failed");
    }).finally(() => {
      if (!controller.signal.aborted) setCloudflareAccessLoading(false);
    });
    return () => controller.abort();
  }, [service?.id, cpaClientAPI, data.centerRemoteAccess, data.centerRemoteAccessError]);
  const selectKind = (next: PublicationKind) => {
    setKind(next); const nodes = service ? gatewaysForKind(data, service, next) : [];
    setGatewayID(nodes[0]?.id ?? "");
    if (service) setHostname(defaultPublicationHostname(data, service, next));
    setHostnameTouched(false);
    setError("");
    setDNSProvider(defaultDNS(next));
    setTLSEnabled(cloudflareReady && (next === "lan_gateway" || next === "headscale_gateway"));
    setHighRisk(false);
  };
  const selectIntent = (next: PublicationIntent) => {
    setIntent(next);
    if (!service) return;
    const preferred = publicationIntentOptions(data, service, language).find((option) => option.intent === next)?.kind;
    if (preferred) selectKind(preferred);
  };
  const highRiskRequired = Boolean(service?.management && kind === "public_direct");
  const cloudflareAccessRequired = !cpaClientAPI && kind === "cloudflare_tunnel" && (cloudflareAccessLoading || Boolean(cloudflareAccessLoadError) || cloudflareAccessStatus?.status !== "configured" || cloudflareAccessStatus?.protectionMode !== "access");
  const automaticPublicHostname = kind === "public_direct" || kind === "cloudflare_tunnel";
  const normalizedHostname = normalizeHostname(hostname);
  const hostnameZone = dnsProvider === "cloudflare" ? cloudflareZone : "";
  const hostnameExample = `${cpaClientAPI ? "cpa" : "app"}.${hostnameZone || data.sites.find((site) => site.id === service?.siteId)?.domainSuffix || "example.com"}`;
  const hostnameError = !normalizedHostname && automaticPublicHostname ? ""
    : !validHostname(hostname)
      ? copy(language, `请输入完整域名，例如 ${hostnameExample}；不能只填前缀，也不要包含 https://、端口或路径。`, `Enter a complete hostname, such as ${hostnameExample}, not just a prefix. Do not include https://, a port, or a path.`)
      : hostnameZone && normalizedHostname !== hostnameZone && !normalizedHostname.endsWith(`.${hostnameZone}`)
        ? copy(language, `域名必须属于当前 Cloudflare 域名 ${hostnameZone}，例如 ${hostnameExample}。`, `Use the configured Cloudflare zone ${hostnameZone} or one of its subdomains, such as ${hostnameExample}.`)
        : "";
  const hostnameInvalid = hostnameTouched && Boolean(hostnameError);
  const canSubmit = Boolean(selectedOption?.enabled && !hostnameError && (applicationNodeIngress ? applicationNode : gatewayID) && (kind !== "public_shared_443" || sniHostname) && (!tlsEnabled || cloudflareReady) && !cloudflareAccessRequired && (!highRiskRequired || highRisk));
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!service) return;
    setError("");
    setHostnameTouched(true);
    if (hostnameError) { hostnameInput.current?.focus(); return; }
    if (!canSubmit || busy) return;
    setBusy(true);
    try {
      const ingress: PublicationIngressInput = applicationNodeIngress ? { owner: "application_node" } : ingressOwner === "tunnel_connector" ? { owner: "tunnel_connector", entryNodeId: gatewayID } : { owner: "site_gateway", entryNodeId: gatewayID };
      await onSubmit({ serviceId: service.id, kind, ingress, hostname: normalizedHostname || undefined, sniHostname: kind === "public_shared_443" ? sniHostname : undefined, dnsProvider, tlsEnabled: (kind === "lan_gateway" || kind === "headscale_gateway") && tlsEnabled, confirmHighRisk: highRisk });
    } catch (submitError) { setError(userError(language, submitError)); } finally { setBusy(false); }
  };
  return (
    <Sheet onOpenChange={(next) => { if (!next) onClose(); }} open={Boolean(service)}>
      <SheetContent className="sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>{cpaClientAPI ? copy(language, "开启 CPA 公网 API", "Enable CPA public API") : copy(language, `添加 ${service?.name ?? ""} 的访问方式`, `Add access to ${service?.name ?? ""}`)}</SheetTitle>
          <SheetDescription>{cpaClientAPI ? copy(language, "生成供客户端调用的 HTTPS API 地址。", "Create an HTTPS API URL for clients.") : copy(language, "选择谁可以访问，Vastora 会自动使用当前最安全的可用入口。", "Choose who can access it. Vastora automatically uses the safest available method.")}</SheetDescription>
        </SheetHeader>
        <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void submit(event)}>
          <div className="flex-1 overflow-y-auto px-4">
            <FieldGroup>
              {cpaClientAPI ? <Alert><ShieldCheckIcon /><AlertTitle>{copy(language, "仅开放客户端 API", "Client API only")}</AlertTitle><AlertDescription>{copy(language, "此域名只转发 /v1 请求，并要求 CPA 客户端 API 密钥；管理页面仍使用原来的受保护入口。", "This hostname forwards only /v1 requests and requires the CPA client API key. The management page stays on its existing protected access point.")}</AlertDescription></Alert> : <FieldSet>
                <FieldLegend>{copy(language, "谁需要访问？", "Who needs access?")}</FieldLegend>
                {intents.map((option) => (
                  <label className="flex min-h-11 items-start gap-3 rounded-xl border p-3 has-checked:border-primary has-checked:bg-primary/5" data-disabled={!option.enabled} key={option.intent}>
                    <input checked={intent === option.intent} className="mt-1" disabled={!option.enabled} name="publication-intent" onChange={() => selectIntent(option.intent)} type="radio" value={option.intent} />
                    <span className="flex min-w-0 flex-1 flex-col gap-1 text-sm">
                      <span className="font-medium">{option.title}</span>
                      <span className="text-muted-foreground">{option.description}</span>
                    </span>
                  </label>
                ))}
              </FieldSet>}
              <Field data-invalid={hostnameInvalid}>
                <FieldLabel htmlFor="publication-hostname">{cpaClientAPI ? copy(language, "API 域名（可自定义）", "API hostname (customizable)") : copy(language, kind === "public_direct" || kind === "cloudflare_tunnel" ? "公网入口域名（可自定义）" : "访问域名", kind === "public_direct" || kind === "cloudflare_tunnel" ? "Public hostname (customizable)" : "Access hostname")}</FieldLabel>
                <Input aria-describedby={hostnameInvalid ? "publication-hostname-help publication-hostname-error" : "publication-hostname-help"} aria-invalid={hostnameInvalid} autoCapitalize="none" autoCorrect="off" id="publication-hostname" onBlur={(event) => { setHostnameTouched(true); setHostname(normalizeHostname(event.target.value)); }} onChange={(event) => { setHostname(event.target.value.toLowerCase()); setError(""); }} placeholder={automaticPublicHostname ? copy(language, "留空时自动生成", "Generated automatically when empty") : hostnameExample} ref={hostnameInput} required={!automaticPublicHostname} spellCheck={false} value={hostname} />
                <FieldDescription id="publication-hostname-help">{cpaClientAPI ? copy(language, `留空时自动生成随机域名；也可以填写 ${hostnameExample} 这样的完整域名。最终 API 地址以 /v1 结尾。`, `Leave empty for a random hostname, or enter a complete hostname such as ${hostnameExample}. The resulting API URL ends in /v1.`) : automaticPublicHostname ? copy(language, `留空由 Center 生成 128-bit 随机域名，不包含应用或节点信息。自定义请填写完整域名，例如 ${hostnameExample}。`, `Leave empty for a random 128-bit hostname without app or node details. To customize it, enter a complete hostname such as ${hostnameExample}.`) : copy(language, `填写完整域名，例如 ${hostnameExample}，不要只填前缀。`, `Enter a complete hostname such as ${hostnameExample}, not just a prefix.`)}</FieldDescription>
                {hostnameInvalid ? <FieldError id="publication-hostname-error">{hostnameError}</FieldError> : null}
              </Field>
              {kind === "public_shared_443" ? <Field><FieldLabel htmlFor="publication-sni">{copy(language, "协议 SNI", "Protocol SNI")}</FieldLabel><Input autoCapitalize="none" autoCorrect="off" id="publication-sni" onChange={(event) => setSNIHostname(event.target.value.toLowerCase())} placeholder="www.example.com" required spellCheck={false} value={sniHostname} /><FieldDescription>{copy(language, "客户端握手中使用的 SNI；它与上面的连接域名是两个不同地址。", "The SNI sent in the client handshake. It is different from the connection hostname above.")}</FieldDescription></Field> : null}
              {intent === "protocol" ? <Alert><ShieldAlertIcon /><AlertTitle>{copy(language, "这是高级公网入口", "This is an advanced public access method")}</AlertTitle><AlertDescription>{copy(language, "应用负责协议和端口配置；Vastora 只检查公网能力与运行状态。", "The app controls protocol and ports. Vastora only checks public reachability and runtime status.")}</AlertDescription></Alert> : null}
              {!cpaClientAPI && kind === "cloudflare_tunnel" && cloudflareAccessLoading ? <Alert><Spinner /><AlertTitle>{copy(language, "正在读取 Center 远程入口状态", "Loading the Center remote entry status")}</AlertTitle><AlertDescription>{copy(language, "正在确认 Cloudflare Access 配置。", "Checking the Cloudflare Access configuration.")}</AlertDescription></Alert> : null}
              {!cpaClientAPI && kind === "cloudflare_tunnel" && !cloudflareAccessLoading && cloudflareAccessLoadError ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "无法读取 Center 远程入口状态", "Could not load the Center remote entry status")}</AlertTitle><AlertDescription>{copy(language, "当前无法确认 Cloudflare Access 是否已配置，因此暂不允许创建入口。请重试页面同步。", "Cloudflare Access configuration cannot be confirmed, so this entry cannot be created yet. Retry the page sync.")}<code className="mt-2 block break-all text-xs">{cloudflareAccessLoadError}</code></AlertDescription></Alert> : null}
              {!cpaClientAPI && kind === "cloudflare_tunnel" && !cloudflareAccessLoading && !cloudflareAccessLoadError && !cloudflareAccessStatus ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "尚未读取 Center 远程入口状态", "Center remote entry status has not loaded")}</AlertTitle><AlertDescription>{copy(language, "当前无法确认 Cloudflare Access 是否已配置，因此暂不允许创建入口。请重试页面同步。", "Cloudflare Access configuration cannot be confirmed, so this entry cannot be created yet. Retry the page sync.")}</AlertDescription></Alert> : null}
              {!cpaClientAPI && kind === "cloudflare_tunnel" && !cloudflareAccessLoadError && cloudflareAccessStatus?.status === "pending" ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "Center 远程入口正在配置", "The Center remote entry is being configured")}</AlertTitle><AlertDescription>{copy(language, "请等待网络页面完成 Cloudflare Access 配置后再创建应用入口。", "Wait for Cloudflare Access setup to finish on the Network page before creating this application entry.")}</AlertDescription></Alert> : null}
              {!cpaClientAPI && kind === "cloudflare_tunnel" && !cloudflareAccessLoadError && cloudflareAccessStatus?.status === "failed" ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "Center 远程入口配置失败", "Center remote entry setup failed")}</AlertTitle><AlertDescription>{copy(language, "请先在网络页面修复 Cloudflare Access 配置。", "Fix the Cloudflare Access configuration on the Network page first.")}{cloudflareAccessStatus.lastError ? <code className="mt-2 block break-all text-xs">{cloudflareAccessStatus.lastError}</code> : null}</AlertDescription></Alert> : null}
              {!cpaClientAPI && kind === "cloudflare_tunnel" && !cloudflareAccessLoadError && cloudflareAccessStatus?.status === "disabled" ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, cloudflareAccessStatus.available ? "请先启用 Center 远程入口" : "Center 远程入口不可用", cloudflareAccessStatus.available ? "Enable the Center remote entry first" : "The Center remote entry is unavailable")}</AlertTitle><AlertDescription>{copy(language, cloudflareAccessStatus.available ? "Cloudflare 模式会复用相同的 Access 登录限制；请先在网络页面完成配置。" : "当前 Center 无法管理 Cloudflare Access，因此不能创建此入口。", cloudflareAccessStatus.available ? "Cloudflare mode reuses the same Access login restriction. Configure it on the Network page first." : "This Center cannot manage Cloudflare Access, so this entry cannot be created.")}</AlertDescription></Alert> : null}
              {!cpaClientAPI && kind === "cloudflare_tunnel" && !cloudflareAccessLoadError && cloudflareAccessStatus?.status === "configured" && cloudflareAccessStatus.protectionMode !== "access" ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "应用入口需要 Cloudflare Access 模式", "Application entry requires Cloudflare Access mode")}</AlertTitle><AlertDescription>{copy(language, "Center 当前使用 Turnstile 直达登录。请在网络页面把远程备用入口切换为“Cloudflare Access 双层登录”，再创建受 Access 保护的应用入口。", "Center currently uses direct Turnstile sign-in. On the Network page, switch the remote fallback to Cloudflare Access two-layer sign-in before creating an Access-protected application entry.")}</AlertDescription></Alert> : null}
              {cpaClientAPI && !selectedOption?.enabled ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "暂时无法开启公网 API", "Public API is not available yet")}</AlertTitle><AlertDescription>{selectedOption?.reason || copy(language, "请先连接 Cloudflare 并启用 Tunnel 节点。", "Connect Cloudflare and enable a Tunnel node first.")}</AlertDescription></Alert> : null}
              {highRiskRequired ? <Alert variant="destructive"><ShieldAlertIcon /><AlertTitle>{copy(language, "管理页面公网发布风险较高", "Publishing an admin page publicly is high risk")}</AlertTitle><AlertDescription>{copy(language, "请确认应用已设置强密码。Vastora 第一版不会代管额外的访问认证。", "Confirm that the app has a strong password. Vastora v1 does not manage an additional access login.")}<Field className="mt-3" orientation="horizontal"><FieldLabel htmlFor="confirm-high-risk">{copy(language, "我确认继续公网发布", "I understand and want to publish")}</FieldLabel><Switch checked={highRisk} id="confirm-high-risk" onCheckedChange={setHighRisk} /></Field></AlertDescription></Alert> : null}
              {kind === "lan_gateway" || kind === "headscale_gateway" ? <Field className="rounded-xl border p-3" orientation="horizontal"><div className="flex flex-1 flex-col gap-1"><FieldLabel htmlFor="publication-tls">HTTPS</FieldLabel><FieldDescription>{cloudflareReady ? copy(language, "默认开启。使用 Cloudflare DNS 验证申请可信证书，服务仍只在私网开放。", "On by default. Cloudflare DNS validation issues a trusted certificate while the service remains private.") : copy(language, "连接 Cloudflare 后可以开启；当前入口将使用私网 HTTP。", "Connect Cloudflare to enable it. This access point will use private HTTP for now.")}</FieldDescription></div><Switch aria-label={copy(language, "使用 HTTPS", "Use HTTPS")} checked={tlsEnabled} disabled={!cloudflareReady} id="publication-tls" onCheckedChange={setTLSEnabled} /></Field> : null}
              <details className="rounded-xl border p-3">
                <summary className="cursor-pointer text-sm font-medium">{copy(language, "高级设置", "Advanced settings")}</summary>
                <div className="mt-4 flex flex-col gap-4">
                  <Field>
                    <FieldLabel htmlFor="publication-kind">{copy(language, "底层入口方式", "Underlying access method")}</FieldLabel>
                    <SelectControl id="publication-kind" onValueChange={(value) => selectKind(value as PublicationKind)} options={advancedOptions.map((option) => ({ value: option.kind, label: publicationKindLabel(language, option.kind) + " — " + option.reason, disabled: !option.enabled }))} value={kind} />
                    <FieldDescription>{selectedOption?.reason}</FieldDescription>
                  </Field>
                  {applicationNodeIngress ? <Field><FieldLabel>{copy(language, "入口所有者", "Ingress owner")}</FieldLabel><div className="rounded-lg border bg-muted/40 px-3 py-2 text-sm">{copy(language, "应用节点", "Application node")} · {applicationNode?.name ?? copy(language, "节点不可用", "Node unavailable")}</div><FieldDescription>{copy(language, "协议入口固定由运行应用的节点提供，不能选择其他 Site Gateway。", "Protocol ingress is always provided by the node running the application; another Site Gateway cannot be selected.")}</FieldDescription></Field> : <Field><FieldLabel htmlFor="publication-node">{copy(language, ingressOwner === "tunnel_connector" ? "Tunnel Connector" : "Site Gateway", ingressOwner === "tunnel_connector" ? "Tunnel connector" : "Site Gateway")}</FieldLabel><SelectControl id="publication-node" onValueChange={setGatewayID} options={[{ value: "", label: copy(language, "没有可用节点", "No node available"), disabled: true }, ...gateways.map((agent) => ({ value: agent.id, label: agent.name }))]} required value={gatewayID} /><FieldDescription>{copy(language, ingressOwner === "tunnel_connector" ? "选择负责连接 Cloudflare Tunnel 的节点。" : "选择负责站点 Web 反向代理的网关。", ingressOwner === "tunnel_connector" ? "Select the node that connects the Cloudflare Tunnel." : "Select the gateway that provides Site Web reverse proxying.")}</FieldDescription></Field>}
                  <Field>
                    <FieldLabel htmlFor="publication-dns">DNS</FieldLabel>
                    <SelectControl disabled={kind === "cloudflare_tunnel"} id="publication-dns" onValueChange={(value) => setDNSProvider(value as "manual" | "cloudflare" | "headscale")} options={[{ value: "manual", label: copy(language, "手动配置", "Manual") }, ...(kind === "headscale_gateway" && headscaleReady ? [{ value: "headscale", label: "Headscale DNS" }] : []), ...((kind === "public_direct" || kind === "public_shared_443") && cloudflareReady ? [{ value: "cloudflare", label: "Cloudflare DNS-only" }] : []), ...(kind === "cloudflare_tunnel" ? [{ value: "cloudflare", label: "Cloudflare Tunnel" }] : [])]} value={dnsProvider} />
                  </Field>
                  {kind === "public_shared_443" ? <Alert><ShieldAlertIcon /><AlertTitle>{copy(language, "共享公网 443", "Shared public 443")}</AlertTitle><AlertDescription>{managedRealityOnOwnNode ? copy(language, "这是 Vastora 管理的 Xray REALITY：Xray 只监听节点的私有服务地址，宿主机公网 443 由 HAProxy 独占并按精确 SNI 转发。", "This is Vastora-managed Xray REALITY: Xray listens only on the node's private service address, while HAProxy exclusively owns public port 443 and routes by exact SNI.") : copy(language, "连接域名只负责解析到节点，HAProxy 会按协议 SNI 分流。普通应用与入口位于同一节点时，应用监听端口必须避开宿主机 443。", "The connection hostname only resolves to the node, and HAProxy routes by protocol SNI. When a regular app and its entry are on the same node, the app listener must not occupy host port 443.")}</AlertDescription></Alert> : null}
                </div>
              </details>
              {error ? <FieldError role="alert">{error}</FieldError> : null}
            </FieldGroup>
          </div>
          <SheetFooter>
            <Button onClick={onClose} type="button" variant="outline">{copy(language, "取消", "Cancel")}</Button>
            <Button disabled={busy || !canSubmit} type="submit">{busy ? <Spinner data-icon="inline-start" /> : null}{cpaClientAPI ? copy(language, "开启公网 API", "Enable public API") : copy(language, "创建访问方式", "Create access")}</Button>
          </SheetFooter>
        </form>
      </SheetContent>
    </Sheet>
  );
}
