import type { CurrentUser, UserSettings } from './types';

interface AppBootstrap {
    me?: CurrentUser;
    settings?: UserSettings;
    version?: string;
    send_enabled?: boolean;
}

let parsed = false;
let bootstrap: AppBootstrap = {};

export function takeBootstrapCurrentUser(): CurrentUser | undefined {
    const data = readBootstrap();
    const value = data.me;
    delete data.me;
    return value;
}

export function takeBootstrapUserSettings(): UserSettings | undefined {
    const data = readBootstrap();
    const value = data.settings;
    delete data.settings;
    return value;
}

export function appVersion(): string {
    return readBootstrap().version || '';
}

export function initialSendEnabled(): boolean {
    return readBootstrap().send_enabled === true;
}

function readBootstrap(): AppBootstrap {
    if (parsed) return bootstrap;
    parsed = true;

    const el = document.getElementById('polka-bootstrap');
    if (!el?.textContent) return bootstrap;

    try {
        const data = JSON.parse(el.textContent) as AppBootstrap;
        if (data && typeof data === 'object') bootstrap = data;
    } catch {
        bootstrap = {};
    }
    return bootstrap;
}
