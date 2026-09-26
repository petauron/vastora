#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import process from "node:process";

const root = path.resolve(import.meta.dirname, "..");
const centerDir = path.join(root, "internal", "center");
const serverSource = fs.readFileSync(path.join(centerDir, "server.go"), "utf8");
const centerSource = fs.readdirSync(centerDir)
  .filter((name) => name.endsWith(".go") && !name.endsWith("_test.go"))
  .sort()
  .map((name) => fs.readFileSync(path.join(centerDir, name), "utf8"))
  .join("\n");
const packageSources = new Map();
const internalSource = fs.readdirSync(path.join(root, "internal"), { withFileTypes: true })
  .filter((entry) => entry.isDirectory())
  .flatMap((entry) => {
    const directory = path.join(root, "internal", entry.name);
    const sources = fs.readdirSync(directory)
      .filter((name) => name.endsWith(".go") && !name.endsWith("_test.go"))
      .map((name) => fs.readFileSync(path.join(directory, name), "utf8"));
    packageSources.set(entry.name, sources.join("\n"));
    return sources;
  })
  .join("\n");

const routePattern = /mux\.HandleFunc\("(GET|POST|PUT|PATCH|DELETE) (\/api\/v1\/[^\"]+)",\s*(?:s\.requireAuth\((true|false),\s*)?s\.(handle[A-Za-z0-9]+)\)?\)/g;
const routes = [];
for (const match of serverSource.matchAll(routePattern)) {
  routes.push({ method: match[1].toLowerCase(), path: match[2], mutation: match[3] === "true", admin: match[3] !== undefined, handler: match[4] });
}
if (routes.length === 0) {
  throw new Error("no Center API routes found");
}

function handlerSource(name) {
  const start = centerSource.indexOf(`func (s *Server) ${name}(`);
  if (start < 0) return "";
  const next = centerSource.indexOf("\nfunc ", start + 1);
  return centerSource.slice(start, next < 0 ? undefined : next);
}

function words(value) {
  return value
    .replace(/^handle/, "")
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replace(/Three X U I/g, "3x-ui")
    .replace(/X U I/g, "x-ui");
}

function tagFor(routePath) {
  const segment = routePath.split("/")[3] || "system";
  const tags = {
    "agent-binaries": "Agents",
    "agent-decommission-results": "Agents",
    agents: "Agents",
    "agent-enrollments": "Agents",
    "application-commands": "Applications",
    applications: "Applications",
    auth: "Authentication",
    backups: "System",
    catalog: "Catalog",
    deployments: "Deployments",
    integrations: "Integrations",
    meridian: "Applications",
    network: "Network",
    organizations: "Sites",
    publications: "Publications",
    regions: "Agents",
    "registry-credentials": "Catalog",
    routes: "Publications",
    services: "Applications",
    setup: "Setup",
    sites: "Sites",
    status: "System",
    diagnostics: "System",
    system: "System",
    tasks: "Agents",
    executions: "Agents",
    "three-x-ui-migrations": "Applications",
    "three-x-ui": "Applications",
    actions: "System",
  };
  return tags[segment] || "System";
}

function securityFor(route) {
  if (route.admin) {
    return route.mutation
      ? [{ AdminSession: [], AdminCSRF: [] }]
      : [{ AdminSession: [] }];
  }
  if (route.path === "/api/v1/setup/status") return [{}, { AdminSession: [] }];
  if (route.path === "/api/v1/agent-binaries/{os}/{arch}") return [{ EnrollmentBearer: [] }];
  if (route.path === "/api/v1/agent-decommission-results/{taskID}") return [{ DecommissionCallbackBearer: [] }];
  if (route.path.startsWith("/api/v1/agents/{id}/") && route.path !== "/api/v1/agents/{id}/region-suggestion" && route.path !== "/api/v1/agents/{id}/headscale-join" && route.path !== "/api/v1/agents/{id}/revoke") {
    return [{ AgentBearer: [] }];
  }
  return [];
}

function mediaFor(route, source) {
  if (route.handler === "handleAgentBinary" || route.handler === "handleAgentUpdateBinary" || route.handler === "handleThreeXUIMigrationBackup") return "application/octet-stream";
  if (source.includes("text/event-stream")) return "text/event-stream";
  return "application/json";
}

function successStatus(source) {
  if (source.includes("http.StatusCreated")) return "201";
  if (source.includes("http.StatusAccepted")) return "202";
  if (source.includes("http.StatusNoContent")) return "204";
  return "200";
}

function pathParameters(routePath) {
  return [...routePath.matchAll(/\{([^}]+)\}/g)].map((match) => ({
    name: match[1],
    in: "path",
    required: true,
    schema: match[1] === "revision" ? { type: "integer", format: "int64", minimum: 1 } : { type: "string", minLength: 1 },
  }));
}

function queryParameters(source) {
  const names = new Set([...source.matchAll(/Query\(\)\.Get\("([^"]+)"\)/g)].map((match) => match[1]));
  return [...names].sort().map((name) => ({ name, in: "query", required: false, schema: { type: "string" } }));
}

function schemaForGoType(rawType) {
  let type = rawType.trim();
  let nullable = false;
  if (type.startsWith("*")) {
    nullable = true;
    type = type.slice(1);
  }
  let schema;
  if (type.startsWith("[]")) {
    schema = type === "[]byte" ? { type: "string", contentEncoding: "base64" } : { type: "array", items: schemaForGoType(type.slice(2)) };
  } else if (type.startsWith("map[")) {
    schema = { type: "object", additionalProperties: true };
  } else if (type === "string" || type === "time.Time") {
    schema = type === "time.Time" ? { type: "string", format: "date-time" } : { type: "string" };
  } else if (["int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64"].includes(type)) {
    schema = { type: "integer" };
  } else if (["float32", "float64"].includes(type)) {
    schema = { type: "number" };
  } else if (type === "bool") {
    schema = { type: "boolean" };
  } else if (type === "json.RawMessage" || type === "any" || type === "interface{}") {
    schema = {};
  } else {
    const name = type.split(".").at(-1);
    const source = type.includes(".") ? packageSources.get(type.split(".")[0]) ?? "" : internalSource;
    const match = source.match(new RegExp(`type\\s+${name}\\s+struct\\s*\\{([\\s\\S]*?)\\n\\}`));
    schema = match ? schemaForStructFields(match[1]) : { type: "object", additionalProperties: true };
  }
  return nullable ? { anyOf: [schema, { type: "null" }] } : schema;
}

function schemaForStructFields(fields) {
  const properties = {};
  const pattern = /^\s*[A-Za-z0-9_]+\s+([^\s`]+)\s+`json:"([^",]+)[^"]*"`/gm;
  for (const match of fields.matchAll(pattern)) {
    if (match[2] !== "-") properties[match[2]] = schemaForGoType(match[1]);
  }
  if (Object.keys(properties).length === 0) return { type: "object", additionalProperties: true };
  return { type: "object", additionalProperties: false, properties };
}

function requestSchema(source) {
  const anonymous = source.match(/var\s+input\s+struct\s*\{([\s\S]*?)\n\s*\}/);
  if (anonymous) return schemaForStructFields(anonymous[1]);
  const named = source.match(/var\s+input\s+([A-Za-z0-9_.]+)/);
  return named ? schemaForGoType(named[1]) : { $ref: "#/components/schemas/JsonObject" };
}

const publicationIngressRequestSchema = {
  oneOf: [
    {
      type: "object",
      additionalProperties: false,
      required: ["owner", "entryNodeId"],
      properties: {
        owner: { type: "string", const: "site_gateway" },
        entryNodeId: { type: "string", minLength: 1 },
      },
    },
    {
      type: "object",
      additionalProperties: false,
      required: ["owner"],
      properties: {
        owner: { type: "string", const: "application_node" },
      },
    },
    {
      type: "object",
      additionalProperties: false,
      required: ["owner", "entryNodeId"],
      properties: {
        owner: { type: "string", const: "tunnel_connector" },
        entryNodeId: { type: "string", minLength: 1 },
      },
    },
  ],
};

const publicationIngressResponseSchema = {
  oneOf: ["site_gateway", "application_node", "tunnel_connector"].map((owner) => ({
    type: "object",
    additionalProperties: false,
    required: ["owner", "entryNodeId"],
    properties: {
      owner: { type: "string", const: owner },
      entryNodeId: { type: "string", minLength: 1 },
    },
  })),
};

const realitySecurityCheckItemSchema = {
  type: "object",
  additionalProperties: false,
  required: ["kind", "status", "reason"],
  properties: {
    kind: { type: "string", enum: ["expected_fallback", "openai_sni", "cloudflare_sni", "random_sni", "no_sni"] },
    status: { type: "string", enum: ["passed", "failed", "inconclusive"] },
    reason: { type: "string", enum: ["expected_fallback_verified", "expected_fallback_unavailable", "unauthorized_destination_rejected", "unauthorized_destination_reached", "local_tls_termination", "probe_timeout", "probe_interrupted"] },
  },
};

const realitySecurityCheckResponseSchema = {
  type: "object",
  additionalProperties: false,
  required: ["status", "scope", "checks", "checkedAt"],
  properties: {
    status: { type: "string", enum: ["safe", "affected", "inconclusive"] },
    scope: { type: "string", enum: ["remote", "same_host"] },
    checks: { type: "array", minItems: 5, maxItems: 5, items: realitySecurityCheckItemSchema },
    checkedAt: { type: "string", format: "date-time" },
  },
};

const publicationResponseSchema = schemaForGoType("PublicationView");
publicationResponseSchema.required = ["id", "serviceId", "kind", "ingress", "hostname", "dnsProvider", "tlsEnabled", "desiredRevision", "appliedRevision", "status", "createdAt", "updatedAt"];
publicationResponseSchema.properties.ingress = { $ref: "#/components/schemas/PublicationIngress" };
publicationResponseSchema.properties.securityCheck = { $ref: "#/components/schemas/RealitySecurityCheck" };

const document = {
  openapi: "3.1.0",
  info: {
    title: "Vastora Center API",
    version: "v1",
    summary: "Administrator and Agent control-plane contract",
    description: "The current prerelease /api/v1 contract. Browser administrator reads require the SameSite session cookie; mutations additionally require the X-CSRF-Token header. Agent endpoints use a per-Agent bearer credential, initial binary download uses a one-time enrollment bearer token, and host-removal completion uses a token bound to one decommission task. JSON requests must use application/json, contain one value, be at most 1 MiB, and contain no unknown fields. Error responses use the Error envelope and never include secrets. Binary downloads and event streams declare their actual media types.",
  },
  servers: [{ url: "/", description: "The current Center" }],
  tags: ["Authentication", "Setup", "System", "Sites", "Agents", "Deployments", "Applications", "Publications", "Integrations", "Network", "Catalog"].map((name) => ({ name })),
  paths: {},
  components: {
    securitySchemes: {
      AdminSession: { type: "apiKey", in: "cookie", name: "vastora_session", description: "Administrator session cookie." },
      AdminCSRF: { type: "apiKey", in: "header", name: "X-CSRF-Token", description: "Required together with AdminSession for browser mutations." },
      AgentBearer: { type: "http", scheme: "bearer", bearerFormat: "opaque Agent credential", description: "Credential bound to the Agent id in the path." },
      EnrollmentBearer: { type: "http", scheme: "bearer", bearerFormat: "one-time enrollment token", description: "Short-lived token used only to download the initial Agent binary." },
      DecommissionCallbackBearer: { type: "http", scheme: "bearer", bearerFormat: "task-bound callback token", description: "Single-task token used only to acknowledge completed local Agent removal." },
    },
    schemas: {
      Error: {
        type: "object",
        additionalProperties: false,
        required: ["code", "error"],
        properties: {
          code: { type: "string", example: "invalid_request" },
          error: { type: "string", example: "Check your entries and try again." },
        },
      },
      LoginError: {
        type: "object",
        additionalProperties: false,
        required: ["code", "error", "captchaRequired"],
        properties: {
          code: { type: "string", enum: ["invalid_credentials", "captcha_failed", "login_throttled", "login_protection_unavailable"] },
          error: { type: "string" },
          captchaRequired: { type: "boolean" },
        },
      },
      JsonObject: { type: "object", additionalProperties: true, description: "Endpoint-specific JSON object. Runtime decoding rejects fields not declared by the corresponding Go request type." },
      PublicationIngress: publicationIngressResponseSchema,
      Publication: publicationResponseSchema,
      RealitySecurityCheck: realitySecurityCheckResponseSchema,
      LandingView: {
        type: "object",
        additionalProperties: false,
        required: ["nodeIds", "revision", "servers", "candidates", "proxies", "latencies"],
        properties: {
          ...schemaForGoType("LandingSelection").properties,
          ...schemaForGoType("LandingView").properties,
        },
      },
      ApplicationCredentials: {
        oneOf: [
          {
            type: "object",
            additionalProperties: false,
            required: ["kind", "username", "password"],
            properties: {
              kind: { type: "string", const: "three_x_ui" },
              username: { type: "string" },
              password: { type: "string" },
            },
          },
          {
            type: "object",
            additionalProperties: false,
            required: ["kind", "managementKey", "clientApiKey"],
            properties: {
              kind: { type: "string", const: "cpa" },
              managementKey: { type: "string" },
              clientApiKey: { type: "string" },
            },
          },
        ],
      },
      ApplicationCredentialRotation: {
        type: "object",
        additionalProperties: false,
        required: ["id", "applicationId", "target", "state", "createdAt", "updatedAt"],
        properties: {
          id: { type: "string" },
          applicationId: { type: "string" },
          target: { type: "string", enum: ["management", "client"] },
          state: { type: "string", enum: ["preparing", "pending", "succeeded", "failed", "action_required"] },
          cpaDeploymentId: { type: "string" },
          keeperDeploymentId: { type: "string" },
          lastError: { type: "string" },
          createdAt: { type: "string", format: "date-time" },
          updatedAt: { type: "string", format: "date-time" },
        },
      },
    },
    responses: {
      Error: {
        description: "The request failed.",
        content: { "application/json": { schema: { $ref: "#/components/schemas/Error" } } },
      },
    },
  },
};

for (const route of routes) {
  const source = handlerSource(route.handler);
  const media = mediaFor(route, source);
  const status = successStatus(source);
  const parameters = [...pathParameters(route.path), ...queryParameters(source)];
  const operation = {
    tags: [tagFor(route.path)],
    summary: words(route.handler),
    operationId: `${route.handler.replace(/^handle/, "").replace(/^./, (value) => value.toLowerCase())}_${route.method}`,
    "x-vastora-audience": route.admin ? "browser-admin" : route.path === "/api/v1/setup/status" ? "bootstrap-optional-admin" : securityFor(route).some((entry) => entry.AgentBearer) ? "agent" : securityFor(route).some((entry) => entry.EnrollmentBearer) ? "agent-enrollment" : securityFor(route).some((entry) => entry.DecommissionCallbackBearer) ? "agent-decommission-callback" : "bootstrap-public",
    security: securityFor(route),
    responses: {
      [status]: {
        description: status === "201" ? "Created." : status === "202" ? "Accepted." : status === "204" ? "No content." : "Successful response.",
        ...(status === "204" ? {} : { content: { [media]: { schema: media === "application/json" ? {} : { type: "string", format: "binary" } } } }),
      },
      "400": { $ref: "#/components/responses/Error" },
      "401": { $ref: "#/components/responses/Error" },
      "404": { $ref: "#/components/responses/Error" },
      "409": { $ref: "#/components/responses/Error" },
      "500": { $ref: "#/components/responses/Error" },
    },
  };
  if (parameters.length > 0) operation.parameters = parameters;
  if (/decodeJSON(?:Limit)?\(\s*(?:request|r)\s*,/.test(source)) {
    operation.requestBody = {
      required: true,
      description: "A single strict JSON object. Content-Type must be application/json, the decoded size is limited to 1 MiB, and unknown fields are rejected.",
      content: { "application/json": { schema: requestSchema(source) } },
    };
  } else if (route.handler === "handleStoreThreeXUIBackup") {
    operation.requestBody = {
      required: true,
      description: "Encrypted 3x-ui backup stream. The authenticated Agent and revision identify the restore point.",
      content: { "application/octet-stream": { schema: { type: "string", format: "binary" } } },
    };
  }
  const noStoreHeaders = {
    "Cache-Control": {
      description: "Prevents storage of the security-sensitive response.",
      schema: { type: "string", const: "no-store" },
    },
  };
  if (["handleGetExecutionClaimControl", "handleSetExecutionClaimControl"].includes(route.handler)) {
    operation.responses[status].headers = noStoreHeaders;
    operation.description = "Administrator-only persisted control of new task claims and authorization issuance. Pausing does not cancel previously authorized executions or stop heartbeats, session registration or read-only observation. Resuming does not dispose unresolved executions or replay failed tasks.";
    if (route.handler === "handleGetExecutionClaimControl") {
      operation.responses[status].content["application/json"].schema = schemaForGoType("ExecutionClaimControl");
    } else {
      operation.requestBody.content["application/json"].schema.required = ["paused"];
      operation.requestBody.content["application/json"].schema.properties.paused = {type:"boolean"};
    }
  }
  if (["handleExecutionSession", "handleExecutionTransition", "handleReexecuteExecution", "handleAbandonExecution", "handleConfirmExecution", "handleDisposeHelperExecution", "handleDisposeLegacyReceipt", "handleListExecutions", "handleInspectLegacyReceipt"].includes(route.handler)) {
    operation.responses[status].headers = noStoreHeaders;
    if (route.handler === "handleInspectLegacyReceipt") {
      operation.description = "Administrator-only inspection of one bounded legacy receipt archive. Returns original task identity and evidence availability, never completion contents or credentials. Does not resolve, replay or delete evidence.";
      operation.responses[status].content["application/json"].schema = schemaForGoType("LegacyReceiptView");
      operation.responses["409"] = { $ref: "#/components/responses/Error" };
    } else if (route.handler === "handleListExecutions") {
      operation.description = "Administrator-only execution history. Reports persisted phases, errors and dispositions, never sealed task or result evidence. A successful heartbeat does not imply execution success.";
      operation.parameters ||= [];
      operation.parameters.push({name:"before",in:"query",required:false,schema:{type:"integer",minimum:1},description:"Exclusive insertion cursor from nextCursor. Omit for the newest page; each page contains at most 100 records."});
      operation.responses[status].content["application/json"].schema = schemaForGoType("ExecutionPage");
    } else {
      const schema = operation.requestBody.content["application/json"].schema;
      if (route.handler === "handleExecutionSession") {
        schema.required = ["sessionId", "protocol"];
        schema.properties.protocol.const = 2;
        operation.responses["403"] = { $ref: "#/components/responses/Error" };
        operation.description = "Register a process-local session for execution protocol 2. Unsupported protocols and retired sessions are rejected; replacing a session leaves unfinished Agent executions unknown. This request does not authorize a command.";
      } else if (route.handler === "handleExecutionTransition") {
        schema.required = ["sessionId", "action"];
        schema.properties.action.enum = ["start", "step", "renew", "stop", "helper-step", "helper-observe"];
        schema.oneOf = [
          { properties: { action: { const: "start" } }, required: ["digest"] },
          { properties: { action: { enum: ["step", "helper-step"] } }, required: ["phase"] },
          { properties: { action: { enum: ["renew", "stop", "helper-observe"] } } },
        ];
        operation.description = "Consume or inspect an execution bound to the authenticated Agent and session. Lost replies are unknown, not permission to repeat mutations. helper-observe is read-only and returns ready; other transitions return recorded. Expired, stopped, disposed or mismatched executions fail closed.";
        operation.responses[status].content["application/json"].schema = { oneOf: ["ready", "recorded"].map((key) => ({ type: "object", additionalProperties: false, required: [key], properties: { [key]: { type: "boolean" } } })) };
      } else {
        schema.required = ["action", "executionStopped", "note"];
        schema.properties.action.enum = route.handler === "handleReexecuteExecution" ? ["reexecute"] : ["handleAbandonExecution", "handleDisposeLegacyReceipt"].includes(route.handler) ? ["abandon"] : route.handler === "handleConfirmExecution" ? ["confirm-completed"] : ["confirm-completed", "abandon"];
        schema.properties.executionStopped.const = true;
        schema.properties.note.minLength = 1;
        schema.properties.note.maxLength = 1024;
        operation.description = route.handler === "handleReexecuteExecution"
          ? "Explicit administrator decision for a failed or unknown execution after verifying it stopped. Records the audit disposition and queues the exact task revision; a later claim creates a new attempt and authorization. Does not replay the old execution."
          : route.handler === "handleAbandonExecution"
          ? "Explicitly abandon a stopped application or runtime execution. Atomically terminates its exact business attempt and records the operator decision while retaining evidence and existing effects. Does not queue, undo or replay commands. Helpers and imported legacy receipts require their dedicated resolution workflow."
          : route.handler === "handleDisposeLegacyReceipt"
          ? "Explicitly abandon an unresolved imported application or runtime receipt after verifying its executor stopped. Terminates only its matching business attempt and records the administrator disposition atomically. Preserves the complete encrypted archive, including generated credentials, and continues acknowledging identical migration uploads. No replay or remote cleanup. Rejects superseded attempts/revisions and unsupported legacy helpers. Confirmation and reexecution of legacy receipts are not yet supported."
          : route.handler === "handleConfirmExecution"
          ? "Confirm an uncertain application deployment/command, landing server/proxy, gateway routes/component, node listener or tunnel using retained successful result evidence. Application deployment also requires original runtime generation evidence. Applies the normal result validation and business projection atomically with the administrator disposition, retaining historical state and evidence. Rejects missing, invalid or unknown result evidence and superseded attempts or revisions. Does not invoke the Agent executor. Imported receipts are not yet supported; helpers use their dedicated resolution endpoint."
          : "Resolve an Agent update or decommission helper without issuing a command. Update confirmation requires a fresh target-version heartbeat. Decommission confirmation is an explicit administrator attestation of actual cleanup, never inferred from offline status. Records the stopped-execution confirmation, administrator and verification note; old helper permissions and callbacks remain rejected.";
      }
    }
  }
  if (route.handler === "handleClaimTask") {
    operation.parameters ||= [];
    operation.parameters.push({ name: "X-Vastora-Execution-Session", in: "header", required: true, schema: { type: "string", minLength: 1 } });
    operation.description = "Claim only after registering the current execution session. Center persists the content digest and single-use authorization before returning an encrypted task. Only one wait query parameter is accepted; recovery and historical task selectors are rejected. Unresolved executions fence further claims.";
  }
  if (route.handler === "handleImportLegacyReceipt") {
    operation.description = "One-time legacy evidence transfer, not task execution or result replay. Authenticated Agent and current process session are required. Identical evidence is acknowledged only after encrypted archival; conflicts fail closed. Unacknowledged legacy outcomes fence new work until operator disposition. Original acknowledged receipts retain their historical status without applying business effects.";
    operation.requestBody.description = "One strict JSON object, bounded to 3 MiB with a completion bounded to 2 MiB. May contain generated credentials; never log or expose the body.";
    operation.requestBody.content["application/json"].schema.required = ["sessionId", "digest", "receipt"];
    operation.responses["200"].headers = noStoreHeaders;
  }
  if (route.handler === "handleListSources" || route.handler === "handleListApps") {
    const sources = route.handler === "handleListSources";
    const field = sources ? "sources" : "apps";
    operation.description = sources
      ? "Catalog source status. The reserved vastora-official identity is anchored to independently provisioned TUF trust, not the editable source name or URL. Catalog revision and expiry describe the last verified cache; a failed refresh does not replace it."
      : "Available catalog entries. installBlocked means the cached entry may be displayed but cannot authorize a new install or upgrade. Existing application recovery, configuration and uninstall use their recorded manifests.";
    operation.responses["200"].content["application/json"].schema = {
      type: "object", required: [field], additionalProperties: false,
      properties: { [field]: { type: "array", items: schemaForGoType(sources ? "CatalogSource" : "AppView") } },
    };
  } else if (route.handler === "handleOfficialCatalog") {
    operation.description = "Returns the last accepted official target, including signed identity, channel, revision and lifetime. This endpoint alone is not a signature proof: independent clients must verify the upstream TUF repository with their own trusted root. Expired cache may be returned for display only. Missing or damaged cache returns 404.";
    operation.responses["200"].headers = noStoreHeaders;
    operation.responses["200"].content["application/json"].schema = schemaForGoType("catalog.OfficialTarget");
  } else if (["handleLanding", "handleSelectLanding", "handleConfigureLandingProxy"].includes(route.handler)) {
    operation.description = "Administrator-only landing configuration. Mutations use the last observed revision and return the complete overview. Configuration readiness alone does not establish connection health; connection health requires fresh Agent observations.";
    operation.responses["200"].content["application/json"].schema = { $ref: "#/components/schemas/LandingView" };
  } else if (route.handler === "handleLandingLatencyEvents") {
    operation.description = "Administrator-only live latency stream. The first event and each reconnect send a reset snapshot; subsequent events contain only changed or expired source/landing pairs. Each sample includes its measurement time. The stream renews periodically to revalidate the session.";
  } else if (route.handler === "handleAgentLandingLatency") {
    operation.description = "Immediately report one completed latency observation. Agent bearer authentication owns the source node; Center revalidates the configured target identity, revision and freshness. Invalid or replayed observations return 409. This endpoint cannot change routes or authorize business traffic.";
  } else if (route.handler === "handleCreateRealityCommand") {
    operation.requestBody.content["application/json"].schema.required = [
      "applicationId", "regionCode", "name",
      "dnsProvider", "targetHost", "serverName", "verificationId", "targetIp",
    ];
  } else if (route.handler === "handleCreateMeridianEndpoint") {
    operation.requestBody.content["application/json"].schema.required = [
      "applicationId", "verificationId", "targetIp", "targetHost", "serverName", "regionCode", "name",
    ];
  } else if (route.handler === "handleRemoveRealityCommand") {
    operation.description = "Remove only the global subscription controller's local managed VLESS inbound. Retains the controller, global clients, subscription URL and remote nodes. Restores any landing route before deletion. Repeated requests resume an active operation for the same service.";
    operation.requestBody.content["application/json"].schema.required = ["serviceId"];
    operation.responses["202"].content["application/json"].schema = schemaForGoType("ApplicationCommandView");
  } else if (route.handler === "handleRecoveryReadiness") {
    operation.description = "Reads the cluster recovery inventory without probing nodes or opening caller-supplied files. Center-only backup never establishes cluster readiness. External application restore evidence is operator-attested; readiness is not proof of an actual cluster recovery drill or off-host storage.";
    operation.responses["200"].headers = noStoreHeaders;
    operation.responses["200"].content["application/json"].schema = schemaForGoType("RecoveryReadiness");
  } else if (route.handler === "handleLogin") {
    operation.description = "Authenticates the administrator with server-enforced abuse protection. Public responses do not expose failure thresholds, lockout durations or retry countdowns. Complete the security check when captchaRequired is true.";
    operation.requestBody.content["application/json"].schema.required = ["username", "password"];
    operation.responses["403"] = { description: "The required login security check failed or is unavailable.", content: { "application/json": { schema: { $ref: "#/components/schemas/LoginError" } } } };
    operation.responses["429"] = { description: "Sign-in is temporarily unavailable. Try again later.", content: { "application/json": { schema: { $ref: "#/components/schemas/LoginError" } } } };
    operation.responses["401"] = { description: "The credentials were rejected.", content: { "application/json": { schema: { $ref: "#/components/schemas/LoginError" } } } };
  } else if (route.handler === "handleCreatePublication") {
    const schema = operation.requestBody.content["application/json"].schema;
    schema.required = ["serviceId", "kind", "ingress", "dnsProvider"];
    schema.properties.ingress = publicationIngressRequestSchema;
  } else if (route.handler === "handleVerifyRealityTarget") {
    operation.requestBody.content["application/json"].schema.required = [];
    operation.requestBody.content["application/json"].schema.oneOf = [
      { required: ["recommend"], properties: { recommend: { const: true } } },
      { required: ["targetHost", "serverName"], properties: { recommend: { const: false } } },
    ];
  } else if (route.handler === "handleRealitySecurityCheck") {
    operation.summary = "Check Managed REALITY Publication Security";
    operation.description = "Runs five bounded TLS handshakes from Center to the managed node's authoritative public IPv4 address on port 443. The caller cannot supply an address, port, or SNI. Only finite results are retained, and same-host checks are explicitly marked as non-external.";
    operation.responses["200"].description = "The latest revision-fenced security result.";
    operation.responses["200"].headers = noStoreHeaders;
    operation.responses["200"].content["application/json"].schema = { $ref: "#/components/schemas/RealitySecurityCheck" };
  } else if (route.handler === "handleRevealApplicationCredentials") {
    operation.summary = "Reveal Protected Application Credentials";
    operation.description = "Reauthenticates the current administrator, records a security audit event, and returns only the current credentials for the selected managed 3x-ui controller or CPA application. The response is never cacheable.";
    operation.requestBody.content["application/json"].schema.required = ["currentPassword"];
    operation.responses["200"].headers = noStoreHeaders;
    operation.responses["200"].content["application/json"].schema = { $ref: "#/components/schemas/ApplicationCredentials" };
  } else if (route.handler === "handleRotateApplicationCredentials") {
    operation.summary = "Rotate One CPA Credential";
    operation.description = "Reauthenticates the current administrator and rotates either the CPA management key or client API key. The same Idempotency-Key always resumes the same generated value. Management-key rotation updates CPA first and then Keeper; partial completion remains failed or action_required until retried.";
    operation.parameters ||= [];
    operation.parameters.push({
      name: "Idempotency-Key",
      in: "header",
      required: true,
      schema: { type: "string", minLength: 16, maxLength: 128, pattern: "^[A-Za-z0-9._-]+$" },
    });
    const schema = operation.requestBody.content["application/json"].schema;
    schema.required = ["currentPassword", "target", "confirm"];
    schema.properties.target.enum = ["management", "client"];
    schema.properties.confirm.const = true;
    operation.responses["202"].description = "The durable rotation was created, resumed, or completed.";
    operation.responses["202"].headers = noStoreHeaders;
    operation.responses["202"].content["application/json"].schema = { $ref: "#/components/schemas/ApplicationCredentialRotation" };
  } else if (route.handler === "handleApplicationCredentialRotation") {
    operation.summary = "Read CPA Credential Rotation Status";
    operation.description = "Returns the non-secret durable status of one credential rotation so the administrator can see completion, failure, or action_required without retaining a password in the browser. The response is never cacheable.";
    operation.responses["200"].description = "Current durable rotation state.";
    operation.responses["200"].headers = noStoreHeaders;
    operation.responses["200"].content["application/json"].schema = { $ref: "#/components/schemas/ApplicationCredentialRotation" };
  }
  if (route.handler === "handleListIPQuality") {
    operation.summary = "List IPQuality by exact egress address";
    operation.description = "Returns eligible targets and saved checks with agentId, canonical public address, family and selected exit metadata. Reports are independent for each node and IP.";
    const address = operation.parameters.find((parameter) => parameter.name === "compareAddress");
    address.description = "Exact public IPv4 or IPv6 address on compareNodeId. Omit to use that node's native public egress. Landing candidates use their configured exit.";
  } else if (route.handler === "handleStartIPQuality") {
    operation.summary = "Start IPQuality for an eligible egress address";
    const schema = operation.requestBody.content["application/json"].schema;
    schema.required = ["address"];
    schema.properties.address.description = "An exact public IPv4 or IPv6 address from this node's eligible targets. Center resolves the trusted local bind address.";
  }
  if (route.handler === "handleListPublications") {
    operation.responses["200"].content["application/json"].schema = {
      type: "object",
      additionalProperties: false,
      required: ["publications"],
      properties: { publications: { type: "array", items: { $ref: "#/components/schemas/Publication" } } },
    };
  } else if (["handleCreatePublication", "handleUpdatePublicationTLS", "handleVerifyPublication"].includes(route.handler)) {
    operation.responses[status].content["application/json"].schema = { $ref: "#/components/schemas/Publication" };
  }
  document.paths[route.path] ||= {};
  document.paths[route.path][route.method] = operation;
}

const output = `${JSON.stringify(document, null, 2)}\n`;
const outputPath = path.join(root, "docs", "openapi.json");
if (process.argv.includes("--check")) {
  const existing = fs.existsSync(outputPath) ? fs.readFileSync(outputPath, "utf8") : "";
  if (existing !== output) {
    console.error("docs/openapi.json is stale; run node scripts/generate-openapi.mjs");
    process.exit(1);
  }
} else {
  fs.writeFileSync(outputPath, output);
}
