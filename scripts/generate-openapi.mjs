#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { execFileSync } from "node:child_process";

const root = path.resolve(import.meta.dirname, "..");
const centerDir = path.join(root, "internal", "center");
const serverSource = fs.readFileSync(path.join(centerDir, "server.go"), "utf8");
const centerSource = fs.readdirSync(centerDir)
  .filter((name) => name.endsWith(".go") && !name.endsWith("_test.go"))
  .sort()
  .map((name) => fs.readFileSync(path.join(centerDir, name), "utf8"))
  .join("\n");
const packageSources = new Map();
// Catalog is a pinned external protocol, not a second local implementation.
// Resolve the same Go module used by Center, including an explicit development
// workspace when present. Generation must fail if that dependency is missing.
let catalogModuleDir = execFileSync("go", ["list", "-m", "-f", "{{.Dir}}", "github.com/petauron/catalog"], { cwd: root, encoding: "utf8" }).trim();
// Fresh CI checkouts may have the version in go.mod without its source cache.
// Download only that pinned dependency; never resolve an unversioned latest.
if (!catalogModuleDir) {
  const downloaded = JSON.parse(execFileSync("go", ["mod", "download", "-json", "github.com/petauron/catalog"], { cwd: root, encoding: "utf8" }));
  if (downloaded.Error) throw new Error("unable to download the pinned catalog module");
  catalogModuleDir = downloaded.Dir;
}
if (!catalogModuleDir) throw new Error("the pinned catalog module is unavailable");
const catalogSourceDir = path.join(catalogModuleDir, "catalog");
const externalCatalogSource = fs.readdirSync(catalogSourceDir).filter(name => name.endsWith(".go") && !name.endsWith("_test.go")).sort().map(name => fs.readFileSync(path.join(catalogSourceDir, name), "utf8")).join("\n");
const externalSchemas = {};
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

packageSources.set("catalog", externalCatalogSource);

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
    nodes: "Agents",
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

function schemaForGoType(rawType, scope) {
  let type = rawType.trim();
  let nullable = false;
  if (type.startsWith("*")) {
    nullable = true;
    type = type.slice(1);
  }
  let schema;
  if (type.startsWith("[]")) {
    schema = type === "[]byte" ? { type: "string", contentEncoding: "base64" } : { type: "array", items: schemaForGoType(type.slice(2), scope) };
  } else if (type.startsWith("map[")) {
    const valueType = /^map\[string\](.+)$/.exec(type)?.[1];
    schema = { type: "object", additionalProperties: valueType ? schemaForGoType(valueType, scope) : true };
  } else if (type === "meridian.EgressPolicy") {
    schema = { type: "string", enum: ["auto", "ipv4_only", "ipv6_only"] };
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
    const sourceScope = type.includes(".") ? type.split(".")[0] : scope;
    const source = sourceScope ? packageSources.get(sourceScope) ?? "" : internalSource;
    const match = source.match(new RegExp(`type\\s+${name}\\s+struct\\s*\\{([\\s\\S]*?)\\n\\}`));
    if (sourceScope === "catalog" && match) {
      const key = `Catalog${name}`;
      if (!externalSchemas[key]) {
        externalSchemas[key] = {}; // Break recursive Value.array/object cycles.
        externalSchemas[key] = schemaForStructFields(match[1], "catalog");
      }
      schema = { $ref: `#/components/schemas/${key}` };
    } else schema = match ? schemaForStructFields(match[1]) : { type: "object", additionalProperties: true };
  }
  return nullable ? { anyOf: [schema, { type: "null" }] } : schema;
}

function schemaForStructFields(fields, scope) {
  const properties = {};
  const pattern = /^\s*[A-Za-z0-9_]+\s+([^\s`]+)\s+`json:"([^",]+)[^"]*"`/gm;
  for (const match of fields.matchAll(pattern)) {
    if (match[2] !== "-") properties[match[2]] = schemaForGoType(match[1], scope);
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
  if (route.handler === "handleOfficialUIBundle") {
    const assetSchema = { type: "string", format: "binary" };
    operation.description = "Returns the immutable signed official application UI asset. bundle.js uses text/javascript and bundle.css uses text/css. The response is authenticated and never cached.";
    operation.responses[status].content = {
      "text/javascript": { schema: assetSchema },
      "text/css": { schema: assetSchema },
    };
  }
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
  if (route.handler === "handleNodeEgress") {
    if (route.method === "get") delete operation.requestBody;
    operation.responses[status].content["application/json"].schema = schemaForGoType("NodeEgressView");
  }
  const noStoreHeaders = {
    "Cache-Control": {
      description: "Prevents storage of the security-sensitive response.",
      schema: { type: "string", const: "no-store" },
    },
  };
  if (route.handler === "handleCreateAgentReconnectEnrollment") {
    operation.responses[status].headers = noStoreHeaders;
    operation.requestBody.content["application/json"].schema = schemaForGoType("AgentReinstallInput");
    operation.description = "Administrator confirmation bound to a reviewed recovery revision and an operation ID. Retires the previous execution identity and persistently pauses task claims. Withdraws and verifies the authenticated old private identity before preparing a one-time command. Retrying the same operation returns the same valid command; interrupted external preparation is never automatically replayed. Enrollment is not business recovery.";
  }
  if (route.handler === "handleContinueAgentReinstallIsolation") {
    operation.responses[status].headers = noStoreHeaders;
    operation.requestBody.content["application/json"].schema = schemaForGoType("AgentReinstallContinueInput");
    operation.description = "Explicit continuation by the authorizing administrator, bound to the inspected recovery attempt. Rechecks the exact saved private identity and controller; never rediscovers another owner by address. Stale or duplicate continuations cannot repeat an attempt. This action cannot repeat bootstrap preparation with an uncertain result.";
  }
  if (route.handler === "handleApproveAgentReinstallNetwork") {
    operation.responses[status].headers = noStoreHeaders;
    operation.requestBody.content["application/json"].schema = schemaForGoType("AgentReinstallNetworkInput");
    operation.responses[status].content["application/json"].schema = schemaForGoType("AgentReinstallNetworkApproval");
    operation.description = "Explicit address migration approval bound to the replacement identity and reviewed network evidence. Verifies managed private identity through the controller and retains previous/new profiles plus administrator audit. Saves restoration intent only; does not activate addresses, change publications or release task execution.";
  }
  if (route.handler === "handleAgentReinstallPlan") {
    operation.responses[status].headers = noStoreHeaders;
    operation.description = "Administrator-only read of one node's saved recovery requirements from a consistent database snapshot. Reports saved versions, retained network dependencies, unclaimed work and unresolved execution metadata. It never returns credentials, creates enrollment grants, claims work or establishes recovery success. It is not executable authorization.";
    operation.responses[status].content["application/json"].schema = schemaForGoType("AgentReinstallPlan");
  }
  if (route.handler === "handleAgentReinstallLandingSource") {
    operation.operationId = route.path.endsWith("/authorize-landing") ? "agentReinstallLandingAuthorize_post" : "agentReinstallLandingWithdraw_post";
    operation.responses[status].headers = noStoreHeaders;
    operation.requestBody.content["application/json"].schema = schemaForGoType("AgentReinstallApplicationInput");
    operation.responses[status].content["application/json"].schema = schemaForGoType("AgentReinstallLandingSource");
    operation.description = "Explicitly withdraw saved landing source permissions, then replace the source identity only after applied withdrawal receipts. Reuses existing landing tasks, preserves independent sources and never establishes runtime or client health. Failed or changed evidence requires inspection; repeated requests do not queue work again.";
  }
  if (route.handler === "handleAgentReinstallDNS") {
    operation.operationId = route.path.endsWith("/inspect-dns") ? "agentReinstallDNSInspect_post" : "agentReinstallDNSMigrate_post";
    operation.responses[status].headers = noStoreHeaders;
    operation.description = "Explicitly migrate owned DNS records, or inspect a saved uncertain attempt without writing external DNS. Bound to the reviewed recovery; never releases its execution fence.";
    operation.requestBody.content["application/json"].schema = schemaForGoType("AgentReinstallDNSInput");
    operation.responses["200"].content["application/json"].schema = schemaForGoType("AgentReinstallDNS");
  }
  if (route.handler === "handleAgentReinstallAccess") {
    operation.responses[status].headers = noStoreHeaders;
    operation.requestBody.content["application/json"].schema = schemaForGoType("AgentReinstallApplicationInput");
    operation.responses[status].content["application/json"].schema = schemaForGoType("AgentReinstallAccess");
    operation.description = "Activate the reviewed replacement network profile and one restored Meridian service binding after exact runtime and applicable shared-listener receipts. Atomically retain original address approval and activation evidence. Repeated requests return the same receipt; changed bindings require inspection. Does not alter DNS, landing identities, account usage or business readiness, and never releases the recovery fence.";
  }
  if (route.handler === "handleAgentReinstallEntryCheck") {
    operation.responses[status].headers = noStoreHeaders;
    operation.requestBody.content["application/json"].schema = schemaForGoType("AgentReinstallApplicationInput");
    operation.responses[status].content["application/json"].schema = schemaForGoType("AgentReinstallEntryCheck");
    operation.description = "One explicit read-only DNS and exact-SNI TLS check on the approved replacement public address after native runtime and listener receipts. Revalidates recovery authority before storing evidence, preserves prior checks and never activates profiles or completes authenticated client recovery. A changed review or evidence older than 30 minutes makes the result historical.";
  }
  if (route.handler === "handleAgentReinstallListener") {
    operation.responses[status].headers = noStoreHeaders;
    operation.requestBody.content["application/json"].schema = schemaForGoType("AgentReinstallApplicationInput");
    operation.responses[status].content["application/json"].schema = schemaForGoType("AgentReinstallListener");
    operation.description = "Restore the reviewed saved shared listener after native Meridian runtime restoration. Uses only the approved backend and public bind address; other shared applications or a gateway require restoration first. A listener receipt does not verify public reachability, publish subscriptions or release the recovery fence.";
  }
  if (route.handler === "handleAgentReinstallRuntime") {
    operation.responses[status].headers = noStoreHeaders;
    operation.requestBody.content["application/json"].schema = schemaForGoType("AgentReinstallApplicationInput");
    operation.responses[status].content["application/json"].schema = schemaForGoType("AgentReinstallRuntime");
    operation.description = "Restore one reviewed native Meridian runtime using the saved package and credentials on the approved replacement address. Fixed egress is restored only after reviewed source replacement and applied landing authorization; otherwise it remains disabled. The task digest is bound at approval, selection, sealing and projection. A runtime receipt does not activate network profiles, publish services or complete business recovery.";
  }
  if (route.handler === "handleAgentReinstallPreparation") {
    operation.responses[status].headers = noStoreHeaders;
    operation.requestBody.content["application/json"].schema = schemaForGoType("AgentReinstallApplicationInput");
    operation.responses[status].content["application/json"].schema = schemaForGoType("AgentReinstallPreparation");
    operation.description = "Prepare the reviewed saved Meridian package on its authorized replacement machine. Creates one durable application deployment bound to original intent and approved network, without activating services or releasing the recovery fence. Source changes and uncertain outcomes require review.";
  }
  if (route.handler === "handleAgentReinstallMonitorReporting") {
    operation.responses[status].headers = noStoreHeaders;
    operation.requestBody.content["application/json"].schema = schemaForGoType("AgentReinstallMonitorInput");
    operation.responses[status].content["application/json"].schema = schemaForGoType("AgentReinstallMonitorReporting");
    operation.description = "Restore the reviewed saved Pulse collector package and configuration on the replacement host. Imports the original node's credential from authenticated encrypted rotation evidence using Pulse's fixed stdin CLI. Exact task digest binds approval, claim, sealing and result projection. Does not create another monitoring node, verify metric reporting or release the recovery fence.";
  }
  if (route.handler === "handleAgentReinstallMonitorRestore") {
    operation.responses[status].headers = noStoreHeaders;
    operation.requestBody.content["application/json"].schema = schemaForGoType("AgentReinstallMonitorInput");
    operation.responses[status].content["application/json"].schema = schemaForGoType("AgentReinstallMonitorRestore");
    operation.description = "Restore the reviewed saved Pulse collector package and configuration on the replacement host. Imports the original node's credential from authenticated encrypted rotation evidence using Pulse's fixed stdin CLI. Exact task digest binds approval, claim, sealing and result projection. Does not create another monitoring node, verify metric reporting or release the recovery fence.";
  }
  if (route.handler === "handleAgentReinstallMonitorRotation") {
    operation.responses[status].headers = noStoreHeaders;
    operation.requestBody.content["application/json"].schema = schemaForGoType("AgentReinstallMonitorInput");
    operation.responses[status].content["application/json"].schema = schemaForGoType("AgentReinstallMonitorRotation");
    operation.description = "Explicit single rotation of a recently verified original Pulse node credential. Bound to the administrator, replacement identity, original registration evidence and exact managed service deployment at selection, sealing and projection. Retains the original node and history; stores the new credential only in encrypted execution evidence. Repeats return the saved operation, including unknown outcomes, without issuing another rotation. Collector import, reporting verification and recovery completion remain separate.";
  }
  if (route.handler === "handleAgentReinstallMonitorInspection") {
    operation.responses[status].headers = noStoreHeaders;
    operation.requestBody.content["application/json"].schema = schemaForGoType("AgentReinstallMonitorInput");
    operation.responses[status].content["application/json"].schema = schemaForGoType("AgentReinstallMonitorInspection");
    operation.description = "Explicit read-only inspection of reviewed original Pulse enrollment records on their exact managed service deployment. Bound to the administrator, recovery operation, replacement identity and retained source evidence; validated at selection, authorization and projection. Returns a durable receipt without rotating credentials or completing recovery. Requires an inspection-capable service Agent and Pulse CLI.";
  }
  if (route.handler === "handleSettleAgentReinstallLocalWork") {
    operation.responses[status].headers = noStoreHeaders;
    operation.requestBody.content["application/json"].schema = schemaForGoType("AgentReinstallLocalWorkInput");
    operation.responses[status].content["application/json"].schema = schemaForGoType("AgentReinstallLocalDisposition");
    operation.description = "Explicit administrator settlement of reviewed, retired host-local executions after old identity isolation and replacement enrollment. Preserves historical outcomes and encrypted evidence, atomically abandons exact attempts, cancels reviewed pending attempt-zero local tasks with no historical authorization, and records a durable receipt. Saved application intent and credentials are retained. Remote or unclassified effects require separate review. Does not dispatch restoration or release the recovery fence.";
  }
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
      operation.parameters.push({name:"filter",in:"query",required:false,schema:{type:"string",enum:["all","attention","running"],default:"all"},description:"Filter before pagination. attention includes unresolved failed or unknown executions; running includes undisposed offered, running and helper_running executions."});
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
  if (route.handler === "handleCreateDeployment") {
    const schema = operation.requestBody.content["application/json"].schema;
    schema.required = ["agentId", "appKey", "config"];
    schema.properties.operation = { type: "string", enum: ["install", "upgrade", "configure", "uninstall"], default: "install" };
    schema.properties.config = { type: "object", additionalProperties: { type: ["string", "boolean", "number"] }, description: "Administrator-provided fields only; product-managed fields are supplied by Center and must not be submitted." };
    schema.properties.packageRevision = { type: "integer", minimum: 1, maximum: 9007199254740991 };
    schema.properties.manifestSha256 = { type: "string", pattern: "^[a-f0-9]{64}$" };
    schema.properties.authorizedCapabilities = { type: "array", uniqueItems: true, items: { type: "string", minLength: 1 }, description: "Explicit administrator grants. Omission may retain existing grants, never authorize an expansion. Unknown or undeclared grants fail closed." };
    schema.oneOf = [
      { properties: { operation: { enum: ["install", "upgrade"] } }, required: ["packageRevision", "manifestSha256"] },
      { properties: { operation: { enum: ["configure", "uninstall"] } }, required: ["operation"] },
    ];
    operation.parameters ||= [];
    operation.parameters.push({ name: "Idempotency-Key", in: "header", required: false, schema: { type: "string" }, description: "Stable operation key for product-generated one-time credential operations; retain it for an exact retry." });
    operation.description = "Install/upgrade requires the exact reviewed catalog package revision and canonical manifest SHA256. Stale identity, unsupported node runtime, missing grants, expired trust or pending/blocked adoption rejects admission. Configuration/uninstall use recorded resources. Uninstall retains data unless deleteData is explicitly true. Catalog refresh never creates this task.";
    operation.responses["201"].content["application/json"].schema = schemaForGoType("DeploymentView");
  } else if (["handleListDeployments", "handleListApplications", "handleListAgents"].includes(route.handler)) {
    const [field, type] = { handleListDeployments: ["deployments", "DeploymentView"], handleListApplications: ["applications", "ApplicationView"], handleListAgents: ["agents", "AgentView"] }[route.handler];
    const item = schemaForGoType(type);
    if (type === "ApplicationView") {
      item.properties.installedPackageRevision.minimum = 0;
      item.properties.availablePackageRevision.minimum = 0;
      item.properties.adoptionState.enum = ["pending", "ready", "blocked"];
      item.properties.adoptionState.description = "Historical instances stay pending/blocked until read-only resource adoption succeeds. ready does not imply application upgrade or healthy runtime.";
    }
    if (type === "AgentView") {
      item.properties.capabilities.properties.executorVersions.description = "Supported executor protocol versions, keyed by runtime kind (for example docker/systemd). No application-ID allowlist.";
      item.properties.capabilities.properties.runtimeCapabilities.description = "Available execution capabilities. Availability is not administrator permission approval.";
    }
    operation.responses["200"].content["application/json"].schema = { type: "object", required: [field], additionalProperties: false, properties: { [field]: { type: "array", items: item } } };
  } else if (route.handler === "handleAdoptApplication") {
    operation.summary = "Verify and Adopt Historical Application Resources";
    operation.description = "Maintenance-only management-record adoption based on the historical installed manifest and actual owned resources. Does not pull, rebuild, restart, reconfigure, expand permissions or migrate application data. Requires separate Center, Agent and application-data backups. Unknown ownership or unresolved node work blocks adoption; no repair reinstall. A 202 response means queued, not adopted; observe task and application adoptionState.";
    const schema = operation.requestBody.content["application/json"].schema;
    schema.required = ["backupsConfirmed"];
    schema.properties.backupsConfirmed.const = true;
    operation.responses["202"].content["application/json"].schema = { type: "object", required: ["taskId"], additionalProperties: false, properties: { taskId: { type: "string", minLength: 1 } } };
  } else if (route.handler === "handleQueueApplicationMaintenance") {
    operation.summary = "Queue Managed Application Logs, Backup or Restore";
    operation.description = "Uses verified instance resource ownership and recorded package identity, never caller-supplied paths, commands or current catalog substitutions. Pending adoption or unresolved work blocks maintenance. Restore accepts only an instance-recorded backup from the same package version; cross-version rollback requires matched offline recovery. Unknown outcomes retain evidence and require reconciliation, not automatic replay. Logs are bounded and known delivered credentials are redacted.";
    const schema = operation.requestBody.content["application/json"].schema;
    schema.required = ["action"];
    schema.properties.action.enum = ["logs", "backup", "restore"];
    schema.properties.backupId = { type: "string", minLength: 1, maxLength: 128, pattern: /^[^/\\\x00\r\n]+$/.source, description: "Opaque backup identifier already recorded for this instance, not a host path." };
    schema.oneOf = [
      { properties: { action: { const: "restore" } }, required: ["backupId"] },
      { properties: { action: { enum: ["logs", "backup"] } }, not: { required: ["backupId"] } },
    ];
    operation.responses["202"].content["application/json"].schema = { type: "object", required: ["taskId"], additionalProperties: false, properties: { taskId: { type: "string", minLength: 1 } } };
  } else if (route.handler === "handleApplicationMaintenance") {
    operation.summary = "Read Application Maintenance Result";
    operation.description = "Returns task status and bounded redacted logs or an opaque backup ID. Never returns resource receipts, host backup paths or delivered secret values. A queued task is not proof of backup or restoration. reconciliationRequired means the actual outcome must be resolved before further changes.";
    operation.responses["200"].content["application/json"].schema = schemaForGoType("ApplicationMaintenanceView");
  } else if (route.handler === "handleApplicationBackups") {
    operation.summary = "List Recorded Application Backups";
    operation.description = "Returns instance-recorded backup IDs, package version, creation time, logical resource names and current restorable status. Does not discover arbitrary host files or expose backup paths/digests. restorable does not prove a successful restoration drill; cross-version and unverified historical Docker restores remain blocked.";
    operation.responses["200"].content["application/json"].schema = { type: "array", items: schemaForGoType("ApplicationBackupView") };
  }
  if (route.handler === "handleListSources" || route.handler === "handleListApps") {
    const sources = route.handler === "handleListSources";
    const field = sources ? "sources" : "apps";
    operation.description = sources
      ? "Catalog source status. The reserved vastora-official identity is anchored to independently provisioned TUF trust, not the editable source name or URL. Catalog revision and expiry describe the last verified cache; a failed refresh does not replace it."
      : "Available catalog entries with schema 4 runtime declarations, packageRevision and canonical manifestSha256. Unknown runtime capabilities block only the affected package/node. installBlocked entries remain displayable but cannot authorize install/upgrade. managedConfigFields are Center-controlled product inputs: clients must not render or send them. Existing configuration/uninstall uses recorded manifests.";
    const item = schemaForGoType(sources ? "CatalogSource" : "AppView");
    if (!sources) {
      item.required = ["key", "sourceId", "app", "fetchedAt", "manifestSha256"];
      item.properties.manifestSha256.pattern = "^[a-f0-9]{64}$";
      item.properties.managedConfigFields.description = "Configuration keys owned by the Center product integration, not browser input or secret values.";
    }
    operation.responses["200"].content["application/json"].schema = {
      type: "object", required: [field], additionalProperties: false,
      properties: { [field]: { type: "array", items: item } },
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
  } else if (route.handler === "handleMeridianTraffic") {
    operation.description = "Read directional traffic grouped by entry node and egress. Totals start at the first authenticated tracking sample, never infer historical direction, never reset or enforce quotas, and are not provider billing. Stale means no current active credential sample within 15 minutes.";
    operation.responses["200"].headers = noStoreHeaders;
    operation.responses["200"].content["application/json"].schema = schemaForGoType("MeridianTrafficView");
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

if (externalSchemas.CatalogAppManifest) {
  externalSchemas.CatalogAppManifest.required = ["id", "version", "packageRevision", "runtime", "name", "description", "license", "config"];
  externalSchemas.CatalogAppManifest.properties.packageRevision.minimum = 1;
  externalSchemas.CatalogAppManifest.properties.runtime = { $ref: "#/components/schemas/CatalogRuntimeSpec" };
}
if (externalSchemas.CatalogOfficialTarget) {
  externalSchemas.CatalogOfficialTarget.properties.catalog = schemaForGoType("catalog.Catalog");
  externalSchemas.CatalogCatalog.properties.schemaVersion = { type: "integer", const: 4 };
}
if (externalSchemas.CatalogRuntimeSpec) {
  externalSchemas.CatalogRuntimeSpec.required = ["kind", "version"];
  externalSchemas.CatalogRuntimeSpec.properties.version.minimum = 1;
  externalSchemas.CatalogRuntimeSpec.additionalProperties = true;
  externalSchemas.CatalogRuntimeSpec.description = "Versioned runtime contract. Future valid kinds/versions retain their signed fields for display but cannot execute without matching Agent support. Canonical validation is owned by the pinned catalog module.";
}
Object.assign(document.components.schemas, externalSchemas);
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
