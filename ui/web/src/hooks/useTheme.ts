import { useCallback, useEffect, useState } from 'react';

const THEMES: readonly string[] = ['light-green', 'desert'];
const STORAGE_KEY = 'wedjat-theme';

function getInitialTheme(): string {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    if (saved && THEMES.includes(saved)) return saved;
  } catch { /* noop */ }
  return 'light-green';
}

export function useTheme() {
  const [theme, setThemeState] = useState<string>(getInitialTheme);

  const setTheme = useCallback((newTheme: string) => {
    if (!THEMES.includes(newTheme)) return;
    setThemeState(newTheme);
    document.documentElement.setAttribute('data-theme', newTheme);
    try {
      localStorage.setItem(STORAGE_KEY, newTheme);
    } catch { /* noop */ }
  }, []);

  const toggleTheme = useCallback(() => {
    setTheme(theme === 'light-green' ? 'desert' : 'light-green');
  }, [theme, setTheme]);

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme);
  }, [theme]);

  return { theme, setTheme, toggleTheme, isDesert: theme === 'desert' };
}
