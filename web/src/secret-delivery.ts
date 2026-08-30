const storagePrefix = "vastora.secret-delivery.v1:";

function storageKey(scope: string): string {
  return storagePrefix + encodeURIComponent(scope);
}

export function readSecretOperation(scope: string): string | null {
  try {
    return window.sessionStorage.getItem(storageKey(scope));
  } catch {
    return null;
  }
}

export function secretOperation(scope: string): string {
  const existing = readSecretOperation(scope);
  if (existing) return existing;
  const created = `${crypto.randomUUID()}-${crypto.randomUUID()}`;
  try {
    window.sessionStorage.setItem(storageKey(scope), created);
  } catch {
    // The in-memory caller still reuses the returned key for this page session.
  }
  return created;
}

export function clearSecretOperation(scope: string): void {
  try {
    window.sessionStorage.removeItem(storageKey(scope));
  } catch {
    // Storage may be unavailable in hardened browser contexts.
  }
}

export function deploymentSecretScope(agentId: string, appKey: string, operation: string): string {
  return `deployment:${agentId}:${appKey}:${operation}`;
}

export function commandSecretScope(applicationId: string, commandId: string): string {
  return `application-command:${applicationId}:${commandId}`;
}

export function commandSecretOperations(applicationId: string): Array<{ commandId: string; operationKey: string; scope: string }> {
  const prefix = `application-command:${applicationId}:`;
  const operations: Array<{ commandId: string; operationKey: string; scope: string }> = [];
  try {
    for (let index = 0; index < window.sessionStorage.length; index += 1) {
      const key = window.sessionStorage.key(index);
      if (!key?.startsWith(storagePrefix)) continue;
      const scope = decodeURIComponent(key.slice(storagePrefix.length));
      if (!scope.startsWith(prefix)) continue;
      const commandId = scope.slice(prefix.length);
      const operationKey = window.sessionStorage.getItem(key);
      if (commandId && operationKey) operations.push({ commandId, operationKey, scope });
    }
  } catch {
    return [];
  }
  return operations;
}
