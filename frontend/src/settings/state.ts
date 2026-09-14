import {
    fetchUserSettings,
    saveAdminStorageStatus,
    saveSendEnabled,
    saveUserSettings,
} from '../api';
import { type AutosavedValue, createAutosavedValue } from '../autosave';
import { initialSendEnabled } from '../bootstrap';
import { errorMessage } from '../errors';
import { applyTheme } from '../theme';
import { showToast } from '../toast';
import type { UserSettings, WritebackStatus } from '../types';

type PersonalSetting = 'theme' | 'time_zone' | 'show_continue_reading';
export type PersonalSettings = {
    [K in PersonalSetting]: AutosavedValue<UserSettings[K]>;
};

// These values belong to the page, so closing Settings does not interrupt a
// save or give a reopened control a second, competing copy of its state.
let personalSettings: Promise<PersonalSettings> | null = null;
let sending: AutosavedValue<boolean> | null = null;
let writeback: AutosavedValue<WritebackStatus['mode']> | null = null;

export function loadPersonalSettings(): Promise<PersonalSettings> {
    if (!personalSettings) {
        personalSettings = fetchUserSettings()
            .then((initial) => {
                const settings: PersonalSettings = {
                    theme: personalValue(initial, 'theme'),
                    time_zone: personalValue(initial, 'time_zone'),
                    show_continue_reading: personalValue(initial, 'show_continue_reading'),
                };
                applyTheme(settings.theme.value);
                settings.theme.subscribe(applyTheme);
                return settings;
            })
            .catch((error) => {
                personalSettings = null;
                throw error;
            });
    }
    return personalSettings;
}

function personalValue<K extends PersonalSetting>(
    initial: UserSettings,
    key: K,
): AutosavedValue<UserSettings[K]> {
    return setting(
        initial[key],
        async (value, signal) => (await saveUserSettings({ [key]: value }, signal))[key],
        'Failed to save settings',
    );
}

export function sendingSetting(): AutosavedValue<boolean> {
    sending ??= setting(initialSendEnabled(), saveSendEnabled, 'Failed to save sending setting');
    return sending;
}

export function writebackSetting(
    initial: WritebackStatus['mode'],
): AutosavedValue<WritebackStatus['mode']> {
    writeback ??= setting(
        initial,
        async (mode, signal) =>
            (await saveAdminStorageStatus({ writeback: { mode } }, signal)).writeback.mode,
        'Failed to save write-back mode',
    );
    return writeback;
}

function setting<T>(
    initial: T,
    save: (value: T, signal: AbortSignal) => Promise<T>,
    fallback: string,
): AutosavedValue<T> {
    return createAutosavedValue(initial, save, (error) => {
        showToast(errorMessage(error, fallback), { type: 'error' });
    });
}
