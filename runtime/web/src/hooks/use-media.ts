import { useSyncExternalStore } from 'react';

export function useMedia(query: string) {
  return useSyncExternalStore(
    (cb) => {
      const m = window.matchMedia(query);
      m.addEventListener('change', cb);
      return () => m.removeEventListener('change', cb);
    },
    () => window.matchMedia(query).matches,
    () => false,
  );
}

/** Phones get bottom sheets where desktops get dialogs and side panels. */
export const useIsPhone = () => useMedia('(max-width: 767px)');
