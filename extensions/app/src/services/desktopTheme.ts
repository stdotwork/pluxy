import { invoke } from '@tauri-apps/api/core';
import type { ThemeSelection } from '../themes/types';
import type { AppTheme } from '../themeController';

export const getThemeSelection = (): Promise<ThemeSelection | null> =>
  invoke<ThemeSelection | null>('get_theme_selection');

export const saveThemeSelection = (args: { selection: ThemeSelection; }): Promise<void> =>
  invoke<void>('save_theme_selection', args);

export const detectCorePlatform = (): Promise<{ os: string }> =>
  invoke<{ os: string }>('detect_core_platform');

export const getLinuxSystemTheme = (): Promise<AppTheme | null> =>
  invoke<AppTheme | null>('get_linux_system_theme');
