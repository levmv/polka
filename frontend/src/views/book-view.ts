import {
    createDelivery,
    createDeliveryDevice,
    deleteBook,
    fetchBook,
    fetchCurrentUser,
    fetchDeliveryJob,
    fetchReaderState,
    fetchSendOptions,
    resetReaderState,
    setReadingStatus,
    writebackBook,
} from '../api';
import {
    type BookListContext,
    listURLForContext,
    readBookListContextFromLocation,
} from '../book-list-context';
import { SEND_ENABLED_EVENT, sendEnabled } from '../bootstrap';
import { coverImgHtml } from '../cover';
import { escapeHtml, formField, textEl } from '../dom';
import { errorMessage } from '../errors';
import { icon } from '../icons';
import {
    type Identifier,
    identifierLabel,
    identifierLink,
    isPrimaryIdentifier,
    parseIdentifiers,
} from '../identifiers';
import { createMenu, type MenuItem } from '../menu';
import { confirmModal, openModal } from '../modal';
import { createPopover } from '../popover';
import type { RouteCleanup } from '../router';
import { queryTerm, seriesLibraryURL } from '../search-query';
import { showToast } from '../toast';
import type {
    Asset,
    Book,
    DeliveryJob,
    DeliveryPlan,
    DeliveryPreset,
    ReaderState,
    ReadingStatus,
    SendOptions,
} from '../types';
import { openEditModal } from './book-edit';
import { renderShelfPicker } from './book-shelf-picker';

// Re-exported so existing importers (library/table view) keep pulling the edit
// modal from the book-view facade.
export { openEditModal };

// The currently rendered book detail. The edit modal and metadata-fetch dialog
// read and refresh it through the accessors below so the page stays in sync
// after an in-place save.
let currentBookDetail: Book | null = null;
let currentBookListContext: BookListContext | null = null;
// Router rejects stale mount results after mount() resolves, but a slow book
// fetch can mutate the current detail skeleton before returning a cleanup.
let bookDetailLoadToken = 0;
let currentDetailRenderCleanup: RouteCleanup | null = null;

export function getCurrentBookDetail(): Book | null {
    return currentBookDetail;
}

export function setCurrentBookDetail(b: Book | null): void {
    currentBookDetail = b;
}

export function getCurrentBookListContext(): BookListContext | null {
    return currentBookListContext;
}

function formatSize(bytes: number): string {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return `${parseFloat((bytes / k ** i).toFixed(1))} ${sizes[i]}`;
}

function formatTimestampHuman(timestamp: number): string {
    const d = new Date(timestamp * 1000);
    if (Number.isNaN(d.getTime())) return '';
    return d.toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' });
}

function isReadableAsset(asset: Asset): boolean {
    return asset.can_read === true;
}

function assetFormatLabel(asset: Asset): string {
    return asset.extension.toUpperCase().replace('.', '');
}

function assetDownloadUrl(asset: Asset): string {
    return `/download/${encodeURIComponent(asset.id)}`;
}

function assetDownloadAsUrl(asset: Asset, target: string): string {
    return `/download/${encodeURIComponent(asset.id)}/as/${encodeURIComponent(target)}`;
}

function annotationExportUrl(asset: Asset, format: 'html' | 'markdown'): string {
    const url = `/api/reader/assets/${encodeURIComponent(asset.id)}/annotations/export`;
    return format === 'markdown' ? `${url}?format=markdown` : url;
}

function assetDownloadHtml(asset: Asset): string {
    const label = assetFormatLabel(asset);
    const nativeLink = `<a href="${assetDownloadUrl(asset)}" class="detail-action detail-download-main" target="_blank" rel="noopener noreferrer">${icon('download', 16)}${escapeHtml(label)}</a>`;
    if (!asset.download_as || asset.download_as.length === 0) {
        return nativeLink;
    }
    return `
        <span class="detail-download-group">
            ${nativeLink}
            <button class="detail-action detail-action-icon detail-download-menu" type="button" data-download-menu-asset="${escapeHtml(asset.id)}" aria-label="Download formats for ${escapeHtml(label)}" title="Download formats">
                ${icon('expand_more', 18)}
            </button>
        </span>
    `;
}

function assetDownloadOptions(asset: Asset): Array<{ label: string; url: string }> {
    const options = [
        {
            label: `Download ${assetFormatLabel(asset)}`,
            url: assetDownloadUrl(asset),
        },
    ];
    for (const option of asset.download_as || []) {
        options.push({
            label: `Download ${option.label}`,
            url: assetDownloadAsUrl(asset, option.target),
        });
    }
    return options;
}

function openDownload(url: string): void {
    const opened = window.open(url, '_blank', 'noopener,noreferrer');
    if (opened) {
        opened.opener = null;
        return;
    }
    window.location.href = url;
}

export async function initBookDetail(workId: string): Promise<RouteCleanup | undefined> {
    const token = ++bookDetailLoadToken;
    try {
        currentBookListContext = readBookListContextFromLocation();
        const [b, me] = await Promise.all([fetchBook(workId), fetchCurrentUser()]);
        if (token !== bookDetailLoadToken) return undefined;

        const container = document.getElementById('book-detail-container');
        if (!container) return;

        currentBookDetail = b;
        updateBackLink(currentBookListContext);
        container.classList.remove('book-detail-loading');
        container.removeAttribute('aria-busy');
        renderBookDetail(container, b, { canCurate: me.role === 'admin' || me.role === 'member' });

        document.title = `${b.title} - polka`;
        return () => {
            currentDetailRenderCleanup?.();
            currentDetailRenderCleanup = null;
            if (token === bookDetailLoadToken) {
                currentBookDetail = null;
                currentBookListContext = null;
            }
            bookDetailLoadToken++;
        };
    } catch (e) {
        if (token !== bookDetailLoadToken) return undefined;
        console.error('Failed to load book detail:', e);
        const container = document.getElementById('book-detail-container');
        if (container) {
            container.classList.remove('book-detail-loading');
            container.removeAttribute('aria-busy');
            container.innerHTML = '<h2>Book not found.</h2>';
        }
    }
}

function updateBackLink(context: BookListContext | null): void {
    const link = document.querySelector<HTMLAnchorElement>('.back-link a');
    if (!link) return;
    link.href = listURLForContext(context);
}

// The clamp and the hidden toggle ship in the markup, so a long blurb is never
// briefly full height. This decides, once the text has been laid out, whether
// the blurb earns a toggle or should simply be shown whole.
function setupDescriptionDisclosure(container: HTMLElement): void {
    const description = container.querySelector<HTMLElement>('.detail-description');
    const more = container.querySelector<HTMLButtonElement>('.detail-description-more');
    if (!description || !more) return;

    // Let the engine count the lines. If the whole blurb fits inside the more
    // generous probe clamp, hiding the remainder would only buy a click for a
    // line or two, so the clamp comes off instead.
    description.classList.replace('detail-description--collapsed', 'detail-description--probe');
    const fitsWithSlack = description.scrollHeight <= description.clientHeight;
    description.classList.replace('detail-description--probe', 'detail-description--collapsed');
    if (fitsWithSlack) {
        description.classList.remove('detail-description--collapsed');
        more.remove();
        return;
    }

    more.hidden = false;
    more.addEventListener('click', () => {
        description.classList.remove('detail-description--collapsed');
        more.remove();
    });
}

export function renderBookDetail(
    container: HTMLElement,
    b: Book,
    opts: { loadReaderProgress?: boolean; canCurate?: boolean } = {},
): RouteCleanup {
    currentDetailRenderCleanup?.();
    currentDetailRenderCleanup = null;
    const cleanup: RouteCleanup[] = [];
    const canCurate = opts.canCurate ?? true;
    const authorsHtml = b.authors_list
        .map((author) => author.name)
        .filter((name) => name)
        .map(
            (name) =>
                `<a href="/?q=${encodeURIComponent(queryTerm('author', name))}">${escapeHtml(name)}</a>`,
        )
        .join(' &amp; ');

    let seriesHtml = '';
    if (b.series) {
        const seriesName = b.series;
        seriesHtml = `<div class="detail-series"><a href="${escapeHtml(seriesLibraryURL(seriesName))}">${escapeHtml(seriesName)}</a> ${b.series_index ? `#${b.series_index}` : ''}</div>`;
    }

    let descHtml = '';
    if (b.description_html) {
        descHtml = `
            <div class="detail-description-block">
                <div class="detail-description detail-description--collapsed">${b.description_html}</div>
                <button type="button" class="detail-description-more" hidden>Show more</button>
            </div>
        `;
    }

    // Publication facts + identifiers — the "Details" block that lives in the
    // cover rail (see the layout below).
    let detailsHtml = '';
    if (b.language || b.publisher || b.year || b.identifiers || b.date_human) {
        detailsHtml = '<div class="detail-meta detail-meta-top">';
        if (b.language)
            detailsHtml += `<span>Language: ${escapeHtml(b.language_name || b.language)}</span><br>`;
        if (b.publisher || b.date_human || b.year) {
            const parts = [];
            if (b.publisher) parts.push(escapeHtml(b.publisher));
            if (b.date_human) parts.push(escapeHtml(b.date_human));
            else if (b.year) parts.push(escapeHtml(b.year));
            detailsHtml += `<span>${parts.join(' &middot; ')}</span><br>`;
        }
        if (b.identifiers) {
            const ids = parseIdentifiers(b.identifiers).filter((id) => {
                const t = id.type.toLowerCase();
                return t !== 'uuid' && t !== 'calibre';
            });
            const chip = (id: Identifier, extra = false) => {
                const link = identifierLink(id);
                // Store/retailer ids are opaque routing strings — the value is
                // noise, so a linked store id shows only its label. Bibliographic
                // ids (ISBN/DOI) and any link-less scheme keep the value, which
                // is the part worth reading or copying.
                const showValue = !link || isPrimaryIdentifier(id);
                const text = showValue
                    ? `${identifierLabel(id.type)} ${id.value}`
                    : identifierLabel(id.type);
                const cls = `detail-tag detail-identifier${extra ? ' detail-ids-extra' : ''}`;
                const hid = extra ? ' hidden' : '';
                if (link) {
                    return `<a href="${escapeHtml(link)}" target="_blank" rel="noopener noreferrer" class="${cls}"${hid}>${escapeHtml(text)}</a>`;
                }
                return `<span class="${cls}"${hid}>${escapeHtml(text)}</span>`;
            };

            // Bibliographic ids (ISBN/DOI) lead; if there are none, show the
            // first store id so the row has a visible anchor. The rest carry the
            // `hidden` attribute and reveal in place behind a quiet inline "…".
            // All chips are direct flex children so they share the row's gap and
            // chip styling — a wrapping element would collapse them into a single
            // inline item with no spacing.
            const primary = ids.filter(isPrimaryIdentifier);
            const store = ids.filter((id) => !isPrimaryIdentifier(id));
            const visible = primary.length > 0 ? primary : store.slice(0, 1);
            const hidden = primary.length > 0 ? store : store.slice(1);
            if (visible.length > 0) {
                let row = `<div class="detail-identifiers-row">`;
                row += visible.map((id) => chip(id)).join('');
                row += hidden.map((id) => chip(id, true)).join('');
                if (hidden.length > 0) {
                    const plural = hidden.length > 1 ? 's' : '';
                    row += `<button type="button" class="detail-id-more" data-reveal=".detail-ids-extra" aria-expanded="false" aria-label="Show ${hidden.length} more identifier${plural}">…</button>`;
                }
                row += '</div>';
                detailsHtml += row;
            }
        }
        detailsHtml += '</div>';
    }

    let tagsHtml = '';
    if (b.tags) {
        const tags = b.tags
            .split(',')
            .map((t: string) => t.trim())
            .filter((t: string) => t.length > 0);
        if (tags.length > 0) {
            const tagChip = (tag: string, extra = false) =>
                `<a href="/?q=${encodeURIComponent(queryTerm('tag', tag))}" class="detail-tag${extra ? ' detail-tags-extra' : ''}"${extra ? ' hidden' : ''}>${escapeHtml(tag)}</a>`;
            // The narrow cover rail can hold a handful of tags comfortably; the
            // long tail collapses behind a quiet "+N". Hidden tags are direct
            // flex children carrying `hidden` (not wrapped) so they keep the
            // chip gap/styling when revealed.
            const TAGS_VISIBLE = 8;
            const visible = tags.slice(0, TAGS_VISIBLE);
            const hidden = tags.slice(TAGS_VISIBLE);
            tagsHtml = `<div class="detail-tags">`;
            tagsHtml += visible.map((t) => tagChip(t)).join('');
            tagsHtml += hidden.map((t) => tagChip(t, true)).join('');
            if (hidden.length > 0) {
                tagsHtml += `<button type="button" class="detail-tag detail-tags-more" data-reveal=".detail-tags-extra" aria-expanded="false" aria-label="Show ${hidden.length} more tags">+${hidden.length}</button>`;
            }
            tagsHtml += '</div>';
        }
    }

    let railHtml = '';
    if (detailsHtml || tagsHtml) {
        railHtml = `<div class="detail-rail">${detailsHtml}${tagsHtml}</div>`;
    }

    // Read + per-asset download buttons. They share the .detail-action family
    // with Shelves/Edit/⋯ below, so the whole row reads as one set; Read is the
    // single filled (primary) action.
    let assetsHtml = '';
    const primaryAsset = b.assets?.find((a: Asset) => a.is_primary) ?? b.assets?.[0];
    const primaryReadableAsset =
        primaryAsset && isReadableAsset(primaryAsset) ? primaryAsset : null;
    if (b.assets && b.assets.length > 0) {
        if (primaryReadableAsset) {
            assetsHtml += `<a href="/read/${escapeHtml(b.id)}" class="detail-action detail-action-primary">${icon('menu_book', 16)}Read</a>`;
        }
        b.assets.forEach((a: Asset) => {
            assetsHtml += assetDownloadHtml(a);
        });
        if (sendEnabled()) {
            assetsHtml += `
                <button id="btn-send-book" class="detail-action" type="button">
                    ${icon('upload', 16)}Send
                </button>
            `;
        }
    }

    const readingStatus = b.reading_status?.status ?? 'unread';
    const progressAsset = primaryReadableAsset
        ? ` data-reader-progress-asset="${escapeHtml(primaryReadableAsset.id)}"`
        : '';
    const readingStatusHtml = `
        <div class="detail-reading-state">
            <button id="btn-reading-status" class="detail-reading-status" type="button"${progressAsset} aria-label="Change reading status, currently ${escapeHtml(readingStatusLabel(readingStatus))}">
                <span class="detail-reading-status-copy">
                    <span data-reading-status-label>${escapeHtml(readingStatusLabel(readingStatus))}</span><span data-reader-progress-text hidden></span>
                </span>
                <span class="detail-reading-progress-track" data-reader-progress-track hidden><span class="detail-reading-progress-fill"></span></span>
            </button>
        </div>
    `;

    const coverHtml = coverImgHtml(b.id, b.cover_version);

    let bottomMetaHtml = '';
    const bottomParts = [];
    if (b.added_at) {
        bottomParts.push(`Added ${formatTimestampHuman(b.added_at)}`);

        if (b.updated_at) {
            const addedDate = new Date(b.added_at * 1000);
            const updatedDate = new Date(b.updated_at * 1000);
            if (!Number.isNaN(addedDate.getTime()) && !Number.isNaN(updatedDate.getTime())) {
                const diffMs = updatedDate.getTime() - addedDate.getTime();
                if (diffMs > 60000) {
                    bottomParts.push(`Edited ${formatTimestampHuman(b.updated_at)}`);
                }
            }
        }
    }

    if (b.assets && b.assets.length > 0) {
        let totalSize = 0;
        b.assets.forEach((a) => {
            if (a.size) totalSize += a.size;
        });
        if (totalSize > 0) {
            bottomParts.push(`${formatSize(totalSize)}`);
        }
    }

    if (bottomParts.length > 0) {
        bottomMetaHtml = `<div class="detail-meta detail-meta-bottom">${bottomParts.map(escapeHtml).join(' &middot; ')}</div>`;
    }

    // #book-detail-container is the .detail-layout flex row, so render its two
    // columns directly without another wrapper.
    container.innerHTML = `
        <div class="detail-layout-cover">
            ${coverHtml}
            ${railHtml}
        </div>
        <div class="detail-layout-info">
            <div class="detail-header">
                <h1 class="detail-title">${escapeHtml(b.title)}</h1>
                <h2 class="detail-authors">${authorsHtml}</h2>
                ${seriesHtml}
            </div>
            <div class="detail-actions">
                ${assetsHtml}
                <button id="btn-book-shelves" class="detail-action detail-action-icon" type="button" aria-label="Shelves" title="Shelves">
                    ${icon('bookmark', 20)}
                </button>
                ${
                    canCurate
                        ? `<button id="btn-edit-book" class="detail-action" type="button">
                            ${icon('edit', 16)}Edit
                        </button>`
                        : ''
                }
                ${
                    canCurate || primaryReadableAsset
                        ? `<button id="btn-book-menu" class="detail-action detail-action-icon" type="button" aria-label="More actions" title="More actions">
                            ${icon('more_vert', 20)}
                        </button>`
                        : ''
                }
            </div>
            ${readingStatusHtml}
            ${descHtml}
            ${bottomMetaHtml}
        </div>
    `;
    // Quiet inline disclosures (store identifiers "…", tags "+N"): reveal the
    // hidden chips in place and drop the toggle.
    container.querySelectorAll<HTMLButtonElement>('[data-reveal]').forEach((btn) => {
        btn.addEventListener('click', () => {
            const sel = btn.getAttribute('data-reveal');
            if (sel) {
                container.querySelectorAll<HTMLElement>(sel).forEach((el) => {
                    el.hidden = false;
                });
            }
            btn.remove();
        });
    });

    setupDescriptionDisclosure(container);

    if (primaryReadableAsset && opts.loadReaderProgress !== false) {
        renderBookReaderProgress(container, b, primaryReadableAsset.id);
    }

    const readingStatusBtn = container.querySelector<HTMLButtonElement>('#btn-reading-status');
    if (readingStatusBtn) {
        const statuses: ReadingStatus[] = ['unread', 'reading', 'finished', 'dropped'];
        const statusMenu = createMenu(
            readingStatusBtn,
            statuses.map((status) => ({
                label: `${readingStatusLabel(status)}${status === readingStatus ? ' ✓' : ''}`,
                disabled: status === readingStatus,
                action: () => void changeBookReadingStatus(container, b, status, opts),
            })),
        );
        cleanup.push(() => statusMenu.destroy());
    }

    container.querySelectorAll<HTMLButtonElement>('[data-download-menu-asset]').forEach((btn) => {
        const asset = b.assets?.find((a) => a.id === btn.dataset.downloadMenuAsset);
        if (!asset) return;
        const menu = createMenu(
            btn,
            assetDownloadOptions(asset).map((option) => ({
                label: option.label,
                action: () => openDownload(option.url),
            })),
        );
        cleanup.push(() => menu.destroy());
    });

    document.getElementById('btn-edit-book')?.addEventListener('click', () => {
        if (currentBookDetail) openEditModal(currentBookDetail, undefined, currentBookListContext);
    });
    document.getElementById('btn-send-book')?.addEventListener('click', () => {
        openSendBookModal(b);
    });
    const shelvesBtn = document.getElementById('btn-book-shelves');
    if (shelvesBtn) {
        const popover = createPopover(shelvesBtn, (panel, popover) =>
            renderShelfPicker(panel, popover, b.id),
        );
        cleanup.push(() => popover.destroy());
    }

    const menuBtn = document.getElementById('btn-book-menu');
    if (menuBtn) {
        const items: MenuItem[] = [];
        // Admin-only "Write metadata to file" (manual mode, writable asset): the
        // server sets `available` so the item never shows for non-admins. It stays
        // visible-but-disabled when the file is already current, keeping the
        // feature legible.
        if (b.writeback?.available) {
            items.push({
                label: b.writeback.dirty ? 'Write metadata to file' : 'Metadata file is up to date',
                disabled: !b.writeback.dirty,
                action: () => void writeBookMetadata(b),
            });
        }
        if (primaryReadableAsset) {
            items.push({
                label: 'Export highlights as HTML',
                action: () => openDownload(annotationExportUrl(primaryReadableAsset, 'html')),
            });
            items.push({
                label: 'Export highlights as Markdown',
                action: () => openDownload(annotationExportUrl(primaryReadableAsset, 'markdown')),
            });
            items.push({
                label: 'Reset reading position',
                action: () => void resetBookReaderPosition(container, primaryReadableAsset),
            });
        }
        if (canCurate) {
            items.push({
                label: 'Remove from library',
                action: () => void removeBook(b),
            });
        }
        const menu = createMenu(menuBtn, items);
        cleanup.push(() => menu.destroy());
    }

    // An admin who turns sending on or off in Settings sees the action row follow
    // immediately, without leaving the page they were looking at.
    const handleSendEnabled = () => {
        if (currentBookDetail?.id === b.id) renderBookDetail(container, b, opts);
    };
    window.addEventListener(SEND_ENABLED_EVENT, handleSendEnabled);
    cleanup.push(() => window.removeEventListener(SEND_ENABLED_EVENT, handleSendEnabled));

    const destroy = () => {
        for (const fn of cleanup.splice(0).reverse()) fn();
        if (currentDetailRenderCleanup === destroy) currentDetailRenderCleanup = null;
    };
    currentDetailRenderCleanup = destroy;
    return destroy;
}

function openSendBookModal(book: Book): void {
    const body = document.createElement('div');
    body.className = 'send-device-body';
    body.append(textEl('div', 'settings-note', 'Loading…'));

    const send = document.createElement('button');
    send.type = 'button';
    send.className = 'btn-confirm';
    send.textContent = 'Send';
    send.disabled = true;

    const cancel = document.createElement('button');
    cancel.type = 'button';
    cancel.className = 'btn-confirm-cancel';
    cancel.textContent = 'Cancel';
    cancel.setAttribute('data-modal-close', '');

    const { modal } = openModal({
        title: 'Send to device',
        body,
        bodyClass: 'settings-submodal-body',
        modalClass: 'modal-flow settings-submodal',
        actions: [cancel, send],
    });
    modal.open(send);

    let selectedPlan: DeliveryPlan | null = null;
    let preferredDeviceID = '';
    const state: SendBookModalState = {
        get preferredDeviceID() {
            return preferredDeviceID;
        },
        setPreferredDeviceID(deviceID) {
            preferredDeviceID = deviceID;
        },
        setSelectedPlan(plan) {
            selectedPlan = plan;
        },
        reloadAfterDeviceAdd(deviceID) {
            preferredDeviceID = deviceID;
            load();
        },
    };

    const load = () =>
        fetchSendOptions(book.id)
            .then((options) => {
                renderSendBookOptions(body, send, options, state);
            })
            .catch((err) => {
                body.replaceChildren(
                    textEl(
                        'div',
                        'settings-note settings-note-error',
                        errorMessage(err, 'Load send options failed'),
                    ),
                );
            });
    load();

    send.addEventListener('click', async () => {
        if (!selectedPlan) return;
        send.disabled = true;
        try {
            const job = await createDelivery({
                work_id: book.id,
                device_id: preferredDeviceID,
                asset_id: selectedPlan.asset_id,
                target: selectedPlan.target,
            });
            modal.close();
            showToast('Queued');
            pollDeliveryJob(job);
        } catch (err) {
            send.disabled = false;
            showToast(errorMessage(err, 'Send failed'), { type: 'error' });
        }
    });
}

type SendBookModalState = {
    readonly preferredDeviceID: string;
    reloadAfterDeviceAdd(deviceID: string): void;
    setPreferredDeviceID(deviceID: string): void;
    setSelectedPlan(plan: DeliveryPlan | null): void;
};

function renderSendBookOptions(
    body: HTMLElement,
    send: HTMLButtonElement,
    options: SendOptions,
    state: SendBookModalState,
): void {
    body.replaceChildren();
    send.hidden = false;
    send.disabled = true;
    state.setSelectedPlan(null);

    if (!options.configured) {
        body.append(
            textEl('div', 'settings-note', options.reason || 'Email delivery is not configured'),
        );
        return;
    }
    if (options.devices.length === 0) {
        send.hidden = true;
        renderInlineDeviceAdd(body, state.reloadAfterDeviceAdd);
        return;
    }

    const defaultOption =
        options.devices.find((option) => option.device.id === state.preferredDeviceID) ||
        options.devices.find((option) => option.device.is_default) ||
        options.devices[0] ||
        null;
    if (!defaultOption) return;

    const deviceSelect = document.createElement('select');
    deviceSelect.className = 'settings-input';
    for (const option of options.devices) {
        const item = document.createElement('option');
        item.value = option.device.id;
        item.textContent = `${option.device.name} · ${presetLabel(option.device.preset)}`;
        item.selected = option.device.id === defaultOption.device.id;
        deviceSelect.append(item);
    }
    const deviceField = formField('Device', deviceSelect);

    const planArea = document.createElement('div');
    planArea.className = 'send-device-plan-area';

    const addDevice = document.createElement('button');
    addDevice.type = 'button';
    addDevice.className = 'settings-btn';
    addDevice.textContent = 'Add device';
    addDevice.addEventListener('click', () => {
        send.hidden = true;
        send.disabled = true;
        body.replaceChildren();
        renderInlineDeviceAdd(body, state.reloadAfterDeviceAdd);
    });

    const actions = document.createElement('div');
    actions.className = 'send-device-inline-actions';
    actions.append(addDevice);

    body.append(deviceField, planArea, actions);

    const syncPlan = () => {
        planArea.replaceChildren();
        const option =
            options.devices.find((item) => item.device.id === deviceSelect.value) || null;
        state.setPreferredDeviceID(option?.device.id || '');
        const choices = option?.choices || [];
        if (!option || choices.length === 0) {
            send.disabled = true;
            state.setSelectedPlan(null);
            planArea.append(
                textEl(
                    'div',
                    'settings-note',
                    option?.reason?.message || 'No sendable format for this device.',
                ),
            );
            return;
        }

        const note = textEl('div', 'settings-note send-device-plan', '');
        const selectChoice = (choiceID: string) => {
            const choice = choices.find((item) => planChoiceID(item) === choiceID) || choices[0];
            state.setSelectedPlan(choice);
            note.textContent = planDetails(choice);
        };

        if (choices.length > 1) {
            const fileSelect = document.createElement('select');
            fileSelect.className = 'settings-input';
            for (const choice of choices) {
                const item = document.createElement('option');
                item.value = planChoiceID(choice);
                item.textContent = planLabel(choice);
                fileSelect.append(item);
            }
            fileSelect.addEventListener('change', () => selectChoice(fileSelect.value));
            planArea.append(formField('File', fileSelect));
            selectChoice(fileSelect.value);
        } else {
            selectChoice(planChoiceID(choices[0]));
        }
        planArea.append(note);
        send.disabled = false;
    };

    deviceSelect.addEventListener('change', syncPlan);
    syncPlan();
    deviceSelect.focus();
}

function renderInlineDeviceAdd(
    body: HTMLElement,
    reloadAfterDeviceAdd: (deviceID: string) => void,
): void {
    const form = document.createElement('form');
    form.className = 'settings-submodal-fields';

    const name = document.createElement('input');
    name.type = 'text';
    name.autocomplete = 'off';
    name.required = true;
    name.className = 'settings-input';

    const email = document.createElement('input');
    email.type = 'email';
    email.autocomplete = 'email';
    email.required = true;
    email.className = 'settings-input';

    const preset = document.createElement('select');
    preset.className = 'settings-input';
    for (const item of [
        { value: 'kindle', label: 'Kindle' },
        { value: 'pocketbook', label: 'PocketBook' },
        { value: 'generic', label: 'Generic email' },
    ]) {
        const option = document.createElement('option');
        option.value = item.value;
        option.textContent = item.label;
        preset.append(option);
    }

    let presetTouched = false;
    preset.addEventListener('change', () => {
        presetTouched = true;
    });
    email.addEventListener('input', () => {
        if (!presetTouched) preset.value = suggestedPresetForEmail(email.value);
    });

    const submit = document.createElement('button');
    submit.type = 'submit';
    submit.className = 'settings-btn settings-primary-btn';
    submit.textContent = 'Add device';

    form.append(
        textEl('div', 'settings-submodal-hint', 'Add a reader email address, then send this book.'),
        formField('Name', name),
        formField('Email', email),
        formField('Preset', preset),
        submit,
    );
    body.append(form);

    form.addEventListener('submit', async (event) => {
        event.preventDefault();
        submit.disabled = true;
        try {
            const device = await createDeliveryDevice({
                name: name.value.trim(),
                email: email.value.trim(),
                preset: preset.value as DeliveryPreset,
            });
            showToast(`Added ${device.name}`);
            reloadAfterDeviceAdd(device.id);
        } catch (err) {
            showToast(errorMessage(err, 'Add device failed'), { type: 'error' });
            submit.disabled = false;
        }
    });

    name.focus();
}

function suggestedPresetForEmail(email: string): DeliveryPreset {
    const value = email.trim().toLowerCase();
    if (
        value.endsWith('@kindle.com') ||
        value.endsWith('@free.kindle.com') ||
        value.endsWith('@kindle.cn') ||
        value.endsWith('@free.kindle.cn')
    ) {
        return 'kindle';
    }
    if (value.endsWith('@pbsync.com')) return 'pocketbook';
    return 'generic';
}

function planChoiceID(plan: DeliveryPlan): string {
    return `${plan.asset_id || ''}:${plan.target || ''}`;
}

function presetLabel(preset: DeliveryPreset): string {
    if (preset === 'kindle') return 'Kindle';
    if (preset === 'pocketbook') return 'PocketBook';
    return 'Generic';
}

function planDetails(plan: DeliveryPlan): string {
    const parts = [planLabel(plan)];
    if (plan.size_estimate) parts.push(formatSize(plan.size_estimate));
    if (plan.filename) parts.push(plan.filename);
    return parts.join(' · ');
}

function planLabel(plan?: DeliveryPlan): string {
    if (!plan) return 'Not sendable';
    const label = (plan.target || plan.format || 'file').toUpperCase();
    if (plan.converted && plan.format) {
        return `${label} from ${plan.format.toUpperCase()}`;
    }
    return label;
}

function pollDeliveryJob(initial: DeliveryJob): void {
    const tick = async (job: DeliveryJob, remaining: number) => {
        if (job.status === 'sent') {
            showToast('Sent to mail server');
            return;
        }
        if (job.status === 'failed') {
            showToast(job.error || 'Send failed', { type: 'error' });
            return;
        }
        if (remaining <= 0) return;
        window.setTimeout(async () => {
            try {
                await tick(await fetchDeliveryJob(job.id), remaining - 1);
            } catch {
                // The queued toast already told the user the action started; a
                // transient polling failure should not become a second error.
            }
        }, 1600);
    };
    void tick(initial, 20);
}

function renderBookReaderProgress(container: HTMLElement, book: Book, assetID: string): void {
    const progressEl = container.querySelector<HTMLElement>('[data-reader-progress-asset]');
    if (!progressEl || progressEl.dataset.readerProgressAsset !== assetID) return;

    fetchReaderState(assetID)
        .then((options) => {
            const current = container.querySelector<HTMLElement>('[data-reader-progress-asset]');
            if (current !== progressEl) return;
            if (options.reading_status) book.reading_status = options.reading_status;
            updateBookReaderProgress(current, options);
        })
        .catch(() => {
            const track = progressEl.querySelector<HTMLElement>('[data-reader-progress-track]');
            if (track) track.hidden = true;
        });
}

function updateBookReaderProgress(progressEl: HTMLElement, state: ReaderState): void {
    const progress = clampProgress(state.progress);
    const hasState = progress > 0 || !!state.last_read_at;
    const label = progressEl.querySelector<HTMLElement>('[data-reading-status-label]');
    const status = state.reading_status?.status;
    if (label && status) {
        label.textContent = readingStatusLabel(status);
        progressEl.setAttribute(
            'aria-label',
            `Change reading status, currently ${readingStatusLabel(status)}`,
        );
    }
    const text = progressEl.querySelector<HTMLElement>('[data-reader-progress-text]');
    const track = progressEl.querySelector<HTMLElement>('[data-reader-progress-track]');
    const showsProgress = status === 'reading' || status === 'dropped';
    if (!hasState || !showsProgress) {
        if (text) text.hidden = true;
        if (track) track.hidden = true;
        return;
    }

    const fill = progressEl.querySelector<HTMLElement>('.detail-reading-progress-fill');
    if (!fill) return;

    const percent = Math.round(progress * 100);
    if (text) {
        text.textContent = ` · ${percent}%`;
        text.hidden = false;
    }
    if (progress >= 0.995) {
        fill.style.width = '100%';
    } else if (percent > 0) {
        fill.style.width = `${Math.max(3, percent)}%`;
    } else {
        fill.style.width = '0%';
    }
    if (track) track.hidden = false;
}

function readingStatusLabel(status: ReadingStatus): string {
    switch (status) {
        case 'reading':
            return 'Reading';
        case 'finished':
            return 'Finished';
        case 'dropped':
            return 'Dropped';
        default:
            return 'Unread';
    }
}

async function changeBookReadingStatus(
    container: HTMLElement,
    book: Book,
    status: ReadingStatus,
    opts: { loadReaderProgress?: boolean; canCurate?: boolean },
): Promise<void> {
    try {
        book.reading_status = await setReadingStatus(book.id, status);
        showToast(`Marked as ${readingStatusLabel(status).toLowerCase()}`);
        renderBookDetail(container, book, opts);
    } catch (err) {
        showToast(errorMessage(err, 'Failed to change reading status'), { type: 'error' });
    }
}

async function resetBookReaderPosition(container: HTMLElement, asset: Asset): Promise<void> {
    const format = assetFormatLabel(asset);
    const confirmed = await confirmModal({
        title: 'Reset reading position?',
        body: `This clears your saved position for the ${format} file and removes the book from Continue reading. Highlights and notes are kept.`,
        confirmLabel: 'Reset',
    });
    if (!confirmed) return;

    try {
        await resetReaderState(asset.id);
        const progressEl = container.querySelector<HTMLElement>(
            `[data-reader-progress-asset="${CSS.escape(asset.id)}"]`,
        );
        if (progressEl) {
            const text = progressEl.querySelector<HTMLElement>('[data-reader-progress-text]');
            if (text) text.hidden = true;
            const track = progressEl.querySelector<HTMLElement>('[data-reader-progress-track]');
            if (track) track.hidden = true;
            const fill = progressEl.querySelector<HTMLElement>('.detail-reading-progress-fill');
            if (fill) fill.style.width = '0%';
        }
        showToast('Reading position reset');
    } catch (err) {
        showToast(errorMessage(err, 'Failed to reset reading position'), { type: 'error' });
    }
}

function clampProgress(progress: number): number {
    if (!Number.isFinite(progress)) return 0;
    return Math.max(0, Math.min(1, progress));
}

async function writeBookMetadata(b: Book): Promise<void> {
    const detailToken = bookDetailLoadToken;
    try {
        const result = await writebackBook(b.id);
        if (result.failed > 0) {
            showToast(result.errors?.[0] ?? `Failed to write ${result.failed} file(s)`, {
                type: 'error',
            });
        } else if (result.written === 0) {
            showToast('Book files are already up to date');
        } else {
            const n = result.written;
            showToast(`Metadata written to ${n} ${n === 1 ? 'file' : 'files'}`);
        }
        // Re-render from the refreshed book so the action reflects the new (clean)
        // state. The write may outlive this detail mount; a later book page owns
        // the same global state/container and must not be replaced by this result.
        // Only an admin can reach this action, so canCurate holds.
        if (detailToken !== bookDetailLoadToken || currentBookDetail?.id !== b.id) return;
        currentBookDetail = result.book;
        const container = document.getElementById('book-detail-container');
        if (container) renderBookDetail(container, result.book, { canCurate: true });
    } catch (e) {
        showToast(errorMessage(e, 'Failed to write metadata to file'), { type: 'error' });
    }
}

// Soft-delete: the book moves to Trash (reversible, files kept) and we return to
// the library, where it no longer appears.
async function removeBook(b: Book): Promise<void> {
    const ok = await confirmModal({
        title: 'Remove from library?',
        body: `“${b.title}” moves to Trash. You can restore it later; the files are kept until it is permanently deleted.`,
        confirmLabel: 'Remove',
        danger: true,
    });
    if (!ok) return;
    try {
        await deleteBook(b.id);
        window.location.href = '/';
    } catch (e) {
        console.error('Failed to remove book:', e);
        showToast(errorMessage(e, 'Failed to remove book'), { type: 'error' });
    }
}
