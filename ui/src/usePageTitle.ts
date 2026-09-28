import { useEffect } from 'preact/hooks';

/** Sets the document title for the current page. */
export function usePageTitle(title: string | undefined): void {
  useEffect(() => {
    document.title = title ? `${title} · Stackorder` : 'Stackorder';
  }, [title]);
}
