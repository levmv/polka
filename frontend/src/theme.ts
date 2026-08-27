import type { ThemePreference } from './types';

const THEME_STORAGE_KEY = 'polka-theme';

export function applyCachedTheme(): void {
    const cached = localStorage.getItem(THEME_STORAGE_KEY);
    if (isThemePreference(cached)) {
        applyTheme(cached);
    }
}

export function applyTheme(theme: ThemePreference): void {
    if (theme === 'system') {
        document.documentElement.removeAttribute('data-theme');
    } else {
        document.documentElement.dataset.theme = theme;
    }
    localStorage.setItem(THEME_STORAGE_KEY, theme);
}

function isThemePreference(value: string | null): value is ThemePreference {
    return value === 'system' || value === 'light' || value === 'dark' || value === 'sepia';
}
