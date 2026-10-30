export type NodeSiteCertificate = {
  applicationId: string; hostname: string; port: number; revision: number; appliedRevision: number;
  state: "unconfigured" | "issuing" | "pending" | "ready" | "action_required" | "rolled_back";
  fingerprint?: string; notAfter?: string; error?: string; caDirectory?: string;
};
export type NodeSiteCertificateInput = { hostname: string; port: number; caDirectory: string };
