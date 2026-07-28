export type AppTheme = 'light' | 'dark';

const STORAGE_KEY = 'easy-cli-proxy-api.theme';

export function detectInitialTheme(): AppTheme {
  if (typeof window === 'undefined') return 'light';

  try {
    const saved = window.localStorage.getItem(STORAGE_KEY);
    if (saved === 'light' || saved === 'dark') return saved;
  } catch {
    // Storage may be unavailable in a restricted WebView; use the OS preference.
  }

  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

export function applyTheme(theme: AppTheme): void {
  document.documentElement.dataset.theme = theme;
  document.documentElement.style.colorScheme = theme;
}

export function saveTheme(theme: AppTheme): void {
  applyTheme(theme);
  try {
    window.localStorage.setItem(STORAGE_KEY, theme);
  } catch {
    // The in-memory theme still works when persistent storage is unavailable.
  }
}

