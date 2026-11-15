// Read-only projection of Glivela's existing, secret-free business inventory.
export type SSHProtectionInventory = {
  endpoints: { entryId: string; nodeId: string; retired: boolean }[];
  runtimes: { id: string; role: string; publicSSHConfigured?: boolean; runtimeReady?: boolean; revision?: string; reportedAt?: string }[];
};
