import '@testing-library/jest-dom/vitest';
import { cleanup, configure } from '@testing-library/preact';
import { afterEach } from 'vitest';

configure({ asyncUtilTimeout: 5_000 });

afterEach(() => {
  cleanup();
});

// jsdom has no modal dialog support, so model the parts the app uses.
Object.defineProperties(HTMLDialogElement.prototype, {
  showModal: {
    configurable: true,
    value(this: HTMLDialogElement) {
      this.open = true;
    },
  },
  close: {
    configurable: true,
    value(this: HTMLDialogElement) {
      if (!this.open) return;
      this.open = false;
      this.dispatchEvent(new Event('close'));
    },
  },
});

window.scrollTo = () => undefined;
