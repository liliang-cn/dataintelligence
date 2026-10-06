import type { ReactNode } from 'react';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from '@/components/ui/sheet';
import { useIsPhone } from '@/hooks/use-media';
import { cn } from '@/lib/utils';

/** A dialog on a desktop, a bottom sheet on a phone. */
export function Panel({
  open, onOpenChange, title, description, children, footer, wide,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
}) {
  const phone = useIsPhone();
  if (phone) {
    return (
      <Sheet open={open} onOpenChange={onOpenChange}>
        <SheetContent side="bottom" className="max-h-[88dvh] gap-0 rounded-t-3xl pb-[env(safe-area-inset-bottom)]">
          <div className="mx-auto mt-2.5 h-1.5 w-10 shrink-0 rounded-full bg-muted" aria-hidden />
          <SheetHeader className="px-5 pt-3 pb-3 text-left">
            <SheetTitle className="pr-8 text-base leading-snug font-bold">{title}</SheetTitle>
            {description && <SheetDescription>{description}</SheetDescription>}
          </SheetHeader>
          <div className="min-h-0 flex-1 overflow-y-auto px-5 pb-4">{children}</div>
          {footer && <SheetFooter className="border-t px-5 py-3">{footer}</SheetFooter>}
        </SheetContent>
      </Sheet>
    );
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={cn('max-h-[86dvh] grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0', wide ? 'sm:max-w-3xl' : 'sm:max-w-lg')}>
        <DialogHeader className="px-6 pt-6 pb-4">
          <DialogTitle className="pr-8 text-lg leading-snug font-bold">{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        <div className="min-h-0 overflow-y-auto px-6 pb-6">{children}</div>
        {footer && <DialogFooter className="m-0 rounded-none border-t bg-surface px-6 py-4">{footer}</DialogFooter>}
      </DialogContent>
    </Dialog>
  );
}
