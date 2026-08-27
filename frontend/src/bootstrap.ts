import type { CurrentUser, UserSettings } from './types';

interface AppBootstrap {
    me?: CurrentUser;
    settings?: UserSettings;
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
