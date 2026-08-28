import {
    createDeliveryDevice,
    deleteDeliveryDevice,
    fetchDeliveries,
    fetchDeliveryDevices,
    fetchEmailDeliverySettings,
    saveEmailDeliverySettings,
    saveSendEnabled,
    sendEmailDeliveryTest,
    updateDeliveryDevice,
} from '../api';
import { sendEnabled, setSendEnabled } from '../bootstrap';
import { createSelect } from '../components/select';
import { createToggle } from '../components/toggle';
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
    settingsReveal,
    settingsRow,
} from './ui';

const DEFAULT_SMTP_PORT = 587;
const DEFAULT_ATTACHMENT_LIMIT_MB = 25;

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

// Nothing below the switch exists for a library that does not send books. An
// admin still sees the switch itself, because turning it on is the way in.
function renderDevicesPanel(
    root: HTMLElement,
    currentUser: CurrentUser,
    state: DevicesState,
): void {
    root.replaceChildren();
    const isAdmin = currentUser.role === 'admin';
    const rerender = () => renderDevicesPanel(root, currentUser, state);

    root.append(textEl('h3', 'settings-section-title', 'Devices'));

    if (isAdmin) root.append(createSendSwitchRow(rerender));
    if (!sendEnabled()) return;

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

function createSendSwitchRow(rerender: () => void): HTMLElement {
    const rows = document.createElement('div');
    rows.className = 'settings-rows';
    const apply = async (checked: boolean) => {
        try {
            setSendEnabled(await saveSendEnabled(checked));
        } catch (err) {
            showToast(errorMessage(err, 'Failed to save sending setting'), { type: 'error' });
            toggle.setChecked(!checked);
            return;
        }
        rerender();
    };
    const toggle = createToggle({
        ariaLabel: 'Sending books to a device',
        checked: sendEnabled(),
        onChange: (checked) => void apply(checked),
    });
    rows.append(
        settingsRow(
            'Sending',
            'Send books to a Kindle, PocketBook, or any reader email address.',
            toggle.el,
        ),
    );
    return rows;
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

    // Host, credentials, and the address it sends as describe a mail server;
    // everything else has a working default and waits under Advanced. SMTP-prefixed
    // labels and a new-password field keep the browser from offering the polka
    // account here.
    const host = smtpInput('text', email.host, 'settings-input-wide');
    const port = smtpInput(
        'number',
        String(email.port || DEFAULT_SMTP_PORT),
        'settings-input-port',
    );
    const username = smtpInput('text', email.username, 'settings-input-wide');
    const password = smtpInput('password', '', 'settings-input-wide');
    password.autocomplete = 'new-password';
    password.placeholder = email.password_set ? 'Saved password unchanged' : '';
    password.required = false;
    const fromAddress = smtpInput('text', email.from_address, 'settings-input-wide');
    const fromName = smtpInput('text', email.from_name, 'settings-input-wide');
    const limit = smtpInput(
        'number',
        String(email.attachment_limit_mb || DEFAULT_ATTACHMENT_LIMIT_MB),
        'settings-input-port',
    );
    const security = createSelect({
        ariaLabel: 'SMTP security',
        value: email.security || 'auto',
        options: [
            { value: 'auto', label: 'Auto' },
            { value: 'starttls', label: 'STARTTLS' },
            { value: 'ssl', label: 'SSL/TLS' },
            { value: 'plain', label: 'Plain' },
        ],
        onChange: () => syncActions(),
    });

    const rows = document.createElement('div');
    rows.className = 'settings-rows';
    rows.append(
        settingsRow('SMTP host', '', host),
        settingsRow('SMTP port', '587 for STARTTLS, 465 for SSL/TLS.', port),
        settingsRow('SMTP username', 'Leave blank only for a no-auth relay.', username),
        settingsRow(
            'SMTP password',
            email.password_set ? 'Leave blank to keep the saved password.' : '',
            password,
        ),
        settingsRow('From address', 'Add this address to Amazon approved senders.', fromAddress),
    );

    const advancedRows = document.createElement('div');
    advancedRows.className = 'settings-rows';
    advancedRows.append(
        settingsRow('Security', 'Auto picks it from the port.', security.el),
        settingsRow(
            'From name',
            'Sender name on the device. Blank sends the address alone.',
            fromName,
        ),
        settingsRow('Attachment limit', 'Raw files are checked with base64 email overhead.', limit),
    );

    const save = buttonEl('settings-btn settings-primary-btn', 'Save email settings', async () => {
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
    });
    const test = buttonEl('settings-btn', 'Send test', () => openSendTestModal());

    // A test uses persisted settings, so disable it while the form has unsaved edits.
    const fields = [host, port, username, password, fromAddress, fromName, limit];
    const snapshot = () =>
        JSON.stringify([...fields.map((field) => field.value.trim()), security.getValue()]);
    const saved = snapshot();
    const configured = email.configured;
    function syncActions(): void {
        const edited = snapshot() !== saved;
        save.disabled = !edited;
        test.disabled = edited || !configured;
    }
    for (const field of fields) field.addEventListener('input', syncActions);
    syncActions();

    const actions = document.createElement('div');
    actions.className = 'settings-section-action';
    actions.append(save, test);

    section.append(rows, settingsReveal('Advanced', advancedRows, advancedTouched(email)), actions);
    return section;
}

// A setting that is doing work never waits behind a click to be found.
function advancedTouched(email: EmailDeliverySettings): boolean {
    return (
        (email.security || 'auto') !== 'auto' ||
        email.from_name !== '' ||
        (email.attachment_limit_mb || DEFAULT_ATTACHMENT_LIMIT_MB) !== DEFAULT_ATTACHMENT_LIMIT_MB
    );
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

function smtpInput(type: string, value: string, sizeClass = ''): HTMLInputElement {
    const input = makeInput(type, 'off');
    input.value = value;
    if (sizeClass) input.classList.add(sizeClass);
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
