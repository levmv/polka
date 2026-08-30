import { fetchCurrentUser } from './api';
import { textEl } from './dom';
import { type IconName, iconElement } from './icons';
import { openSettingsModal } from './settings';
import type { CurrentUser } from './types';

export function initSidebarAccount(closeSidebar: () => void): void {
    const root = document.getElementById('sidebar-account');
    if (!root) return;

    fetchCurrentUser()
        .then((currentUser) => {
            root.replaceChildren(
                renderSettingsRow(currentUser, closeSidebar),
                renderLogoutRow(currentUser.username),
            );
            root.hidden = false;
        })
        .catch(() => {
            root.hidden = true;
        });
}

function renderSettingsRow(currentUser: CurrentUser, closeSidebar: () => void): HTMLElement {
    const button = accountRow('settings', 18, 'Settings');
    button.classList.add('account-settings');
    button.addEventListener('click', () => {
        // A modal, not a route: the router's close-on-navigate never runs.
        closeSidebar();
        openSettingsModal(currentUser);
    });
    return button;
}

function renderLogoutRow(username: string): HTMLElement {
    const button = accountRow('logout', 14, 'Log out');
    button.classList.add('account-logout');
    button.append(textEl('span', 'account-name', username));
    button.addEventListener('click', () => {
        showLogoutConfirm(button, username);
    });
    return button;
}

// Logging out is reversible, but this is a full-width row at the bottom of a
// drawer, where a stray thumb lands — so it asks first, in place, the way a
// shelf row confirms its delete.
function showLogoutConfirm(row: HTMLElement, username: string): void {
    const bar = document.createElement('div');
    bar.className = 'account-confirm';
    bar.innerHTML = `
        <span class="account-confirm-text">Log out?</span>
        <button class="account-confirm-yes" type="button">Yes</button>
        <button class="account-confirm-no" type="button">Cancel</button>
    `;
    row.replaceWith(bar);

    const yes = bar.querySelector('.account-confirm-yes') as HTMLButtonElement;
    const no = bar.querySelector('.account-confirm-no') as HTMLButtonElement;

    no.addEventListener('click', () => {
        const restored = renderLogoutRow(username);
        bar.replaceWith(restored);
        restored.focus({ preventScroll: true });
    });

    yes.addEventListener('click', () => {
        yes.disabled = true;
        no.disabled = true;
        submitLogout();
    });

    // Enter should cancel, not sign out.
    no.focus();
}

function accountRow(icon: IconName, iconSize: number, label: string): HTMLButtonElement {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'nav-item account-row';
    button.append(iconElement(icon, iconSize), textEl('span', 'account-label', label));
    return button;
}

function submitLogout(): void {
    const form = document.createElement('form');
    form.method = 'post';
    form.action = '/logout';
    form.hidden = true;
    document.body.appendChild(form);
    form.submit();
}
