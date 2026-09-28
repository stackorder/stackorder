import { useState } from 'preact/hooks';

import { useApi } from '../api/context';
import type { UnlockResponse } from '../api/types';
import { Dialog } from './Dialog';

/** Props of UnlockDialog. */
export interface UnlockDialogProps {
  open: boolean;
  stackId: string;
  stackKey: string;
  onClose: () => void;
  onUnlocked: (res: UnlockResponse) => void;
}

/** Asks for a reason and releases a stack's orchestration lock through the API. */
export function UnlockDialog({ open, stackId, stackKey, onClose, onUnlocked }: UnlockDialogProps) {
  const api = useApi();
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();

  const close = () => {
    setReason('');
    setError(undefined);
    setBusy(false);
    onClose();
  };

  const submit = async (e: Event) => {
    e.preventDefault();
    const trimmed = reason.trim();
    if (!trimmed) {
      setError('Give a reason; it is recorded in the audit log.');
      return;
    }
    setBusy(true);
    setError(undefined);
    try {
      const res = await api.unlockStack(stackId, { reason: trimmed });
      setReason('');
      setBusy(false);
      onUnlocked(res);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onClose={close} labelledBy="unlock-title">
      <form class="dialog__body" onSubmit={(e) => void submit(e)} noValidate>
        <h2 id="unlock-title">Unlock {stackKey}</h2>
        <p>
          Releasing the orchestration lock lets another pull request apply this stack. The state lock in S3 is not
          touched. The release and its reason are audited.
        </p>
        <label for="unlock-reason">Reason</label>
        <textarea
          id="unlock-reason"
          name="reason"
          rows={3}
          required
          value={reason}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? 'unlock-error' : undefined}
          onInput={(e) => {
            setReason(e.currentTarget.value);
          }}
        />
        {error && (
          <p id="unlock-error" class="form-error" role="alert">
            {error}
          </p>
        )}
        <div class="dialog__actions">
          <button type="button" class="button" onClick={close}>
            Cancel
          </button>
          <button type="submit" class="button button--danger" disabled={busy}>
            {busy ? 'Unlocking…' : 'Unlock'}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
