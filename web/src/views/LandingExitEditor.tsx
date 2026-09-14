import type { ReactElement, ReactNode } from "react";
import { Popover } from "@base-ui/react/popover";
import { XIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { useIsMobile } from "@/hooks/use-mobile";

// Both layouts share the same controlled draft, validation and save action.
export function LandingExitEditor({ open, onOpenChange, busy, trigger, title, description, closeLabel, children, footer }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  busy: boolean;
  trigger: ReactElement;
  title: string;
  description: string;
  closeLabel: string;
  children: ReactNode;
  footer: ReactNode;
}) {
  const mobile = useIsMobile();
  const changeOpen = (next: boolean) => { if (!busy) onOpenChange(next); };
  if (mobile) return <Sheet open={open} onOpenChange={changeOpen}>
    <SheetTrigger render={trigger} />
    <SheetContent className="apps-workspace apps-exit-sheet" showCloseButton={!busy}>
      <SheetHeader>
        <SheetTitle>{title}</SheetTitle>
        <SheetDescription>{description}</SheetDescription>
      </SheetHeader>
      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-4">{children}</div>
      <Separator />
      <SheetFooter>{footer}</SheetFooter>
    </SheetContent>
  </Sheet>;

  return <Popover.Root open={open} onOpenChange={changeOpen} modal>
    <Popover.Trigger render={trigger} />
    <Popover.Portal>
      <Popover.Positioner side="bottom" align="start" sideOffset={8} collisionPadding={16}>
        <Popover.Popup className="apps-workspace apps-exit-popover flex flex-col overflow-hidden rounded-xl border border-border bg-popover text-popover-foreground shadow-lg outline-none">
          <div className="relative flex shrink-0 flex-col gap-1.5 p-4 pr-12">
            <Popover.Title className="break-words text-sm font-medium">{title}</Popover.Title>
            <Popover.Description className="text-xs leading-relaxed text-muted-foreground">{description}</Popover.Description>
            <Popover.Close disabled={busy} render={<Button variant="ghost" size="icon-sm" className="absolute right-2 top-2" aria-label={closeLabel} />}><XIcon aria-hidden="true" /></Popover.Close>
          </div>
          <div className="min-h-0 overflow-y-auto px-4 pb-4">{children}</div>
          <Separator />
          <div className="flex shrink-0 justify-end gap-2 p-3">{footer}</div>
        </Popover.Popup>
      </Popover.Positioner>
    </Popover.Portal>
  </Popover.Root>;
}
