import type { ComponentChildren } from 'preact';
import { useEffect, useRef } from 'preact/hooks';

/** Props of Dialog. */
export interface DialogProps {
  open: boolean;
  onClose: () => void;
  labelledBy: string;
  class?: string;
  children: ComponentChildren;
}

/** A native modal dialog, opened and closed by the open prop; Escape and the close button call onClose. */
export function Dialog({ open, onClose, labelledBy, class: className, children }: DialogProps) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    if (!open && dialog.open) dialog.close();
  }, [open]);
  return (
    <dialog
      ref={ref}
      class={`dialog${className ? ` ${className}` : ''}`}
      aria-labelledby={labelledBy}
      onClose={() => {
        if (open) onClose();
      }}
    >
      {open && children}
    </dialog>
  );
}
