import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react';
import { api, ApiError, type ConsoleInfo, type Me } from './api';

type Session = {
  info: ConsoleInfo;
  me: Me | null;
  /** null while loading, false when the server would not say who we are */
  state: 'loading' | 'signed-in' | 'signed-out' | 'offline';
  error?: string;
  refresh: () => Promise<Me | null>;
};

const fallback: ConsoleInfo = { title: '经营顾问', auth: 'users', copilot: true, consult: true };
const Ctx = createContext<Session>({ info: fallback, me: null, state: 'loading', refresh: async () => null });

export function SessionProvider({ children }: { children: ReactNode }) {
  const [info, setInfo] = useState<ConsoleInfo>(fallback);
  const [me, setMe] = useState<Me | null>(null);
  const [state, setState] = useState<Session['state']>('loading');
  const [error, setError] = useState<string>();

  const refresh = useCallback(async () => {
    try {
      const m = await api<Me>('/v1/whoami');
      setMe(m);
      setState('signed-in');
      setError(undefined);
      return m;
    } catch (e) {
      setMe(null);
      if (e instanceof ApiError && e.status === 401) {
        setState('signed-out');
      } else {
        setState('offline');
        setError(e instanceof Error ? e.message : String(e));
      }
      return null;
    }
  }, []);

  useEffect(() => {
    api<ConsoleInfo>('/v1/console')
      .then((i) => setInfo({ ...fallback, ...i, title: i.title || fallback.title }))
      .catch(() => {});
    refresh();
  }, [refresh]);

  useEffect(() => {
    document.title = info.title;
  }, [info.title]);

  return <Ctx.Provider value={{ info, me, state, error, refresh }}>{children}</Ctx.Provider>;
}

export const useSession = () => useContext(Ctx);
