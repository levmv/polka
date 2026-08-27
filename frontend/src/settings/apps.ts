import {
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
import type { AppToken, CurrentUser, KoboConnection, Shelf } from '../types';
import {
    type AsyncLoadState,
    buttonEl,
    createReadonlyCopyField,
    fieldGroup,
    makeInput,
    openFormModal,
    openInfoModal,
    renderAsyncSection,
    settingsItemRow,
} from './ui';

type AppsState = AsyncLoadState & {
    tokens: AppToken[];
};

type KoboState = AsyncLoadState & {
    shelves: Shelf[];
    koboConnection: KoboConnection | null;
};

export function createAppsPanel(currentUser: CurrentUser): (root: HTMLElement) => void {
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
    return (root) => renderAppsPanel(root, currentUser, state, koboState);
}

function renderAppsPanel(
    root: HTMLElement,
    currentUser: CurrentUser,
    state: AppsState,
    koboState: KoboState,
): void {
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
    renderAppPasswords(passwords, currentUser, state);
    root.append(createReadingAppConnections(currentUser));
}

function renderAppPasswords(root: HTMLElement, currentUser: CurrentUser, state: AppsState): void {
    root.replaceChildren();

    const rerender = () => renderAppPasswords(root, currentUser, state);

    const action = document.createElement('div');
    action.className = 'settings-section-action';
    action.append(
        buttonEl('settings-btn settings-primary-btn', 'New app password', () =>
            openCreateAppPasswordModal(currentUser, state, rerender),
        ),
    );

    root.append(
        textEl('h4', 'settings-subsection-title', 'App passwords'),
        textEl(
            'div',
            'settings-block-hint',
            'Create one per app or device. Revoke it later without changing your account password.',
        ),
        action,
    );

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

    const title = document.createElement('div');
    title.className = 'settings-kobo-title';
    title.append(
        textEl('h4', 'settings-subsection-title', 'Kobo sync'),
        textEl('span', 'settings-experimental-badge', 'Experimental'),
    );
    root.append(
        title,
        textEl(
            'div',
            'settings-block-hint',
            'Put one shelf in your Kobo library. Books are added, updated, and removed on the next device sync.',
        ),
    );

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
        const actions = [
            buttonEl('settings-btn', 'Replace…', () => openKoboSetupModal(state, rerender)),
            buttonEl('settings-btn settings-danger-btn', 'Revoke', async () => {
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
                name: state.koboConnection.shelf_name,
                meta: `Connected ${formatTokenDate(state.koboConnection.created_at)} · Last used ${formatTokenDate(state.koboConnection.last_used_at)}`,
                actions,
                rowClass: 'settings-kobo-row',
                actionsClass: 'settings-kobo-actions',
            }),
        );
        return;
    }

    const action = document.createElement('div');
    action.className = 'settings-section-action';
    const setup = buttonEl('settings-btn settings-primary-btn', 'Set up Kobo', () =>
        openKoboSetupModal(state, rerender),
    );
    setup.disabled = state.shelves.length === 0;
    action.append(setup);
    root.append(action);
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
    shelf.className = 'settings-input';
    shelf.setAttribute('aria-label', 'Shelf');
    for (const item of state.shelves) {
        const option = document.createElement('option');
        option.value = item.id;
        option.textContent = item.kind === 'query' ? `${item.name} · smart shelf` : item.name;
        shelf.append(option);
    }
    if (state.koboConnection) shelf.value = state.koboConnection.shelf_id;
    fields.append(
        textEl(
            'div',
            'settings-submodal-hint',
            state.koboConnection
                ? 'Creating a new URL revokes the current Kobo connection.'
                : 'Choose the shelf that should appear on this Kobo.',
        ),
        formField('Shelf', shelf),
    );

    openFormModal({
        title: state.koboConnection ? 'Replace Kobo connection' : 'Set up Kobo',
        submitLabel: state.koboConnection ? 'Replace' : 'Create',
        fields,
        focus: shelf,
        onSubmit: async () => {
            try {
                const created = await createKoboConnection(shelf.value);
                state.koboConnection = created;
                rerender();
                openKoboSecretModal(created.setup_url);
                return true;
            } catch (err) {
                showToast(errorMessage(err, 'Create Kobo connection failed'), { type: 'error' });
                return false;
            }
        },
    });
}

function openKoboSecretModal(setupURL: string): void {
    const body = document.createElement('div');
    body.className = 'settings-submodal-fields';
    body.append(
        textEl(
            'div',
            'settings-submodal-hint',
            "Finish setup now — this private URL isn't shown again. Hardware compatibility is still experimental.",
        ),
        createReadonlyCopyField('Kobo setup URL', setupURL, {
            inputClass: 'settings-kobo-url',
            copyLabel: 'Copy Kobo setup URL',
        }),
        textEl(
            'div',
            'settings-submodal-hint',
            'On the mounted Kobo, open .kobo/Kobo/Kobo eReader.conf and set api_endpoint to this URL under [OneStoreServices], then safely eject and sync.',
        ),
    );
    openInfoModal(
        'Connect Kobo',
        body,
        body.querySelector<HTMLButtonElement>('button') || undefined,
    );
}

function openCreateAppPasswordModal(
    currentUser: CurrentUser,
    state: AppsState,
    rerender: () => void,
): void {
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
                try {
                    state.tokens = await fetchAppTokens();
                } catch {
                    // The token exists; the list just failed to refresh. The
                    // secret below still lets the user finish setup.
                }
                state.loaded = true;
                rerender();
                openSecretModal(currentUser, created.name, created.token);
                return true;
            } catch (err) {
                showToast(errorMessage(err, 'Create app password failed'), { type: 'error' });
                return false;
            }
        },
    });
}

// Shown right after creation — polka stores only a hash, so this is the one
// place where the password and the complete credential-bearing KOSync URL can
// be copied. Stable OPDS details are repeated here to make setup one contained
// flow instead of sending the user back through the settings panel.
function openSecretModal(currentUser: CurrentUser, name: string, token: string): void {
    const body = document.createElement('div');
    body.className = 'settings-submodal-fields';

    const hint = textEl(
        'div',
        'settings-submodal-hint',
        "Finish setup now — polka keeps only a secure hash, so this password and sync URL aren't shown again.",
    );
    const password = createReadonlyCopyField('App password', token, {
        copyLabel: 'Copy app password',
    });

    const catalog = document.createElement('section');
    catalog.className = 'settings-connect-method';
    catalog.append(
        textEl('h4', 'settings-connect-title', 'Browse and download'),
        textEl(
            'p',
            'settings-block-hint',
            'Add the OPDS catalog in your reading app, then sign in with this username and app password.',
        ),
        createReadonlyCopyField('Catalog URL', opdsCatalogURL(), {
            inputClass: 'settings-opds-url',
            copyLabel: 'Copy OPDS catalog URL',
        }),
    );
    catalog.append(
        createReadonlyCopyField('Username', currentUser.username, {
            copyLabel: 'Copy username',
        }),
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
            inputClass: 'settings-kosync-url',
            copyLabel: 'Copy KOReader sync server URL',
        }),
    );

    body.append(hint, password, catalog, progress);

    openInfoModal(
        `Connect ${name}`,
        body,
        password.querySelector<HTMLButtonElement>('button') || undefined,
    );
}

function createTokenRow(token: AppToken, state: AppsState, rerender: () => void): HTMLElement {
    const revoke = buttonEl('settings-btn settings-danger-btn', 'Revoke', async () => {
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
        actions: [revoke],
    });
}

function createReadingAppConnections(currentUser: CurrentUser): HTMLElement {
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
        createReadonlyCopyField('Username', currentUser.username, {
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
            'When you create an app password, the setup screen gives you a complete custom sync server URL, ready to copy.',
        ),
    );

    methods.append(catalog, progress);
    wrap.append(textEl('h4', 'settings-subsection-title', 'Connection details'), methods);
    return wrap;
}

function opdsCatalogURL(): string {
    return new URL('/opds', window.location.origin).toString();
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
