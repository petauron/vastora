export type EgressPolicy = "auto" | "ipv4_only" | "ipv6_only";
export type NodeEgress = {
  policy: EgressPolicy;
  appliedPolicy: EgressPolicy;
  revision: number;
  state: string;
  error?: string;
  available: boolean;
  commandId?: string;
  verified?: { policy: EgressPolicy; configSha256: string; exits: string[]; checkedAt: string };
};
