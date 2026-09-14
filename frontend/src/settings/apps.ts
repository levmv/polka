import {
    changeKoboShelf,
    createAppToken,
    createKoboConnection,
    fetchAppTokens,
    fetchKoboConnection,
    fetchShelves,
    revokeAppToken,
    revokeKoboConnection,
} from '../api';
import { formField, textEl } from '../dom';
import { errorMessage } from '../errors';
import { confirmModal } from '../modal';
import { showToast } from '../toast';
import type { AppToken, KoboConnection, Shelf } from '../types';
import {
    type AsyncLoadState,
    buttonEl,
    createReadonlyCopyField,
    fieldGroup,
    makeInput,
    openFormModal,
    openInfoModal,
    renderAsyncSection,
    type SettingsPanel,
    settingsItemRow,
} from './ui';

type AppsState = AsyncLoadState & {
    tokens: AppToken[];
};

type KoboState = AsyncLoadState & {
    shelves: Shelf[];
    koboConnection: KoboConnection | null;
};

// App passwords identify their account without the Basic-auth username. Keep
// this value fixed because Basic authentication cannot encode ':' in a user ID.
const appPasswordBasicUsername = 'polka';

// Load connection sections independently so one failed request does not hide
// the other connection settings.
export function createAppsPanel(): SettingsPanel {
    const state: AppsState = {
        loaded: false,
        loading: false,
        tokens: [],
        loadError: '',
    };
    const koboState: KoboState = {
        loaded: false,
        loading: false,
        shelves: [],
        koboConnection: null,
        loadError: '',
    };
    return { render: (root) => renderAppsPanel(root, state, koboState) };
}

function renderAppsPanel(root: HTMLElement, state: AppsState, koboState: KoboState): void {
    root.replaceChildren();

    root.append(
        textEl('h3', 'settings-section-title', 'Reading apps'),
        textEl(
            'p',
            'settings-section-intro',
            'Connect reading apps without using your account password.',
        ),
    );

    const kobo = document.createElement('section');
    root.append(kobo);
    renderKoboConnection(kobo, koboState);

    const passwords = document.createElement('section');
    passwords.className = 'settings-app-passwords settings-block';
    root.append(passwords);
    renderAppPasswords(passwords, state);
    root.append(createReadingAppConnections());
}

function renderAppPasswords(root: HTMLElement, state: AppsState): void {
    root.replaceChildren();

    const rerender = () => renderAppPasswords(root, state);

    const header = document.createElement('div');
    header.className = 'settings-subsection-header';
    const intro = document.createElement('div');
    intro.append(
        textEl('h4', 'settings-subsection-title', 'App passwords'),
        textEl(
            'div',
            'settings-block-hint',
            'Create one per app or device. Revoke it later without changing your account password.',
        ),
    );
    const create = buttonEl('dialog-btn', 'New app password', () =>
        openCreateAppPasswordModal(state, rerender),
    );
    create.disabled = !state.loaded;
    header.append(intro, create);
    root.append(header);

    const list = document.createElement('div');
    list.className = 'settings-item-list';
    root.appendChild(list);

    if (
        renderAsyncSection(state, {
            target: list,
            load: async () => {
                state.tokens = await fetchAppTokens();
            },
            rerender,
            errorFallback: 'Failed to load app passwords',
        })
    ) {
        return;
    }

    if (state.tokens.length === 0) {
        list.appendChild(
            textEl(
                'div',
                'settings-item-empty',
                'No app passwords yet — create one to connect your first reader.',
            ),
        );
        return;
    }

    for (const token of state.tokens) {
        list.appendChild(createTokenRow(token, state, rerender));
    }
}

function renderKoboConnection(root: HTMLElement, state: KoboState): void {
    root.replaceChildren();
    root.className = 'settings-kobo-connection';
    const rerender = () => renderKoboConnection(root, state);

    const intro = document.createElement('div');
    intro.append(
        textEl('h4', 'settings-subsection-title', 'Kobo sync'),
        textEl(
            'div',
            'settings-block-hint',
            'Put one shelf in your Kobo library. Books are added, updated, and removed on the next device sync.',
        ),
    );
    const header = document.createElement('div');
    header.append(intro);
    root.append(header);

    if (
        renderAsyncSection(state, {
            target: root,
            load: async () => {
                const [shelves, koboConnection] = await Promise.all([
                    fetchShelves(),
                    fetchKoboConnection(),
                ]);
                state.shelves = shelves;
                state.koboConnection = koboConnection;
            },
            rerender,
            errorFallback: 'Failed to load Kobo settings',
        })
    ) {
        return;
    }

    if (state.koboConnection) {
        const connection = state.koboConnection;
        const details = buttonEl('dialog-btn', 'Details', () =>
            openKoboConnectionDetails(connection.setup_url),
        );
        const changeShelf = buttonEl('dialog-btn', 'Change shelf', () =>
            openKoboSetupModal(state, rerender),
        );
        changeShelf.disabled = state.shelves.length === 0;
        const actions = [
            changeShelf,
            buttonEl('dialog-btn dialog-danger-btn', 'Revoke', async () => {
                const confirmed = await confirmModal({
                    title: 'Revoke Kobo connection',
                    body: 'Kobo library sync will stop immediately. Books already downloaded to the device stay there.',
                    confirmLabel: 'Revoke',
                    danger: true,
                });
                if (!confirmed) return;
                try {
                    await revokeKoboConnection();
                    state.koboConnection = null;
                    rerender();
                    showToast('Kobo connection revoked');
                } catch (err) {
                    showToast(errorMessage(err, 'Revoke Kobo connection failed'), {
                        type: 'error',
                    });
                }
            }),
        ];
        root.append(
            settingsItemRow({
                name: connection.shelf_name || 'No shelf selected',
                meta: connection.shelf_id
                    ? `Connected ${formatTokenDate(connection.created_at)} · Last used ${formatTokenDate(connection.last_used_at)}`
                    : 'The previous shelf was deleted. Its books will be removed from Kobo on the next sync.',
                primaryAction: details,
                actions,
                rowClass: 'settings-kobo-row',
            }),
        );
        if (state.shelves.length === 0) {
            root.append(
                textEl(
                    'div',
                    'settings-item-empty',
                    'Create a shelf to choose a new one for Kobo.',
                ),
            );
        }
        return;
    }

    const setup = buttonEl('dialog-btn', 'Set up Kobo', () => openKoboSetupModal(state, rerender));
    setup.disabled = state.shelves.length === 0;
    header.className = 'settings-subsection-header';
    header.append(setup);
    if (state.shelves.length === 0) {
        root.append(
            textEl('div', 'settings-item-empty', 'Create a shelf first, then return here.'),
        );
    }
}

function openKoboSetupModal(state: KoboState, rerender: () => void): void {
    if (state.shelves.length === 0) return;
    const fields = fieldGroup();
    const shelf = document.createElement('select');
    shelf.className = 'dialog-input';
    shelf.setAttribute('aria-label', 'Shelf');
    for (const item of state.shelves) {
        const option = document.createElement('option');
        option.value = String(item.id);
        option.textContent = item.kind === 'query' ? `${item.name} · smart shelf` : item.name;
        shelf.append(option);
    }
    const connection = state.koboConnection;
    if (connection?.shelf_id && state.shelves.some((item) => item.id === connection.shelf_id)) {
        shelf.value = String(connection.shelf_id);
    }
    fields.append(
        textEl(
            'div',
            'dialog-help',
            connection
                ? 'Books outside the new shelf will be removed from Kobo on its next sync. The setup URL stays the same.'
                : 'Choose the shelf that should appear on this Kobo.',
        ),
        formField('Shelf', shelf),
    );

    openFormModal({
        title: connection ? 'Change Kobo shelf' : 'Set up Kobo',
        submitLabel: connection ? 'Save' : 'Create',
        fields,
        focus: shelf,
        onSubmit: async () => {
            try {
                const saved = connection
                    ? await changeKoboShelf(Number(shelf.value))
                    : await createKoboConnection(Number(shelf.value));
                state.koboConnection = saved;
                rerender();
                if (connection) showToast('Kobo shelf changed');
                else openKoboConnectionDetails(saved.setup_url);
                return true;
            } catch (err) {
                showToast(errorMessage(err, 'Save Kobo connection failed'), { type: 'error' });
                return false;
            }
        },
    });
}

function openKoboConnectionDetails(setupURL: string): void {
    const body = document.createElement('div');
    body.className = 'dialog-fields';
    body.append(
        createReadonlyCopyField('Kobo setup URL', setupURL, {
            copyLabel: 'Copy Kobo setup URL',
        }),
        textEl(
            'div',
            'dialog-help',
            'On the mounted Kobo, open .kobo/Kobo/Kobo eReader.conf and set api_endpoint to this URL under [OneStoreServices], then safely eject and sync.',
        ),
    );
    openInfoModal(
        'Connect Kobo',
        body,
        body.querySelector<HTMLButtonElement>('button') || undefined,
    );
}

function openCreateAppPasswordModal(state: AppsState, rerender: () => void): void {
    const fields = fieldGroup();
    const name = makeInput('text', 'off');
    name.placeholder = 'e.g. KOReader on phone';
    fields.append(formField('Name', name));

    openFormModal({
        title: 'New app password',
        submitLabel: 'Create',
        fields,
        focus: name,
        onSubmit: async (setError) => {
            if (!name.value.trim()) {
                setError('Name is required');
                return false;
            }
            try {
                const created = await createAppToken(name.value.trim());
                state.tokens.unshift(created);
                rerender();
                openAppConnectionDetails(created.name, created.token);
                return true;
            } catch (err) {
                showToast(errorMessage(err, 'Create app password failed'), { type: 'error' });
                return false;
            }
        },
    });
}

function openAppConnectionDetails(name: string, token: string): void {
    const body = document.createElement('div');
    body.className = 'dialog-fields';

    const password = createReadonlyCopyField('App password', token, {
        copyLabel: 'Copy app password',
    });

    const catalog = document.createElement('section');
    catalog.className = 'settings-connect-method';
    catalog.append(
        textEl('h4', 'settings-connect-title', 'Browse and download'),
        createReadonlyCopyField('Catalog URL', opdsCatalogURL(), {
            inputClass: 'settings-opds-url',
            copyLabel: 'Copy OPDS catalog URL',
        }),
    );
    catalog.append(
        createReadonlyCopyField('Username', appPasswordBasicUsername, {
            copyLabel: 'Copy username',
        }),
        createReadonlyCopyField(
            'Complete URL (includes password)',
            opdsCatalogURLWithAppPassword(token),
            {
                inputClass: 'settings-opds-url',
                copyLabel: 'Copy complete OPDS URL',
            },
        ),
    );

    const progress = document.createElement('section');
    progress.className = 'settings-connect-method';
    progress.append(
        textEl('h4', 'settings-connect-title', 'Sync KOReader position'),
        textEl(
            'p',
            'settings-block-hint',
            "Set this as KOReader's custom progress sync server. The URL already includes the app password.",
        ),
        createReadonlyCopyField('Sync server URL', koSyncServerURL(token), {
            copyLabel: 'Copy KOReader sync server URL',
        }),
    );

    body.append(password, catalog, progress);

    openInfoModal(
        `Connect ${name}`,
        body,
        password.querySelector<HTMLButtonElement>('button') || undefined,
    );
}

function createTokenRow(token: AppToken, state: AppsState, rerender: () => void): HTMLElement {
    const details = buttonEl('dialog-btn', 'Details', () =>
        openAppConnectionDetails(token.name, token.token),
    );
    const revoke = buttonEl('dialog-btn dialog-danger-btn', 'Revoke', async () => {
        const confirmed = await confirmModal({
            title: 'Revoke app password',
            body: `"${token.name}" will stop working immediately.`,
            confirmLabel: 'Revoke',
            danger: true,
        });
        if (!confirmed) return;
        try {
            await revokeAppToken(token.id);
            state.tokens = state.tokens.filter((item) => item.id !== token.id);
            showToast(`Revoked ${token.name}`);
            rerender();
        } catch (err) {
            showToast(errorMessage(err, 'Revoke app password failed'), { type: 'error' });
        }
    });

    return settingsItemRow({
        name: token.name,
        meta: `Created ${formatTokenDate(token.created_at)} · Last used ${formatTokenDate(token.last_used_at)}`,
        primaryAction: details,
        actions: [revoke],
    });
}

function createReadingAppConnections(): HTMLElement {
    const wrap = document.createElement('section');
    wrap.className = 'settings-app-connections settings-block';

    const methods = document.createElement('div');
    methods.className = 'settings-app-methods';

    const catalog = document.createElement('section');
    catalog.className = 'settings-app-method settings-opds-setup';
    catalog.append(
        textEl('h5', 'settings-app-method-title', 'Browse and download'),
        textEl(
            'p',
            'settings-block-hint',
            'Use OPDS in KOReader, Moon+ Reader, PocketBook, or another compatible app.',
        ),
        createReadonlyCopyField('Catalog URL', opdsCatalogURL(), {
            inputClass: 'settings-opds-url',
            copyLabel: 'Copy OPDS catalog URL',
        }),
    );
    catalog.append(
        createReadonlyCopyField('Username', appPasswordBasicUsername, {
            copyLabel: 'Copy username',
        }),
    );

    const progress = document.createElement('section');
    progress.className = 'settings-app-method settings-kosync-setup';
    progress.append(
        textEl('h5', 'settings-app-method-title', 'Sync KOReader position'),
        textEl(
            'p',
            'settings-block-hint',
            'Open an app password’s connection details for a complete custom sync server URL, ready to copy.',
        ),
    );

    methods.append(catalog, progress);
    wrap.append(textEl('h4', 'settings-subsection-title', 'Connection details'), methods);
    return wrap;
}

function opdsCatalogURL(): string {
    return new URL('/opds', window.location.origin).toString();
}

function opdsCatalogURLWithAppPassword(password: string): string {
    const url = new URL('/opds', window.location.origin);
    url.username = appPasswordBasicUsername;
    url.password = password;
    return url.toString();
}

function koSyncServerURL(token: string): string {
    return new URL(`/kosync/${encodeURIComponent(token)}`, window.location.origin).toString();
}

function formatTokenDate(timestamp?: number): string {
    if (!timestamp || !Number.isFinite(timestamp)) return 'never';
    return new Date(timestamp * 1000).toLocaleDateString(undefined, {
        year: 'numeric',
        month: 'short',
        day: 'numeric',
    });
}
