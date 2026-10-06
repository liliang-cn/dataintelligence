import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react';

// Light by default, like SmartFactory. index.html applies the stored choice
// before the first paint, so there is no flash.
type Theme = 'light' | 'dark';
const KEY = 'di.theme';
const Ctx = createContext<{ theme: Theme; resolvedTheme: Theme; setTheme: (t: Theme) => void }>({
  theme: 'light', resolvedTheme: 'light', setTheme: () => {},
});

function stored(): Theme {
  try {
    return localStorage.getItem(KEY) === 'dark' ? 'dark' : 'light';
  } catch {
    return 'light';
  }
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, set] = useState<Theme>(stored);
  useEffect(() => {
    document.documentElement.classList.toggle('dark', theme === 'dark');
    document.querySelector('meta[name=theme-color]')?.setAttribute('content', theme === 'dark' ? '#0e1324' : '#e9ecf7');
  }, [theme]);
  const setTheme = useCallback((t: Theme) => {
    set(t);
    try {
      localStorage.setItem(KEY, t);
    } catch {
      /* blocked storage: the choice lasts for this visit */
    }
  }, []);
  return <Ctx.Provider value={{ theme, resolvedTheme: theme, setTheme }}>{children}</Ctx.Provider>;
}

export const useTheme = () => useContext(Ctx);
