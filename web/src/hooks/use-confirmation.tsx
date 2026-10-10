import { useCallback, useEffect, useRef, useState } from "react";
import type { Language } from "../translations";
import { copy } from "../views/shared";
import { AlertDialog, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";

type Confirmation = { title: string; description: string; confirmLabel?: string; destructive?: boolean };

// Preserve the decision point of synchronous confirmations with an accessible,
// themed dialog. Render confirmation inside its owning Sheet/Dialog so Base UI
// manages nested focus and Escape. Dismissal and unmount resolve to cancellation.
export function useConfirmation(language: Language) {
  const [pending, setPending] = useState<Confirmation | null>(null);
  const resolve = useRef<((confirmed: boolean) => void) | null>(null);
  const settle = useCallback((confirmed: boolean) => {
    const done = resolve.current;
    resolve.current = null;
    setPending(null);
    done?.(confirmed);
  }, []);
  const confirm = useCallback((value: Confirmation) => new Promise<boolean>((done) => {
    resolve.current?.(false);
    resolve.current = done;
    setPending(value);
  }), []);
  useEffect(() => () => { resolve.current?.(false); resolve.current = null; }, []);
  const confirmation = <AlertDialog open={pending !== null} onOpenChange={(open) => { if (!open) settle(false); }}>
    <AlertDialogContent>
      <AlertDialogHeader><AlertDialogTitle>{pending?.title}</AlertDialogTitle><AlertDialogDescription>{pending?.description}</AlertDialogDescription></AlertDialogHeader>
      <AlertDialogFooter><Button variant="outline" onClick={() => settle(false)}>{copy(language, "取消", "Cancel")}</Button><Button variant={pending?.destructive ? "destructive" : "default"} onClick={() => settle(true)}>{pending?.confirmLabel ?? copy(language, "继续", "Continue")}</Button></AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>;
  return { confirm, confirmation };
}
