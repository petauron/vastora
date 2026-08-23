import { useCallback, useEffect, useRef, useState } from "react";
import type { ApplicationCommand } from "../types";

type StartCommand = () => Promise<ApplicationCommand>;
type AdoptCommand = (command: ApplicationCommand) => void;

function waitForCommand(commandId: string, signal: AbortSignal, adopt: AdoptCommand): Promise<ApplicationCommand | null> {
  return new Promise((resolve, reject) => {
    const source = new EventSource(`/api/v1/application-commands/${encodeURIComponent(commandId)}/events`, { withCredentials: true });
    let settled = false;
    let timeout = 0;
    let abort = () => {};
    const finish = (settle: () => void) => {
      if (settled) return;
      settled = true;
      window.clearTimeout(timeout);
      signal.removeEventListener("abort", abort);
      source.close();
      settle();
    };
    abort = () => finish(() => resolve(null));
    timeout = window.setTimeout(() => finish(() => reject(new Error("The node did not respond in time"))), 120_000);
    signal.addEventListener("abort", abort, { once: true });
    source.onmessage = (event) => {
      try {
        const command = JSON.parse(event.data) as ApplicationCommand;
        adopt(command);
        if (command.state !== "pending" && command.state !== "running") {
          finish(() => resolve(command));
        }
      } catch {
        finish(() => reject(new Error("Center returned an invalid command event")));
      }
    };
  });
}

export function useApplicationCommandExecutor(scopeKey?: string) {
  const activeController = useRef<AbortController | null>(null);
  const [running, setRunning] = useState(false);

  useEffect(() => {
    activeController.current?.abort();
    activeController.current = null;
    setRunning(false);
    return () => {
      activeController.current?.abort();
      activeController.current = null;
    };
  }, [scopeKey]);

  const execute = useCallback(async (start: StartCommand, adopt: AdoptCommand): Promise<ApplicationCommand | null> => {
    activeController.current?.abort();
    const controller = new AbortController();
    activeController.current = controller;
    setRunning(true);
    try {
      const command = await start();
      if (controller.signal.aborted) return null;
      adopt(command);
      if (command.state !== "pending" && command.state !== "running") {
        return command;
      }
      return await waitForCommand(command.id, controller.signal, adopt);
    } finally {
      if (activeController.current === controller) {
        activeController.current = null;
        setRunning(false);
      }
    }
  }, [scopeKey]);

  return { execute, running };
}
