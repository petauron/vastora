export type SecurityRule = { protocol: "tcp" | "udp"; port: number };
export type SecurityPolicy = { publicInterfaces: string[]; managementSources: string[]; sshPorts: number[]; publicRules: SecurityRule[] };
export type SecurityTask = { action: "inspect" | "prepare" | "preview" | "apply" | "confirm" | "disable" | "unban"; policy: SecurityPolicy; revision?: string; address?: string };
export type SecurityResult = { state: "unconfigured" | "preview" | "pending_confirmation" | "verified" | "rolled_back" | "action_required"; revision?: string; deadline?: string; appliedAt?: string; policy: SecurityPolicy; listeners: string[]; banned: string[]; reasons: string[]; supported: boolean };
export type NodeSecurity = { id: string; state: string; error?: string; result?: SecurityResult; updatedAt?: string };
