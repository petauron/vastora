export type NodeSiteCertificate = {
  applicationId: string; hostname: string; port: number; revision: number; appliedRevision: number;
  state: "unconfigured" | "issuing" | "pending" | "ready" | "action_required" | "rolled_back";
  fingerprint?: string; notAfter?: string; error?: string; caDirectory?: string;
  migrationFrom?: string; tlsLoopbackOnly?: boolean;
};
export type NodeSiteCertificateInput = { hostname: string; port: number; caDirectory: string; previousHostname?: string; tlsLoopbackOnly?: boolean };
