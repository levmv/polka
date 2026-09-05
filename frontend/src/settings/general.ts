import {
    fetchAdminStorageStatus,
    fetchUserSettings,
    retryFailedWriteback,
    saveAdminStorageStatus,
    saveUserSettings,
} from '../api';
import { appVersion } from '../bootstrap';
import { createSelect } from '../components/select';
import { createToggle } from '../components/toggle';
import { textEl } from '../dom';
import { errorMessage } from '../errors';
import { applyTheme } from '../theme';
import { suggestedTimeZones } from '../time-zone';
import { showToast } from '../toast';
import type { AdminStorageStatus, CurrentUser, ThemePreference, UserSettings } from '../types';
import {
    type AsyncLoadState,
    errorNote,
    inlineSettingsButton,
    loadingNote,
    renderAsyncSection,
    settingsRow,
} from './ui';

type GeneralState = AsyncLoadState & {
    settings: UserSettings | null;
    status: AdminStorageStatus | null;
    storageLoading: boolean;
    storageError: string;
    writebackHost: HTMLElement | null;
};

export function createGeneralPanel(currentUser: CurrentUser): (root: HTMLElement) => void {
    const state: GeneralState = {
        loaded: false,
        loading: false,
        settings: null,
        status: null,
        storageLoading: false,
        storageError: '',
        writebackHost: null,
        loadError: '',
    };
    return (root) => renderGeneralPanel(root, currentUser, state);
}

// The General panel autosaves: personal controls persist through PUT
// /api/settings; admin-only catalog-level rows save through their owning admin
// APIs. Success is its own feedback; only failures surface, as a toast or local
// settings row.
function renderGeneralPanel(
    root: HTMLElement,
    currentUser: CurrentUser,
    state: GeneralState,
): void {
    root.replaceChildren();
    root.append(textEl('h3', 'settings-section-title', 'General'));

    if (
        renderAsyncSection(state, {
            target: root,
            load: async () => {
                state.settings = await fetchUserSettings();
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

    // Persist a partial change immediately. On failure, toast and revert the
    // control to its previous on-screen value.
    const persist = async (patch: Partial<UserSettings>, revert: () => void) => {
        try {
            const saved = await saveUserSettings(patch);
            state.settings = saved;
            applyTheme(saved.theme);
            window.dispatchEvent(
                new CustomEvent<UserSettings>('polka:user-settings', { detail: saved }),
            );
        } catch (err) {
            showToast(errorMessage(err, 'Failed to save'), { type: 'error' });
            revert();
        }
    };

    const themeSelect = createSelect({
        ariaLabel: 'Theme',
        value: settings.theme,
        options: [
            { value: 'system', label: 'System' },
            { value: 'light', label: 'Light' },
            { value: 'dark', label: 'Dark' },
            { value: 'sepia', label: 'Sepia' },
        ],
        onChange: (value) => {
            const previous = state.settings?.theme ?? settings.theme;
            applyTheme(value as ThemePreference);
            void persist({ theme: value as ThemePreference }, () => {
                themeSelect.setValue(previous);
                applyTheme(previous);
            });
        },
    });

    const continueToggle = createToggle({
        ariaLabel: 'Show Continue reading rail',
        checked: !settings.hide_continue_reading,
        onChange: (checked) => {
            void persist({ hide_continue_reading: !checked }, () =>
                continueToggle.setChecked(!checked),
            );
        },
    });

    const rows = document.createElement('div');
    rows.className = 'settings-rows';
    const timeZoneField = document.createElement('div');
    const timeZoneInput = document.createElement('input');
    timeZoneInput.type = 'text';
    timeZoneInput.className = 'settings-input';
    timeZoneInput.setAttribute('aria-label', 'Time zone');
    timeZoneInput.setAttribute('list', 'settings-time-zones');
    timeZoneInput.autocomplete = 'off';
    timeZoneInput.spellcheck = false;
    timeZoneInput.value = settings.time_zone;
    timeZoneInput.placeholder = 'Europe/Berlin';
    const timeZones = document.createElement('datalist');
    timeZones.id = 'settings-time-zones';
    for (const zone of suggestedTimeZones(settings.time_zone)) {
        const option = document.createElement('option');
        option.value = zone;
        timeZones.append(option);
    }
    timeZoneInput.addEventListener('change', () => {
        const previous = state.settings?.time_zone ?? settings.time_zone;
        const next = timeZoneInput.value.trim();
        timeZoneInput.value = next;
        if (next !== previous) {
            void persist({ time_zone: next }, () => {
                timeZoneInput.value = previous;
            });
        }
    });
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
        const versionLine = textEl('div', 'settings-note', 'Polka version: ');
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
        state.writebackHost?.replaceChildren(
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

    const select = createSelect({
        ariaLabel: 'Metadata write-back mode',
        value: wb?.mode ?? 'manual',
        options: [
            { value: 'manual', label: 'Manual' },
            { value: 'auto', label: 'Auto' },
            { value: 'off', label: 'Off' },
        ],
        onChange: (mode) =>
            void saveWritebackMode(
                state,
                mode as AdminStorageStatus['writeback']['mode'],
                rerender,
            ),
    });

    wrap.append(select.el, writebackCountsLine(state, rerender));
    return wrap;
}

function writebackCountsLine(state: GeneralState, rerender: () => void): HTMLElement {
    const wb = state.status?.writeback;
    const line = document.createElement('div');
    line.className = 'settings-note settings-storage-note';
    if (!wb || (wb.pending === 0 && wb.failed === 0)) {
        line.textContent = 'All book files carry the current metadata.';
        return line;
    }
    const parts = [`${wb.pending} ${wb.pending === 1 ? 'file' : 'files'} pending metadata write`];
    if (wb.failed > 0) {
        parts.push(`${wb.failed} failed`);
        line.classList.add('settings-note-error');
    }
    line.append(document.createTextNode(parts.join(' · ')));
    if (wb.failed > 0) {
        line.append(
            document.createTextNode(' · '),
            inlineSettingsButton('Retry failed', async () => {
                try {
                    const result = await retryFailedWriteback();
                    state.status = result.storage;
                    window.dispatchEvent(
                        new CustomEvent<AdminStorageStatus>('polka:admin-storage', {
                            detail: result.storage,
                        }),
                    );
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

async function saveWritebackMode(
    state: GeneralState,
    mode: AdminStorageStatus['writeback']['mode'],
    rerender: () => void,
): Promise<void> {
    try {
        state.status = await saveAdminStorageStatus({ writeback: { mode } });
        window.dispatchEvent(
            new CustomEvent<AdminStorageStatus>('polka:admin-storage', { detail: state.status }),
        );
    } catch (err) {
        showToast(errorMessage(err, 'Failed to save write-back mode'), { type: 'error' });
    }
    // Re-render either way: on success to refresh the counts, on failure to snap
    // the select back to the stored mode.
    rerender();
}
