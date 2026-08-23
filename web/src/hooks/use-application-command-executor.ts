import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../api";
import type { ApplicationCommand } from "../types";

type StartCommand = () => Promise<ApplicationCommand>;
type AdoptCommand = (command: ApplicationCommand) => void;

function waitForNextPoll(signal: AbortSignal) {
  return new Promise<void>((resolve) => {
    const finish = () => {
      signal.removeEventListener("abort", abort);
      resolve();
    };
    const abort = () => {
      window.clearTimeout(timer);
      finish();
    };
    const timer = window.setTimeout(finish, 1000);
    signal.addEventListener("abort", abort, { once: true });
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
      let command = await start();
      if (controller.signal.aborted) return null;
      adopt(command);
      for (let attempt = 0; command.state === "pending" || command.state === "running"; attempt += 1) {
        if (attempt >= 120) throw new Error("The node did not respond in time");
        await waitForNextPoll(controller.signal);
        if (controller.signal.aborted) return null;
        try {
          command = await api.applicationCommand(command.id);
        } catch (error) {
          if (attempt === 119) throw error;
          continue;
        }
        if (controller.signal.aborted) return null;
        adopt(command);
      }
      return command;
    } finally {
      if (activeController.current === controller) {
        activeController.current = null;
        setRunning(false);
      }
    }
  }, [scopeKey]);

  return { execute, running };
}
