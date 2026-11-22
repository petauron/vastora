export type SwapDevice = { name: string; type: string; sizeBytes: number; usedBytes: number; priority: number };
export type ZramDevice = { disksizeBytes: number; origDataBytes: number; algorithm: string };
export type SwapTargetPolicy = { tierMiB?: number; zramBytes?: number; zramAlgorithm?: string; zramPriority?: number; swapfileBytes?: number; swapfilePriority?: number; swappiness?: number };
export type NodeSwapTask = { action: "inspect" | "apply" | "restore"; revision?: string };
export type NodeSwapResult = { state: "eligible" | "applied" | "read_only" | "action_required"; revision: string; memoryTotalBytes: number; memoryAvailableBytes: number; diskFreeBytes: number; virtualization?: string; systemd: boolean; swappiness: number; swaps: SwapDevice[]; zram?: ZramDevice; target: SwapTargetPolicy; canApply?: boolean; canRestore?: boolean; reason?: string };
export type NodeSwap = { id: string; state: string; error?: string; result?: NodeSwapResult; updatedAt?: string };
