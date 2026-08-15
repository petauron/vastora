export type LocalizedText = { en: string; "zh-CN": string };

export type CatalogSource = {
  id: string;
  displayName: string;
  url: string;
  publicKey: string;
  customCASet: boolean;
  bearerTokenSet: boolean;
  enabled: boolean;
  refreshIntervalSeconds: number;
  fetchedAt?: string;
  lastError?: string;
};

export type DashboardStatus = {
  version: string;
  catalogSources: number;
  catalogApps: number;
  nodes: number;
  deployments: number;
};

export type AppView = {
  key: string;
  sourceId: string;
  app: {
    id: string;
    version: string;
    name: LocalizedText;
    description: LocalizedText;
  };
  fetchedAt: string;
};
