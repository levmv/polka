import { fetchAdminStorageStatus, retryFailedWriteback } from '../api';
import { appVersion } from '../bootstrap';
import { createSelect } from '../components/select';
import { createToggle } from '../components/toggle';
import { textEl } from '../dom';
import { errorMessage } from '../errors';
import { suggestedTimeZones } from '../time-zone';
import { showToast } from '../toast';
import type { AdminStorageStatus, CurrentUser, ThemePreference } from '../types';
import { loadPersonalSettings, type PersonalSettings, writebackSetting } from './state';
import {
    type AsyncLoadState,
    errorNote,
    inlineSettingsButton,
    loadingNote,
    renderAsyncSection,
    type SettingsPanel,
    settingsRow,
} from './ui';

type GeneralState = AsyncLoadState & {
    settings: PersonalSettings | null;
    status: AdminStorageStatus | null;
    storageLoading: boolean;
    storageError: string;
    writebackHost: HTMLElement | null;
    cleanup: (() => void)[];
    destroyWriteback: (() => void) | null;
};

export function createGeneralPanel(currentUser: CurrentUser): SettingsPanel {
    const state: GeneralState = {
        loaded: false,
        loading: false,
        settings: null,
        status: null,
        storageLoading: false,
        storageError: '',
        writebackHost: null,
        cleanup: [],
        destroyWriteback: null,
        loadError: '',
    };
    return {
        render: (root) => renderGeneralPanel(root, currentUser, state),
        unmount: () => destroyGeneralControls(state),
    };
}

function destroyGeneralControls(state: GeneralState): void {
    for (const cleanup of state.cleanup.splice(0)) cleanup();
    state.destroyWriteback?.();
    state.destroyWriteback = null;
    state.writebackHost = null;
}

function renderGeneralPanel(
    root: HTMLElement,
    currentUser: CurrentUser,
    state: GeneralState,
): void {
    if (!root.isConnected) return;
    destroyGeneralControls(state);
    root.replaceChildren();
    root.append(textEl('h3', 'settings-section-title', 'General'));

    if (
        renderAsyncSection(state, {
            target: root,
            load: async () => {
                state.settings = await loadPersonalSettings();
            },
            rerender: () => renderGeneralPanel(root, currentUser, state),
            errorFallback: 'Failed to load settings',
            isReady: () => state.settings !== null,
        })
    ) {
        return;
    }

    if (!state.settings) return;
    const settings = state.settings;

    const themeSelect = createSelect({
        ariaLabel: 'Theme',
        value: settings.theme.value,
        options: [
            { value: 'system', label: 'System' },
            { value: 'light', label: 'Light' },
            { value: 'dark', label: 'Dark' },
            { value: 'sepia', label: 'Sepia' },
        ],
        onChange: (value) => settings.theme.set(value as ThemePreference),
    });
    state.cleanup.push(themeSelect.destroy, settings.theme.subscribe(themeSelect.setValue));

    const continueToggle = createToggle({
        ariaLabel: 'Show Continue reading rail',
        checked: settings.show_continue_reading.value,
        onChange: settings.show_continue_reading.set,
    });
    state.cleanup.push(settings.show_continue_reading.subscribe(continueToggle.setChecked));

    const rows = document.createElement('div');
    rows.className = 'settings-rows';
    const timeZoneField = document.createElement('div');
    const timeZoneInput = document.createElement('input');
    timeZoneInput.type = 'text';
    timeZoneInput.className = 'dialog-input';
    timeZoneInput.setAttribute('aria-label', 'Time zone');
    timeZoneInput.setAttribute('list', 'settings-time-zones');
    timeZoneInput.autocomplete = 'off';
    timeZoneInput.spellcheck = false;
    timeZoneInput.value = settings.time_zone.value;
    timeZoneInput.placeholder = 'Europe/Berlin';
    const timeZones = document.createElement('datalist');
    timeZones.id = 'settings-time-zones';
    for (const zone of suggestedTimeZones(settings.time_zone.value)) {
        const option = document.createElement('option');
        option.value = zone;
        timeZones.append(option);
    }
    timeZoneInput.addEventListener('change', () => {
        const next = timeZoneInput.value.trim();
        timeZoneInput.value = next;
        settings.time_zone.set(next);
    });
    state.cleanup.push(
        settings.time_zone.subscribe((value, previous) => {
            // A save response must not overwrite text that is still being edited.
            if (timeZoneInput.value === previous) timeZoneInput.value = value;
        }),
    );
    timeZoneField.append(timeZoneInput, timeZones);
    rows.append(
        settingsRow('Theme', 'How polka looks. System follows your device.', themeSelect.el),
        settingsRow(
            'Time zone',
            'New reading sessions use this time zone. Past reading days stay unchanged.',
            timeZoneField,
        ),
        settingsRow(
            'Continue reading',
            'Show the Continue reading rail at the top of the library.',
            continueToggle.el,
        ),
    );

    if (currentUser.role === 'admin') {
        appendWritebackRow(rows, state);
    }
    root.append(rows);
    const currentVersion = appVersion();
    if (currentVersion) {
        const versionLine = textEl('div', 'dialog-note', 'Polka version: ');
        const versionLink = document.createElement('a');
        versionLink.className = 'settings-version-link';
        versionLink.href = 'https://github.com/levmv/polka';
        versionLink.target = '_blank';
        versionLink.rel = 'noreferrer';
        versionLink.textContent = currentVersion;
        versionLine.append(versionLink);
        root.append(versionLine);
    }
}

function appendWritebackRow(rows: HTMLElement, state: GeneralState): void {
    const control = document.createElement('div');
    state.writebackHost = control;
    const rerender = () => {
        // Tab changes can replace the host during a request. Update the latest
        // host without rebuilding the personal controls and losing their edits.
        if (!state.writebackHost) return;
        state.destroyWriteback?.();
        state.destroyWriteback = null;
        state.writebackHost.replaceChildren(
            state.status
                ? writebackControl(state, rerender)
                : state.storageError
                  ? errorNote(state.storageError)
                  : loadingNote(),
        );
    };
    if (!state.status && !state.storageLoading && !state.storageError) {
        state.storageLoading = true;
        fetchAdminStorageStatus()
            .then((status) => {
                state.status = status;
            })
            .catch((err) => {
                state.storageError = errorMessage(err, 'Failed to load library settings');
            })
            .finally(() => {
                state.storageLoading = false;
                rerender();
            });
    }

    rerender();

    rows.append(
        settingsRow(
            'Metadata write-back',
            'Editing metadata rewrites the book file; devices that sync files will see it change.',
            control,
        ),
    );
}

function writebackControl(state: GeneralState, rerender: () => void): HTMLElement {
    const wrap = document.createElement('div');
    wrap.className = 'settings-writeback-control';
    const wb = state.status?.writeback;
    if (!wb) return wrap;
    const setting = writebackSetting(wb.mode);

    const select = createSelect({
        ariaLabel: 'Metadata write-back mode',
        value: setting.value,
        options: [
            { value: 'manual', label: 'Manual' },
            { value: 'auto', label: 'Auto' },
            { value: 'off', label: 'Off' },
        ],
        onChange: (mode) => setting.set(mode as AdminStorageStatus['writeback']['mode']),
    });

    const unsubscribe = setting.subscribe(select.setValue);
    state.destroyWriteback = () => {
        unsubscribe();
        select.destroy();
    };
    wrap.append(select.el, writebackCountsLine(state, rerender));
    return wrap;
}

function writebackCountsLine(state: GeneralState, rerender: () => void): HTMLElement {
    const wb = state.status?.writeback;
    const line = document.createElement('div');
    line.className = 'dialog-note';
    if (!wb || (wb.pending === 0 && wb.failed === 0)) {
        line.textContent = 'All book files carry the current metadata.';
        return line;
    }
    const parts = [`${wb.pending} ${wb.pending === 1 ? 'file' : 'files'} pending metadata write`];
    if (wb.failed > 0) {
        parts.push(`${wb.failed} failed`);
        line.classList.add('dialog-note-error');
    }
    line.append(document.createTextNode(parts.join(' · ')));
    if (wb.failed > 0) {
        line.append(
            document.createTextNode(' · '),
            inlineSettingsButton('Retry failed', async () => {
                try {
                    const result = await retryFailedWriteback();
                    state.status = result.storage;
                    showToast(
                        result.queued === 0
                            ? 'No failed metadata writes'
                            : `Retrying ${result.queued} failed ${result.queued === 1 ? 'file' : 'files'}`,
                    );
                    rerender();
                } catch (err) {
                    showToast(errorMessage(err, 'Retry metadata write-back failed'), {
                        type: 'error',
                    });
                }
            }),
        );
    }
    return line;
}
