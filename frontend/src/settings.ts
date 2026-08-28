import { sendEnabled } from './bootstrap';
import { openModal } from './modal';
import { createAppsPanel } from './settings/apps';
import { createDevicesPanel } from './settings/devices';
import { createGeneralPanel } from './settings/general';
import { createStoragePanel } from './settings/storage';
import { buttonEl } from './settings/ui';
import { createUsersPanel } from './settings/users';
import type { CurrentUser } from './types';

// Settings is a left-rail modal with a few focused sections:
//   General  — important standalone preferences and admin-wide library policy.
//   Devices  — email delivery settings, reader addresses, and recent sends.
//   Users    — the people who can sign in. A flat list: your own row first,
//              and (for admins) everyone else, all managed through small modals.
//   Reading apps — how external readers connect: OPDS catalog + app passwords.
//   Storage  — admin-only managed paths, layout, import, and ingest status.
// Mutations (passwords, new users, new tokens) open a stacked secondary modal
// rather than an inline form, so each panel stays a calm list. Their results are
// reported through a toast, not an inline status line.
type SettingsTab = 'general' | 'devices' | 'users' | 'apps' | 'storage';

const TAB_LABELS: Record<SettingsTab, string> = {
    general: 'General',
    devices: 'Devices',
    storage: 'Storage',
    users: 'Users',
    apps: 'Reading apps',
};

export function openSettingsModal(
    currentUser: CurrentUser,
    initialTab: SettingsTab = 'general',
): void {
    const { modal, root } = openModal({
        body: `
            <div class="settings-sidebar">
                <div class="settings-sidebar-title" id="settings-title">Settings</div>
                <div class="settings-tabs" role="tablist" aria-label="Settings sections"></div>
            </div>
            <div class="settings-panel"></div>
        `,
        bodyClass: 'settings-body',
        modalClass: 'settings-modal',
        backdropClass: 'modal-wide settings-backdrop',
        labelledBy: 'settings-title',
        closeExisting: true,
    });

    const tabs = root.querySelector<HTMLElement>('.settings-tabs');
    const panel = root.querySelector<HTMLElement>('.settings-panel');
    if (!tabs || !panel) {
        root.remove();
        return;
    }

    const storagePanel = createStoragePanel();
    const panels: Record<SettingsTab, (root: HTMLElement) => void> = {
        general: createGeneralPanel(currentUser),
        devices: createDevicesPanel(currentUser),
        storage: storagePanel.render,
        users: createUsersPanel(currentUser),
        apps: createAppsPanel(currentUser),
    };

    // Admins need Devices to enable sending; other roles see it only when enabled.
    const availableTabs: SettingsTab[] = ['general'];
    if (sendEnabled() || currentUser.role === 'admin') availableTabs.push('devices');
    if (currentUser.role === 'admin') availableTabs.push('storage');
    availableTabs.push('users', 'apps');
    let activeTab: SettingsTab = availableTabs.includes(initialTab) ? initialTab : 'general';

    // Per-tab containers keep late async renders from overwriting the active tab.
    const containers = new Map<SettingsTab, HTMLElement>();
    const renderPanel = () => {
        let container = containers.get(activeTab);
        if (!container) {
            container = document.createElement('div');
            containers.set(activeTab, container);
        }
        panel.replaceChildren(container);
        panels[activeTab](container);
    };

    // Storage shows live counts and free space. Refresh quietly whenever the tab
    // is reopened, retaining its current values until the request completes.
    const refreshStorageSilently = () => {
        storagePanel.refresh(() => {
            if (activeTab === 'storage') renderPanel();
        });
    };

    const tabButtons = renderTabs(tabs, availableTabs, activeTab, (tab) => {
        if (tab === activeTab) return;
        activeTab = tab;
        updateTabSelection(tabButtons, activeTab);
        renderPanel();
        if (tab === 'storage') refreshStorageSilently();
    });

    renderPanel();
    modal.open(
        tabButtons.get(activeTab) || root.querySelector<HTMLElement>('.settings-tab') || undefined,
    );
}

function renderTabs(
    root: HTMLElement,
    tabs: SettingsTab[],
    activeTab: SettingsTab,
    onSelect: (tab: SettingsTab) => void,
): Map<SettingsTab, HTMLButtonElement> {
    root.replaceChildren();
    const buttons = new Map<SettingsTab, HTMLButtonElement>();
    for (const tab of tabs) {
        const button = buttonEl('settings-tab', TAB_LABELS[tab], () => onSelect(tab));
        button.setAttribute('role', 'tab');
        button.setAttribute('aria-selected', String(tab === activeTab));
        root.appendChild(button);
        buttons.set(tab, button);
    }
    return buttons;
}

function updateTabSelection(
    buttons: Map<SettingsTab, HTMLButtonElement>,
    activeTab: SettingsTab,
): void {
    for (const [tab, button] of buttons) {
        button.setAttribute('aria-selected', String(tab === activeTab));
    }
}
