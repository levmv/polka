import {
    createUser,
    deleteUser,
    fetchShelves,
    fetchUsers,
    updateUserAccess,
    updateUserPassword,
} from '../api';
import { createSelect } from '../components/select';
import { formField, textEl } from '../dom';
import { errorMessage } from '../errors';
import { icon } from '../icons';
import { confirmModal } from '../modal';
import { showToast } from '../toast';
import type { CurrentUser, Shelf, UserAccount } from '../types';
import {
    type AsyncLoadState,
    buttonEl,
    fieldGroup,
    makeInput,
    openFormModal,
    renderAsyncSection,
} from './ui';

type UsersState = AsyncLoadState & {
    users: UserAccount[];
};

export function createUsersPanel(currentUser: CurrentUser): (root: HTMLElement) => void {
    const state: UsersState = {
        loaded: false,
        loading: false,
        users: [],
        loadError: '',
    };
    return (root) => renderUsersPanel(root, currentUser, state);
}

// The Users panel is a flat list of the people who can sign in. Members see a
// single row (themselves); admins fetch and manage everyone. Every mutation —
// changing a password, adding a user, removing one — happens in a small modal,
// and its outcome surfaces as a toast.
function renderUsersPanel(root: HTMLElement, currentUser: CurrentUser, state: UsersState): void {
    root.replaceChildren();

    const isAdmin = currentUser.role === 'admin';
    const rerender = () => renderUsersPanel(root, currentUser, state);

    root.append(textEl('h3', 'settings-section-title', 'Users'));

    if (isAdmin) {
        const action = document.createElement('div');
        action.className = 'settings-section-action';
        action.append(
            buttonEl('settings-btn settings-primary-btn', 'Add user', () =>
                openAddUserModal(state, rerender),
            ),
        );
        root.append(action);
    }

    const list = document.createElement('div');
    list.className = 'settings-user-list';
    root.append(list);

    const renderRows = (users: UserAccount[]) => {
        for (const user of users) {
            list.appendChild(createPersonRow(user, currentUser, users, state, rerender));
        }
    };

    if (!isAdmin) {
        renderRows([
            {
                id: currentUser.id,
                username: currentUser.username,
                role: currentUser.role,
                content_scope: currentUser.content_scope,
            },
        ]);
        return;
    }

    if (
        renderAsyncSection(state, {
            target: list,
            load: async () => {
                state.users = await fetchUsers();
            },
            rerender,
            errorFallback: 'Failed to load users',
        })
    ) {
        return;
    }

    renderRows(state.users);
}

function createPersonRow(
    user: UserAccount,
    currentUser: CurrentUser,
    allUsers: UserAccount[],
    state: UsersState,
    rerender: () => void,
): HTMLElement {
    const row = document.createElement('div');
    row.className = 'settings-user-row';

    const isSelf = user.id === currentUser.id;

    const main = document.createElement('div');
    main.className = 'settings-user-main';
    main.append(
        textEl('div', 'settings-user-name', user.username),
        textEl(
            'div',
            'settings-user-meta',
            isSelf ? `You · ${accessSummary(user)}` : accessSummary(user),
        ),
    );

    const actions = document.createElement('div');
    actions.className = 'settings-user-actions';

    actions.append(
        buttonEl('settings-btn', isSelf ? 'Change password' : 'Reset password', () =>
            openPasswordModal(user, isSelf, rerender),
        ),
    );

    // Admins manage other people here; you can't remove yourself (that's a
    // sign-out, not a settings action) or the last remaining admin.
    if (currentUser.role === 'admin' && !isSelf) {
        actions.append(
            buttonEl('settings-btn', 'Access', () => {
                void openAccessModal(user, allUsers, state, rerender);
            }),
        );

        const lastAdmin = user.role === 'admin' && countAdmins(allUsers) <= 1;
        const remove = document.createElement('button');
        remove.type = 'button';
        remove.className = 'settings-icon-btn settings-danger-icon';
        remove.innerHTML = icon('delete', 18);
        remove.setAttribute('aria-label', `Remove ${user.username}`);
        remove.title = lastAdmin ? 'Cannot remove the last admin' : `Remove ${user.username}`;
        remove.disabled = lastAdmin;
        remove.addEventListener('click', () => removeUser(user, state, rerender));
        actions.append(remove);
    }

    row.append(main, actions);
    return row;
}

function accessSummary(user: UserAccount): string {
    const role = capitalize(user.role);
    if (user.content_scope === 'shelves') {
        const count = user.scope_shelf_ids?.length || 0;
        return `${role} · ${count} ${count === 1 ? 'shelf' : 'shelves'}`;
    }
    return role;
}

function openPasswordModal(user: UserAccount, isSelf: boolean, rerender: () => void): void {
    const fields = fieldGroup();
    const password = makeInput('password', 'new-password');
    const confirm = makeInput('password', 'new-password');
    fields.append(formField('New password', password), formField('Confirm password', confirm));

    openFormModal({
        title: isSelf ? 'Change password' : `Reset password — ${user.username}`,
        submitLabel: isSelf ? 'Change password' : 'Reset password',
        fields,
        focus: password,
        onSubmit: async (setError) => {
            if (!password.value) {
                setError('Password is required');
                return false;
            }
            if (password.value !== confirm.value) {
                setError('Passwords do not match');
                return false;
            }
            try {
                await updateUserPassword(user.id, password.value);
                showToast(isSelf ? 'Password changed' : `Password changed for ${user.username}`);
                rerender();
                return true;
            } catch (err) {
                showToast(errorMessage(err, 'Password update failed'), { type: 'error' });
                return false;
            }
        },
    });
}

async function openAddUserModal(state: UsersState, rerender: () => void): Promise<void> {
    let shelves: Shelf[];
    try {
        shelves = await fetchShelves();
    } catch (err) {
        showToast(errorMessage(err, 'Load shelves failed'), { type: 'error' });
        return;
    }

    const fields = fieldGroup();
    const username = makeInput('text', 'off');
    const password = makeInput('password', 'new-password');
    const access = createAccessControls({
        role: 'reader',
        contentScope: 'all',
        scopeShelfIDs: [],
        shelves,
    });
    fields.append(
        formField('Username', username),
        formField('Password', password),
        ...access.fields,
    );

    openFormModal({
        title: 'Add user',
        submitLabel: 'Add user',
        fields,
        focus: username,
        onSubmit: async (setError) => {
            if (!username.value.trim()) {
                setError('Username is required');
                return false;
            }
            if (!password.value) {
                setError('Password is required');
                return false;
            }
            try {
                const created = await createUser({
                    username: username.value.trim(),
                    password: password.value,
                    role: access.role(),
                    content_scope: access.contentScope(),
                    scope_shelf_ids: access.scopeShelfIDs(),
                });
                state.users = [...state.users, created].sort((a, b) =>
                    a.username.localeCompare(b.username),
                );
                showToast(`Added ${created.username}`);
                rerender();
                return true;
            } catch (err) {
                showToast(errorMessage(err, 'Create user failed'), { type: 'error' });
                return false;
            }
        },
    });
}

async function openAccessModal(
    user: UserAccount,
    allUsers: UserAccount[],
    state: UsersState,
    rerender: () => void,
): Promise<void> {
    let shelves: Shelf[];
    try {
        shelves = await fetchShelves();
    } catch (err) {
        showToast(errorMessage(err, 'Load shelves failed'), { type: 'error' });
        return;
    }

    const fields = fieldGroup();
    const access = createAccessControls({
        role: user.role,
        contentScope: user.content_scope || 'all',
        scopeShelfIDs: user.scope_shelf_ids || [],
        shelves,
    });
    fields.append(...access.fields);

    openFormModal({
        title: `Access — ${user.username}`,
        submitLabel: 'Save access',
        fields,
        focus: access.focus,
        onSubmit: async (setError) => {
            const nextRole = access.role();
            if (user.role === 'admin' && nextRole !== 'admin' && countAdmins(allUsers) <= 1) {
                setError('Cannot demote the last admin');
                return false;
            }
            try {
                const updated = await updateUserAccess(user.id, {
                    role: nextRole,
                    content_scope: access.contentScope(),
                    scope_shelf_ids: access.scopeShelfIDs(),
                });
                state.users = state.users.map((item) => (item.id === updated.id ? updated : item));
                showToast(`Updated ${updated.username}`);
                rerender();
                return true;
            } catch (err) {
                showToast(errorMessage(err, 'Access update failed'), { type: 'error' });
                return false;
            }
        },
    });
}

function createAccessControls(opts: {
    role: UserAccount['role'];
    contentScope: UserAccount['content_scope'];
    scopeShelfIDs: string[];
    shelves: Shelf[];
}): {
    fields: HTMLElement[];
    focus: HTMLElement;
    role(): UserAccount['role'];
    contentScope(): UserAccount['content_scope'];
    scopeShelfIDs(): string[];
} {
    const role = createSelect({
        options: roleOptions(),
        value: opts.role,
        ariaLabel: 'User role',
        onChange: sync,
    });
    const scope = createSelect({
        options: [
            { value: 'all', label: 'All library' },
            { value: 'shelves', label: 'Selected shelves' },
        ],
        value: opts.contentScope,
        ariaLabel: 'Content scope',
        onChange: sync,
    });

    const shelfBox = document.createElement('div');
    shelfBox.className = 'settings-scope-shelves';
    const checked = new Set(opts.scopeShelfIDs);
    const visibleShelfIDs = new Set(opts.shelves.map((shelf) => shelf.id));
    const hiddenScopeShelfIDs = opts.scopeShelfIDs.filter((id) => !visibleShelfIDs.has(id));
    if (opts.shelves.length === 0) {
        shelfBox.append(textEl('div', 'settings-block-hint', 'No shelves yet'));
    } else {
        for (const shelf of opts.shelves) {
            const input = document.createElement('input');
            input.type = 'checkbox';
            input.className = 'settings-checkbox';
            input.value = shelf.id;
            input.checked = checked.has(shelf.id);
            shelfBox.append(shelfCheckboxField(shelf, input));
        }
    }
    if (hiddenScopeShelfIDs.length > 0) {
        shelfBox.append(
            textEl(
                'div',
                'settings-block-hint',
                `${hiddenScopeShelfIDs.length} assigned shelf${hiddenScopeShelfIDs.length === 1 ? '' : 's'} not visible to you`,
            ),
        );
    }

    const roleField = formField('Role', role.el);
    const scopeField = formField('Library access', scope.el);
    const shelfField = formField('Shelves', shelfBox);
    sync();

    function sync(): void {
        const isReader = role.getValue() === 'reader';
        scope.el.disabled = !isReader;
        scopeField.hidden = !isReader;
        shelfField.hidden = !isReader || scope.getValue() !== 'shelves';
    }

    return {
        fields: [roleField, scopeField, shelfField],
        focus: role.el,
        role: () => role.getValue() as UserAccount['role'],
        contentScope: () =>
            role.getValue() !== 'reader'
                ? 'all'
                : (scope.getValue() as UserAccount['content_scope']),
        scopeShelfIDs: () =>
            role.getValue() !== 'reader' || scope.getValue() !== 'shelves'
                ? []
                : [
                      ...hiddenScopeShelfIDs,
                      ...Array.from(
                          shelfBox.querySelectorAll<HTMLInputElement>('input:checked'),
                      ).map((input) => input.value),
                  ],
    };
}

function roleOptions(): { value: string; label: string }[] {
    return [
        { value: 'reader', label: 'Reader' },
        { value: 'member', label: 'Member' },
        { value: 'admin', label: 'Admin' },
    ];
}

function shelfLabel(shelf: Shelf): string {
    const parts = [shelf.name];
    if (shelf.visibility === 'personal') parts.push('Private');
    return parts.join(' · ');
}

function shelfCheckboxField(shelf: Shelf, input: HTMLInputElement): HTMLLabelElement {
    const label = document.createElement('label');
    label.className = 'settings-checkbox-field settings-shelf-checkbox-field';

    const marker = document.createElement('span');
    marker.className = 'shelf-kind-marker';
    marker.dataset.kind = shelf.kind;
    marker.setAttribute('aria-hidden', 'true');

    const text = document.createElement('span');
    text.className = 'settings-shelf-checkbox-label';
    text.textContent = shelfLabel(shelf);

    label.append(input, marker, text);
    return label;
}

async function removeUser(
    user: UserAccount,
    state: UsersState,
    rerender: () => void,
): Promise<void> {
    const sharedShelves = user.shared_shelf_names ?? [];
    let body = `Remove ${user.username}? They lose access immediately. This cannot be undone.`;
    if (sharedShelves.length > 0) {
        // Deleting the owner cascades their shared shelves away for the whole
        // household — say so plainly instead of losing "Kids" silently.
        const list = sharedShelves.join(', ');
        const noun = sharedShelves.length === 1 ? 'shared shelf' : 'shared shelves';
        body += ` Their ${noun} (${list}) will be removed for everyone.`;
    }
    const confirmed = await confirmModal({
        title: 'Remove user',
        body,
        confirmLabel: 'Remove',
        danger: true,
    });
    if (!confirmed) return;
    try {
        await deleteUser(user.id);
        state.users = state.users.filter((item) => item.id !== user.id);
        showToast(`Removed ${user.username}`);
        rerender();
    } catch (err) {
        showToast(errorMessage(err, 'Remove user failed'), { type: 'error' });
    }
}

function capitalize(value: string): string {
    return value ? value[0].toUpperCase() + value.slice(1) : value;
}

function countAdmins(users: UserAccount[]): number {
    return users.filter((user) => user.role === 'admin').length;
}
