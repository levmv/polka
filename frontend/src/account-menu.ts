import { fetchCurrentUser } from './api';
import { createMenu } from './menu';
import { openSettingsModal } from './settings';

export function initSidebarAccount(): void {
    const root = document.getElementById('sidebar-account');
    if (!root) return;

    fetchCurrentUser()
        .then((currentUser) => {
            root.replaceChildren(renderAccountButton(currentUser.username));
            root.hidden = false;

            const trigger = root.querySelector<HTMLButtonElement>('.account-trigger');
            if (!trigger) return;
            createMenu(trigger, [
                {
                    label: 'Settings',
                    action: () => {
                        openSettingsModal(currentUser);
                    },
                },
                {
                    label: 'Log out',
                    action: () => {
                        submitLogout();
                    },
                },
            ]);
        })
        .catch(() => {
            root.hidden = true;
        });
}

function renderAccountButton(username: string): HTMLElement {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'account-trigger';
    button.setAttribute('aria-label', `Account menu for ${username}`);

    const avatar = document.createElement('span');
    avatar.className = 'account-avatar';
    avatar.setAttribute('aria-hidden', 'true');
    avatar.textContent = accountInitial(username);

    const name = document.createElement('span');
    name.className = 'account-name';
    name.textContent = username;

    const caret = document.createElement('span');
    caret.className = 'account-caret';
    caret.setAttribute('aria-hidden', 'true');

    button.append(avatar, name, caret);
    return button;
}

function accountInitial(username: string): string {
    const trimmed = username.trim();
    return (trimmed[0] || '?').toUpperCase();
}

function submitLogout(): void {
    const form = document.createElement('form');
    form.method = 'post';
    form.action = '/logout';
    form.hidden = true;
    document.body.appendChild(form);
    form.submit();
}
