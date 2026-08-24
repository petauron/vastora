import { useCallback, useEffect, useRef, useState } from "react";
import { APIError, api } from "../api";
import type { ApplicationCommand } from "../types";

type StartCommand = () => Promise<ApplicationCommand>;
type AdoptCommand = (command: ApplicationCommand) => void;

const commandWatchdogMilliseconds = 120_000;
const maximumReconnectDelayMilliseconds = 4_000;

function waitForCommand(commandId: string, signal: AbortSignal, adopt: AdoptCommand): Promise<ApplicationCommand | null> {
  return new Promise((resolve, reject) => {
    let source: EventSource | null = null;
    let settled = false;
    let watchdogTimer = 0;
    let reconnectTimer = 0;
    let reconnectAttempts = 0;
    let recovering = false;
    let abort = () => {};
    const finish = (settle: () => void) => {
      if (settled) return;
      settled = true;
      window.clearTimeout(watchdogTimer);
      window.clearTimeout(reconnectTimer);
      signal.removeEventListener("abort", abort);
      source?.close();
      source = null;
      settle();
    };
    abort = () => finish(() => resolve(null));
    const reconnect = () => {
      if (settled || signal.aborted) return;
      window.clearTimeout(reconnectTimer);
      const delay = Math.min(maximumReconnectDelayMilliseconds, 500 * 2 ** Math.min(reconnectAttempts, 3));
      reconnectTimer = window.setTimeout(connect, delay);
    };
    const recover = async () => {
      if (settled || recovering) return;
      recovering = true;
      try {
        const current = await api.applicationCommand(commandId);
        if (settled) return;
        adopt(current);
        if (current.state !== "pending" && current.state !== "running") {
          finish(() => resolve(current));
          return;
        }
        reconnectAttempts += 1;
        reconnect();
      } catch (error) {
        if (settled) return;
        if (error instanceof APIError && error.status === 401) {
          finish(() => reject(new Error("Your Center session expired. Sign in again and retry.")));
          return;
        }
        reconnectAttempts += 1;
        reconnect();
      } finally {
        recovering = false;
      }
    };
    const armWatchdog = (currentSource: EventSource) => {
      window.clearTimeout(watchdogTimer);
      watchdogTimer = window.setTimeout(() => {
        if (settled || source !== currentSource) return;
        currentSource.close();
        source = null;
        void recover();
      }, commandWatchdogMilliseconds);
    };
    function connect() {
      if (settled || signal.aborted) return;
      try {
        const nextSource = new EventSource(`/api/v1/application-commands/${encodeURIComponent(commandId)}/events`, { withCredentials: true });
        source = nextSource;
        nextSource.onmessage = (event) => {
          try {
            const command = JSON.parse(event.data) as ApplicationCommand;
            reconnectAttempts = 0;
            adopt(command);
            if (command.state !== "pending" && command.state !== "running") {
              finish(() => resolve(command));
            } else {
              armWatchdog(nextSource);
            }
          } catch {
            finish(() => reject(new Error("Center returned an invalid command event")));
          }
        };
        nextSource.onerror = () => {
          if (settled || source !== nextSource) return;
          nextSource.close();
          source = null;
          window.clearTimeout(watchdogTimer);
          void recover();
        };
        armWatchdog(nextSource);
      } catch {
        void recover();
      }
    }
    signal.addEventListener("abort", abort, { once: true });
    connect();
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
