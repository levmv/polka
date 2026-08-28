import type { CurrentUser, UserSettings } from './types';

interface AppBootstrap {
    me?: CurrentUser;
    settings?: UserSettings;
    send_enabled?: boolean;
}

// Fired when an admin flips the sending switch, so views already on screen can
// show or drop their Send affordance without a reload.
export const SEND_ENABLED_EVENT = 'polka:send-enabled';

let parsed = false;
let bootstrap: AppBootstrap = {};
let sendEnabledValue: boolean | null = null;

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

// Unlike the values above this is app-wide state an admin can flip mid-session,
// so it is read rather than taken, and kept current in place.
export function sendEnabled(): boolean {
    if (sendEnabledValue === null) sendEnabledValue = readBootstrap().send_enabled === true;
    return sendEnabledValue;
}

export function setSendEnabled(enabled: boolean): void {
    if (sendEnabled() === enabled) return;
    sendEnabledValue = enabled;
    window.dispatchEvent(new CustomEvent<boolean>(SEND_ENABLED_EVENT, { detail: enabled }));
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
