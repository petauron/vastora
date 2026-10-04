export type ReinstallClientCheck = {
  commandId: string;
  state: string;
  checkedAt?: string;
  applicationId?: string;
  verifierAgentId?: string;
  protocol?: string;
  accountName?: string;
  egressName?: string;
};
export type ReinstallClientCheckInput = {
  requestId: string;
  operationId: string;
  planRevision: string;
  applicationId: string;
  verifierAgentId: string;
};
