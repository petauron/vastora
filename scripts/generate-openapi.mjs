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
const internalSource = fs.readdirSync(path.join(root, "internal"), { withFileTypes: true })
  .filter((entry) => entry.isDirectory())
  .flatMap((entry) => {
    const directory = path.join(root, "internal", entry.name);
    return fs.readdirSync(directory)
      .filter((name) => name.endsWith(".go") && !name.endsWith("_test.go"))
      .map((name) => fs.readFileSync(path.join(directory, name), "utf8"));
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
    "three-x-ui-migrations": "Applications",
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
    const match = internalSource.match(new RegExp(`type\\s+${name}\\s+struct\\s*\\{([\\s\\S]*?)\\n\\}`));
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

const publicationResponseSchema = schemaForGoType("PublicationView");
publicationResponseSchema.required = ["id", "serviceId", "kind", "ingress", "hostname", "dnsProvider", "tlsEnabled", "desiredRevision", "appliedRevision", "status", "createdAt", "updatedAt"];
publicationResponseSchema.properties.ingress = { $ref: "#/components/schemas/PublicationIngress" };

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
          error: { type: "string", example: "center: request is invalid" },
        },
      },
      JsonObject: { type: "object", additionalProperties: true, description: "Endpoint-specific JSON object. Runtime decoding rejects fields not declared by the corresponding Go request type." },
      PublicationIngress: publicationIngressResponseSchema,
      Publication: publicationResponseSchema,
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
  if (source.includes("decodeJSON(request")) {
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
  if (route.handler === "handleCreateRealityCommand") {
    operation.requestBody.content["application/json"].schema.required = [
      "applicationId", "regionCode", "name",
      "dnsProvider", "targetHost", "serverName",
    ];
  } else if (route.handler === "handleCreatePublication") {
    const schema = operation.requestBody.content["application/json"].schema;
    schema.required = ["serviceId", "kind", "ingress", "dnsProvider"];
    schema.properties.ingress = publicationIngressRequestSchema;
  } else if (route.handler === "handleVerifyRealityTarget") {
    operation.requestBody.content["application/json"].schema.required = ["targetHost", "serverName"];
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
