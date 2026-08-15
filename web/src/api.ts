import type { AppView, CatalogSource, DashboardStatus } from "./types";

export class APIError extends Error {
  constructor(
    message: string,
    readonly status: number
  ) {
    super(message);
  }
}

function csrfToken(): string {
  return document.cookie
    .split("; ")
    .find((value) => value.startsWith("vastora_csrf="))
    ?.split("=")[1] ?? "";
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body) {
    headers.set("Content-Type", "application/json");
    headers.set("X-CSRF-Token", csrfToken());
  }
  const response = await fetch(path, { ...init, headers, credentials: "same-origin" });
  const body = (await response.json().catch(() => ({}))) as T & { error?: string };
  if (!response.ok) {
    throw new APIError(body.error ?? "Request failed", response.status);
  }
  return body;
}

export const api = {
  setupStatus: () => request<{ configured: boolean }>("/api/v1/setup/status"),
  setupAdmin: (bootstrapToken: string, password: string) =>
    request<{ configured: boolean }>("/api/v1/setup/admin", {
      method: "POST",
      body: JSON.stringify({ bootstrapToken, password })
    }),
  login: (password: string) =>
    request<{ authenticated: boolean }>("/api/v1/auth/login", {
      method: "POST",
      body: JSON.stringify({ password })
    }),
  logout: () => request<{ authenticated: boolean }>("/api/v1/auth/logout", { method: "POST", body: "{}" }),
  status: () => request<DashboardStatus>("/api/v1/status"),
  sources: () => request<{ sources: CatalogSource[] }>("/api/v1/catalog/sources"),
  apps: () => request<{ apps: AppView[] }>("/api/v1/catalog/apps"),
  createSource: (source: {
    id: string;
    displayName: string;
    url: string;
    publicKey: string;
    bearerToken: string;
    customCA: string;
    refreshIntervalSeconds: number;
  }) => request<{ id: string }>("/api/v1/catalog/sources", { method: "POST", body: JSON.stringify(source) }),
  refreshSource: (id: string) => request<{ sourceID: string; apps?: number; notModified: boolean }>(`/api/v1/catalog/sources/${encodeURIComponent(id)}/refresh`, { method: "POST", body: "{}" })
};
