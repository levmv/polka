import { openModal } from './modal';
import { createAppsPanel } from './settings/apps';
import { createDevicesPanel } from './settings/devices';
import { createGeneralPanel } from './settings/general';
import { sendingSetting } from './settings/state';
import { createStoragePanel } from './settings/storage';
import { buttonEl, type SettingsPanel } from './settings/ui';
import { createUsersPanel } from './settings/users';
import type { CurrentUser } from './types';

type SettingsTab = 'general' | 'storage' | 'apps' | 'users' | 'devices';
type SettingsDestination = SettingsTab | 'email-recipients';

const TAB_LABELS: Record<SettingsTab, string> = {
    general: 'General',
    storage: 'Storage',
    apps: 'Reading apps',
    users: 'Users',
    devices: 'Email delivery',
};

export function openSettingsModal(
    currentUser: CurrentUser,
    destination: SettingsDestination = 'general',
): void {
    const initialTab = destination === 'email-recipients' ? 'devices' : destination;
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
        onClose: () => {
            for (const panel of Object.values(panels)) panel.unmount?.();
        },
    });

    const tabs = root.querySelector<HTMLElement>('.settings-tabs');
    const panel = root.querySelector<HTMLElement>('.settings-panel');
    if (!tabs || !panel) {
        root.remove();
        return;
    }

    const storagePanel = createStoragePanel();
    const panels: Record<SettingsTab, SettingsPanel> = {
        general: createGeneralPanel(currentUser),
        storage: storagePanel,
        apps: createAppsPanel(),
        users: createUsersPanel(currentUser),
        devices: createDevicesPanel(currentUser, destination === 'email-recipients'),
    };

    const availableTabs: SettingsTab[] = ['general'];
    if (currentUser.role === 'admin') availableTabs.push('storage');
    availableTabs.push('apps', 'users');
    // Admins can enable email delivery; other roles see it only when enabled.
    if (sendingSetting().value || currentUser.role === 'admin') availableTabs.push('devices');
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
        panels[activeTab].render(container);
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
        panels[activeTab].unmount?.();
        activeTab = tab;
        updateTabSelection(tabButtons, activeTab);
        renderPanel();
        if (tab === 'storage') refreshStorageSilently();
    });

    renderPanel();
    const initialTabButton = tabButtons.get(activeTab);
    modal.open(initialTabButton);
    initialTabButton?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
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
