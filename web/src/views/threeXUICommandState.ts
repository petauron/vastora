import type { ApplicationCommand } from "../types";

export function mergeCommandUpdate(current: ApplicationCommand | null, next: ApplicationCommand): ApplicationCommand {
  if (!current) return next;
  return {
    ...next,
    clients: next.clientsObserved ? next.clients : current.clients,
    clientsObserved: next.clientsObserved || current.clientsObserved,
    inbounds: next.inboundsObserved ? next.inbounds : current.inbounds,
    inboundsObserved: next.inboundsObserved || current.inboundsObserved,
    subscriptionAvailable: next.subscriptionAvailable ?? current.subscriptionAvailable
  };
}

export function mergeCachedCommand(current: ApplicationCommand | null, cached: ApplicationCommand): ApplicationCommand {
  if (!current) return cached;
  return {
    ...current,
    clients: current.clientsObserved ? current.clients : cached.clients,
    clientsObserved: current.clientsObserved || cached.clientsObserved,
    inbounds: current.inboundsObserved ? current.inbounds : cached.inbounds,
    inboundsObserved: current.inboundsObserved || cached.inboundsObserved,
    subscriptionAvailable: current.subscriptionAvailable ?? cached.subscriptionAvailable
  };
}

export function hasObservedThreeXUIState(command: ApplicationCommand): boolean {
  return Boolean(command.clientsObserved || command.inboundsObserved);
}
