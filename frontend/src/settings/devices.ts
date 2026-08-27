import {
    createDeliveryDevice,
    deleteDeliveryDevice,
    fetchDeliveries,
    fetchDeliveryDevices,
    fetchEmailDeliverySettings,
    saveEmailDeliverySettings,
    sendEmailDeliveryTest,
    updateDeliveryDevice,
} from '../api';
import { createSelect } from '../components/select';
import { formField, textEl } from '../dom';
import { errorMessage } from '../errors';
import { confirmModal } from '../modal';
import { showToast } from '../toast';
import type {
    CurrentUser,
    DeliveryDevice,
    DeliveryJob,
    DeliveryPreset,
    EmailDeliverySettings,
} from '../types';
import {
    type AsyncLoadState,
    buttonEl,
    fieldGroup,
    makeInput,
    openFormModal,
    renderAsyncSection,
    settingsItemRow,
    settingsRow,
} from './ui';

type DevicesState = AsyncLoadState & {
    email: EmailDeliverySettings | null;
    devices: DeliveryDevice[];
    deliveries: DeliveryJob[];
};

export function createDevicesPanel(currentUser: CurrentUser): (root: HTMLElement) => void {
    const state: DevicesState = {
        loaded: false,
        loading: false,
        email: null,
        devices: [],
        deliveries: [],
        loadError: '',
    };
    return (root) => renderDevicesPanel(root, currentUser, state);
}

function renderDevicesPanel(
    root: HTMLElement,
    currentUser: CurrentUser,
    state: DevicesState,
): void {
    root.replaceChildren();
    const isAdmin = currentUser.role === 'admin';
    const rerender = () => renderDevicesPanel(root, currentUser, state);

    root.append(
        textEl('h3', 'settings-section-title', 'Devices'),
        textEl(
            'p',
            'settings-section-intro',
            'Send books to reader email addresses such as Kindle or PocketBook.',
        ),
    );

    if (
        renderAsyncSection(state, {
            target: root,
            load: async () => {
                const [devices, deliveries, email] = await Promise.all([
                    fetchDeliveryDevices(),
                    fetchDeliveries(10),
                    isAdmin ? fetchEmailDeliverySettings() : Promise.resolve(null),
                ]);
                state.devices = devices;
                state.deliveries = deliveries;
                state.email = email;
            },
            rerender,
            errorFallback: 'Failed to load devices',
        })
    ) {
        return;
    }

    if (isAdmin && state.email) {
        root.append(createEmailDeliveryBlock(state, rerender));
    }
    root.append(createDeviceListBlock(state, rerender), createDeliveryHistoryBlock(state));
}

function createEmailDeliveryBlock(state: DevicesState, rerender: () => void): HTMLElement {
    const section = document.createElement('section');
    section.className = 'settings-block';
    section.append(
        textEl('h4', 'settings-subsection-title', 'Email delivery'),
        textEl(
            'div',
            'settings-block-hint',
            'Use an app password or dedicated SMTP relay account. The password is stored recoverably so polka can send mail.',
        ),
    );
    const email = state.email;
    if (!email) return section;

    const host = inputWithValue(email.host);
    const port = inputWithValue(String(email.port || 587), 'number');
    const username = inputWithValue(email.username);
    const password = inputWithValue('', 'password');
    password.placeholder = email.password_set ? 'Saved password unchanged' : '';
    password.required = false;
    const fromAddress = inputWithValue(email.from_address);
    const fromName = inputWithValue(email.from_name || 'polka');
    const limit = inputWithValue(String(email.attachment_limit_mb || 25), 'number');
    const security = createSelect({
        ariaLabel: 'SMTP security',
        value: email.security || 'auto',
        options: [
            { value: 'auto', label: 'Auto' },
            { value: 'starttls', label: 'STARTTLS' },
            { value: 'ssl', label: 'SSL/TLS' },
            { value: 'plain', label: 'Plain' },
        ],
    });

    const rows = document.createElement('div');
    rows.className = 'settings-rows';
    rows.append(
        settingsRow('SMTP host', 'Mail server hostname.', host),
        settingsRow('SMTP port', '587 for STARTTLS, 465 for SSL/TLS.', port),
        settingsRow('Security', '', security.el),
        settingsRow('Username', 'Leave blank only for a no-auth relay.', username),
        settingsRow('Password', 'Leave blank to keep the saved password.', password),
        settingsRow('From address', 'Add this address to Amazon approved senders.', fromAddress),
        settingsRow('From name', '', fromName),
        settingsRow('Attachment limit', 'Raw files are checked with base64 email overhead.', limit),
    );

    const actions = document.createElement('div');
    actions.className = 'settings-section-action';
    actions.append(
        buttonEl('settings-btn settings-primary-btn', 'Save email settings', async () => {
            try {
                const payload: Parameters<typeof saveEmailDeliverySettings>[0] = {
                    host: host.value.trim(),
                    port: Number(port.value),
                    security: emailSecurityValue(security.getValue()),
                    username: username.value.trim(),
                    from_address: fromAddress.value.trim(),
                    from_name: fromName.value.trim(),
                    attachment_limit_mb: Number(limit.value),
                };
                if (password.value) payload.password = password.value;
                state.email = await saveEmailDeliverySettings(payload);
                showToast('Email settings saved');
                rerender();
            } catch (err) {
                showToast(errorMessage(err, 'Save email settings failed'), { type: 'error' });
            }
        }),
        buttonEl('settings-btn', 'Send test', () => openSendTestModal()),
    );

    section.append(rows, actions);
    return section;
}

function createDeviceListBlock(state: DevicesState, rerender: () => void): HTMLElement {
    const section = document.createElement('section');
    section.className = 'settings-block';

    const action = document.createElement('div');
    action.className = 'settings-section-action';
    action.append(
        buttonEl('settings-btn settings-primary-btn', 'Add device', () =>
            openDeliveryDeviceModal(null, state, rerender),
        ),
    );

    section.append(
        textEl('h4', 'settings-subsection-title', 'Send devices'),
        textEl(
            'div',
            'settings-block-hint',
            state.email?.from_address
                ? `For Kindle, approve ${state.email.from_address} in Amazon settings.`
                : 'Kindle users must approve the configured From address in Amazon settings.',
        ),
        action,
    );

    const list = document.createElement('div');
    list.className = 'settings-item-list';
    section.append(list);
    if (state.devices.length === 0) {
        list.append(textEl('div', 'settings-item-empty', 'No devices yet'));
        return section;
    }
    for (const device of state.devices) {
        list.append(createDeliveryDeviceRow(device, state, rerender));
    }
    return section;
}

function createDeliveryDeviceRow(
    device: DeliveryDevice,
    state: DevicesState,
    rerender: () => void,
): HTMLElement {
    const suffix = device.is_default ? ' · Default' : '';
    const actions: HTMLElement[] = [];
    if (!device.is_default) {
        actions.push(
            buttonEl('settings-btn', 'Default', async () => {
                try {
                    const updated = await updateDeliveryDevice(device.id, { is_default: true });
                    state.devices = state.devices.map((item) =>
                        item.id === updated.id ? updated : { ...item, is_default: false },
                    );
                    rerender();
                } catch (err) {
                    showToast(errorMessage(err, 'Set default failed'), { type: 'error' });
                }
            }),
        );
    }
    actions.push(
        buttonEl('settings-btn', 'Edit', () => openDeliveryDeviceModal(device, state, rerender)),
        buttonEl('settings-btn settings-danger-btn', 'Remove', async () => {
            const confirmed = await confirmModal({
                title: 'Remove device',
                body: `Remove ${device.name}?`,
                confirmLabel: 'Remove',
                danger: true,
            });
            if (!confirmed) return;
            try {
                await deleteDeliveryDevice(device.id);
                state.devices = await fetchDeliveryDevices();
                rerender();
            } catch (err) {
                showToast(errorMessage(err, 'Remove device failed'), { type: 'error' });
            }
        }),
    );
    return settingsItemRow({
        name: device.name,
        meta: `${device.email} · ${presetLabel(device.preset)}${suffix}`,
        actions,
    });
}

function createDeliveryHistoryBlock(state: DevicesState): HTMLElement {
    const section = document.createElement('section');
    section.className = 'settings-block';
    section.append(
        textEl('h4', 'settings-subsection-title', 'Recent sends'),
        textEl('div', 'settings-block-hint', 'Sent means accepted by the mail server.'),
    );
    const list = document.createElement('div');
    list.className = 'settings-item-list';
    section.append(list);
    if (state.deliveries.length === 0) {
        list.append(textEl('div', 'settings-item-empty', 'No sends yet'));
        return section;
    }
    for (const job of state.deliveries) {
        list.append(
            settingsItemRow({
                name: job.title,
                meta: `${job.status} · ${job.device_name}${job.error ? ` · ${job.error}` : ''}`,
            }),
        );
    }
    return section;
}

function openDeliveryDeviceModal(
    device: DeliveryDevice | null,
    state: DevicesState,
    rerender: () => void,
): void {
    const fields = fieldGroup();
    const name = makeInput('text', 'off');
    name.value = device?.name || '';
    const email = makeInput('email', 'email');
    email.value = device?.email || '';
    const preset = createSelect({
        ariaLabel: 'Device preset',
        value: device?.preset || 'kindle',
        options: [
            { value: 'kindle', label: 'Kindle' },
            { value: 'pocketbook', label: 'PocketBook' },
            { value: 'generic', label: 'Generic email' },
        ],
    });
    fields.append(
        formField('Name', name),
        formField('Email', email),
        formField('Preset', preset.el),
    );

    openFormModal({
        title: device ? 'Edit device' : 'Add device',
        submitLabel: device ? 'Save device' : 'Add device',
        fields,
        focus: name,
        onSubmit: async (setError) => {
            if (!name.value.trim()) {
                setError('Name is required');
                return false;
            }
            if (!email.value.trim()) {
                setError('Email is required');
                return false;
            }
            try {
                if (device) {
                    const updated = await updateDeliveryDevice(device.id, {
                        name: name.value.trim(),
                        email: email.value.trim(),
                        preset: preset.getValue() as DeliveryPreset,
                    });
                    state.devices = state.devices.map((item) =>
                        item.id === updated.id ? updated : item,
                    );
                } else {
                    const created = await createDeliveryDevice({
                        name: name.value.trim(),
                        email: email.value.trim(),
                        preset: preset.getValue() as DeliveryPreset,
                    });
                    state.devices = await fetchDeliveryDevices();
                    showToast(`Added ${created.name}`);
                }
                rerender();
                return true;
            } catch (err) {
                showToast(errorMessage(err, device ? 'Save device failed' : 'Add device failed'), {
                    type: 'error',
                });
                return false;
            }
        },
    });
}

function openSendTestModal(): void {
    const fields = fieldGroup();
    const to = makeInput('email', 'email');
    fields.append(formField('Send test to', to));
    openFormModal({
        title: 'Send test message',
        submitLabel: 'Send test',
        fields,
        focus: to,
        onSubmit: async (setError) => {
            if (!to.value.trim()) {
                setError('Recipient is required');
                return false;
            }
            try {
                await sendEmailDeliveryTest(to.value.trim());
                showToast('Test message sent');
                return true;
            } catch (err) {
                showToast(errorMessage(err, 'Test send failed'), { type: 'error' });
                return false;
            }
        },
    });
}

function inputWithValue(value: string, type = 'text'): HTMLInputElement {
    const input = makeInput(type, type === 'password' ? 'current-password' : 'off');
    input.value = value;
    return input;
}

function presetLabel(preset: DeliveryPreset): string {
    if (preset === 'kindle') return 'Kindle';
    if (preset === 'pocketbook') return 'PocketBook';
    return 'Generic';
}

function emailSecurityValue(value: string): EmailDeliverySettings['security'] {
    if (value === 'starttls' || value === 'ssl' || value === 'plain') return value;
    return 'auto';
}

// Reading-app integrations load independently: an experimental Kobo endpoint
// must not hide stable app-password and OPDS settings when it fails. KOSync's
// secret URL is still shown only in the one-time result dialog after password
// creation.
