import { fetchCurrentUser } from './api';
import { isPlainClick } from './dom';
import { openModal } from './modal';
import { registerOverlayReopen } from './navigation';
import { createAppsPanel } from './settings/apps';
import { createDevicesPanel } from './settings/devices';
import { createGeneralPanel } from './settings/general';
import { sendingSetting } from './settings/state';
import { createStoragePanel } from './settings/storage';
import type { SettingsPanel } from './settings/ui';
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

registerOverlayReopen(
    'settings',
    async (entry) => {
        const user = await fetchCurrentUser();
        return () => openSettingsModal(user, entry.target as SettingsDestination);
    },
    { urlParam: 'settings' },
);

export function openSettingsModal(
    currentUser: CurrentUser,
    destination: SettingsDestination = 'general',
): void {
    const initialTab = destination === 'email-recipients' ? 'devices' : destination;
    const availableTabs: SettingsTab[] = ['general'];
    if (currentUser.role === 'admin') availableTabs.push('storage');
    availableTabs.push('apps', 'users');
    // Admins can enable email delivery; other roles see it only when enabled.
    if (sendingSetting().value || currentUser.role === 'admin') availableTabs.push('devices');
    let activeTab: SettingsTab = availableTabs.includes(initialTab) ? initialTab : 'general';
    const { modal, root } = openModal({
        history: { kind: 'settings', target: activeTab },
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

    const tabLinks = renderTabs(tabs, availableTabs, activeTab, (tab) => {
        if (tab === activeTab) return;
        panels[activeTab].unmount?.();
        activeTab = tab;
        modal.updateHistory({ kind: 'settings', target: tab });
        updateTabSelection(tabLinks, activeTab);
        renderPanel();
        if (tab === 'storage') refreshStorageSilently();
    });

    renderPanel();
    const initialTabLink = tabLinks.get(activeTab);
    modal.open(initialTabLink);
    initialTabLink?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
}

function renderTabs(
    root: HTMLElement,
    tabs: SettingsTab[],
    activeTab: SettingsTab,
    onSelect: (tab: SettingsTab) => void,
): Map<SettingsTab, HTMLAnchorElement> {
    root.replaceChildren();
    const links = new Map<SettingsTab, HTMLAnchorElement>();
    for (const tab of tabs) {
        const link = document.createElement('a');
        const url = new URL(window.location.href);
        url.searchParams.set('settings', tab);
        link.href = url.href;
        link.className = 'settings-tab';
        link.textContent = TAB_LABELS[tab];
        link.setAttribute('role', 'tab');
        link.setAttribute('aria-selected', String(tab === activeTab));
        link.addEventListener('click', (event) => {
            if (!isPlainClick(event)) return;
            event.preventDefault();
            onSelect(tab);
        });
        root.appendChild(link);
        links.set(tab, link);
    }
    return links;
}

function updateTabSelection(
    links: Map<SettingsTab, HTMLAnchorElement>,
    activeTab: SettingsTab,
): void {
    for (const [tab, link] of links) {
        link.setAttribute('aria-selected', String(tab === activeTab));
    }
}
