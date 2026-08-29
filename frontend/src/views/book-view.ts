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
    bookURL,
    listURLForContext,
    readBookListContextFromLocation,
} from '../book-list-context';
import { SEND_ENABLED_EVENT, sendEnabled } from '../bootstrap';
import { notifyCatalogChanged } from '../catalog-events';
import { coverImgHtml } from '../cover';
import { escapeHtml, formField, textEl } from '../dom';
import { errorMessage } from '../errors';
import { isLibraryPath, readPredecessorURL } from '../history-state';
import { icon } from '../icons';
import {
    type Identifier,
    identifierLabel,
    identifierLink,
    isPrimaryIdentifier,
    parseIdentifiers,
} from '../identifiers';
import { beginGlobalLoading } from '../loading-indicator';
import { createMenu, type MenuItem } from '../menu';
import { confirmModal, openModal } from '../modal';
import { createPopover } from '../popover';
import {
    navigateApp,
    type RouteCleanup,
    type RouteController,
    type RouteMountContext,
    replaceLocationURL,
} from '../router';
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
import { type BookDetailHost, registerActiveBookDetailHost } from './book-detail-host';
import { openEditModal } from './book-edit';
import { renderShelfPicker } from './book-shelf-picker';

// Re-exported so existing importers (library/table view) keep pulling the edit
// modal from the book-view facade.
export { openEditModal };

// One mounted book page. The book it shows, the list it was opened from, the
// request that fills it, and the cleanup for whatever is currently rendered all
// belong to this instance. Two of them exist for a moment during a navigation,
// and neither may write over the other's page.
interface BookDetailView {
    root: HTMLElement;
    phase: 'active' | 'destroyed';
    book: Book | null;
    listContext: BookListContext | null;
    abort: AbortController;
    renderCleanup: RouteCleanup | null;
    // Whether this page has to place focus itself; see the heading below.
    takeFocus: boolean;
}

// What the edit dialog is given so it can keep the page behind it in step. It
// is handed out by the page that opened the dialog, so an editor opened from
// the library — where there is no book page — simply has none. Telling the rest
// of the app about the change is not this: that goes through CATALOG_CHANGED.
function hostFor(view: BookDetailView): BookDetailHost {
    const container = () => view.root.querySelector<HTMLElement>('#book-detail-container');
    // While the editor is open this updates its entry; rerender after dismissal
    // updates the underlying page entry.
    const syncLocation = () => {
        if (view.book && view.listContext) {
            replaceLocationURL(bookURL(view.book.id, view.listContext));
        }
    };
    return {
        applySaved(b: Book): void {
            if (view.phase !== 'active' || view.book?.id !== b.id) return;
            view.book = b;
        },
        showBook(b: Book, listContext?: BookListContext | null): void {
            const host = container();
            if (view.phase !== 'active' || !host || view.book?.id === b.id) return;
            view.book = b;
            view.listContext = listContext ?? view.listContext;
            renderBookDetail(view, host, b, { loadReaderProgress: false });
            document.title = `${b.title} - polka`;
            syncLocation();
        },
        rerender(): void {
            const host = container();
            if (view.phase !== 'active' || !host || !view.book) return;
            renderBookDetail(view, host, view.book);
            syncLocation();
        },
    };
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

// The controller is handed back before the book has arrived: content readiness
// is view state, not route ownership. Whoever navigates away next can therefore
// cancel this page's request instead of racing its render.
export function initBookDetail(
    workId: string,
    root: HTMLElement,
    context: RouteMountContext,
): RouteController {
    const view: BookDetailView = {
        root,
        phase: 'active',
        book: null,
        listContext: readBookListContextFromLocation(),
        abort: new AbortController(),
        renderCleanup: null,
        takeFocus: context.clientNavigation,
    };
    const releaseEditorHost = registerActiveBookDetailHost(hostFor(view));
    void loadBookDetail(view, workId);
    return {
        destroy(): void {
            releaseEditorHost();
            view.phase = 'destroyed';
            view.abort.abort();
            view.renderCleanup?.();
            view.renderCleanup = null;
            view.book = null;
        },
    };
}

async function loadBookDetail(view: BookDetailView, workId: string): Promise<void> {
    const container = view.root.querySelector<HTMLElement>('#book-detail-container');
    if (!container) return;
    // The page marks itself busy for its own data, the way the library does.
    // The router stops waiting for content once it holds this controller, so
    // nothing else is left to say the page has not filled in yet.
    const finishGlobalLoading = beginGlobalLoading();
    try {
        const [b, me] = await Promise.all([
            fetchBook(workId, view.abort.signal),
            fetchCurrentUser(),
        ]);
        if (view.phase !== 'active') return;

        view.book = b;
        updateBackLink(view.root, view.listContext);
        container.classList.remove('book-detail-loading');
        container.removeAttribute('aria-busy');
        renderBookDetail(view, container, b, {
            canCurate: me.role === 'admin' || me.role === 'member',
        });

        document.title = `${b.title} - polka`;
        // Only when the app swapped this page in under the reader, where a
        // keyboard reader would otherwise be left on a link that no longer
        // exists. After a reload it would only paint a ring nobody asked for.
        const heading = view.takeFocus
            ? container.querySelector<HTMLElement>('.detail-title')
            : null;
        if (heading) {
            heading.tabIndex = -1;
            heading.focus({ preventScroll: true });
        }
    } catch (e) {
        if (view.phase !== 'active') return;
        console.error('Failed to load book detail:', e);
        container.classList.remove('book-detail-loading');
        container.removeAttribute('aria-busy');
        container.innerHTML = '<h2>Book not found.</h2>';
    } finally {
        finishGlobalLoading();
    }
}

// The control points at the entry this page was actually opened from, so it and
// browser Back make the same one-entry move. Without a known predecessor — a
// direct link, a new tab — it falls back to the list the URL describes.
function updateBackLink(root: HTMLElement, context: BookListContext | null): void {
    const link = root.querySelector<HTMLAnchorElement>('.back-link a');
    if (!link) return;
    const predecessor = readPredecessorURL(window.history.state);
    link.href = predecessor ?? listURLForContext(context);
    const target = new URL(link.href, window.location.origin);
    const label = isLibraryPath(target.pathname) ? 'Back to Library' : 'Back';
    link.title = label;
    // At strip widths the control is an icon, so its name cannot come from the
    // text beside it. It says where the reader lands, not what the icon draws.
    link.setAttribute('aria-label', label);
}

// The clamp and the hidden toggle ship in the markup, so a long blurb is never
// briefly full height. This decides, once the text has been laid out, whether
// the blurb earns a toggle or should simply be shown whole.
// How tall a blurb may stand. Both numbers are line-heights rather than lines
// of text, because a blurb that arrives as several <p> blocks spends real
// height on the gaps between them and stands taller than the same count of
// plain prose lines — which is what made the collapsed block look inconsistent
// from book to book. A blurb no taller than the slack is shown whole — hiding a
// line or two behind a click buys nothing — and anything longer is cut to the
// cap.
const COLLAPSED_DESCRIPTION_LINES = 13;
const WHOLE_DESCRIPTION_LINES = 15;
// However the gaps fall, a collapsed blurb still has to say something.
const COLLAPSED_DESCRIPTION_MIN_LINES = 6;

function setupDescriptionDisclosure(container: HTMLElement): void {
    const description = container.querySelector<HTMLElement>('.detail-description');
    const more = container.querySelector<HTMLButtonElement>('.detail-description-more');
    if (!description || !more) return;

    // The clamp ships in the markup, so a long blurb is never briefly full
    // height. Under it, scrollHeight is what the whole blurb would take.
    const lineHeight = parseFloat(getComputedStyle(description).lineHeight);
    if (!Number.isFinite(lineHeight) || lineHeight <= 0) return;
    if (description.scrollHeight <= lineHeight * WHOLE_DESCRIPTION_LINES) {
        description.classList.remove('detail-description--collapsed');
        more.remove();
        return;
    }

    // The clamp is what cuts between lines and supplies the ellipsis, so the
    // cap is applied by asking it for fewer lines rather than by cropping the
    // box. Only block-spaced blurbs give back any lines, and they stop after a
    // step or two, so this costs a couple of reflows on a box already laid out.
    const budget = lineHeight * COLLAPSED_DESCRIPTION_LINES;
    let lines = parseInt(getComputedStyle(description).webkitLineClamp, 10);
    while (
        Number.isFinite(lines) &&
        lines > COLLAPSED_DESCRIPTION_MIN_LINES &&
        description.clientHeight > budget
    ) {
        lines -= 1;
        description.style.setProperty('-webkit-line-clamp', String(lines));
    }

    more.hidden = false;
    more.addEventListener('click', () => {
        description.classList.remove('detail-description--collapsed');
        description.style.removeProperty('-webkit-line-clamp');
        more.remove();
    });
}

function renderBookDetail(
    view: BookDetailView,
    container: HTMLElement,
    b: Book,
    opts: { loadReaderProgress?: boolean; canCurate?: boolean } = {},
): RouteCleanup {
    view.renderCleanup?.();
    view.renderCleanup = null;
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
                action: () => void changeBookReadingStatus(view, container, b, status, opts),
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

    container.querySelector('#btn-edit-book')?.addEventListener('click', () => {
        if (view.book) openEditModal(view.book, view.listContext, null, hostFor(view));
    });
    container.querySelector('#btn-send-book')?.addEventListener('click', () => {
        openSendBookModal(b);
    });
    const shelvesBtn = container.querySelector<HTMLElement>('#btn-book-shelves');
    if (shelvesBtn) {
        const popover = createPopover(shelvesBtn, (panel, popover) =>
            renderShelfPicker(panel, popover, b.id),
        );
        cleanup.push(() => popover.destroy());
    }

    const menuBtn = container.querySelector<HTMLElement>('#btn-book-menu');
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
                action: () => void writeBookMetadata(view, container, b),
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
        if (view.book?.id === b.id) renderBookDetail(view, container, b, opts);
    };
    window.addEventListener(SEND_ENABLED_EVENT, handleSendEnabled);
    cleanup.push(() => window.removeEventListener(SEND_ENABLED_EVENT, handleSendEnabled));

    const destroy = () => {
        for (const fn of cleanup.splice(0).reverse()) fn();
        if (view.renderCleanup === destroy) view.renderCleanup = null;
    };
    view.renderCleanup = destroy;
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
    view: BookDetailView,
    container: HTMLElement,
    book: Book,
    status: ReadingStatus,
    opts: { loadReaderProgress?: boolean; canCurate?: boolean },
): Promise<void> {
    try {
        book.reading_status = await setReadingStatus(book.id, status);
        showToast(`Marked as ${readingStatusLabel(status).toLowerCase()}`);
        if (view.phase !== 'active') return;
        renderBookDetail(view, container, book, opts);
        notifyCatalogChanged({ kind: 'books-updated', books: [book] });
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
        notifyCatalogChanged();
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

async function writeBookMetadata(
    view: BookDetailView,
    container: HTMLElement,
    b: Book,
): Promise<void> {
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
        // state. The write can outlive this page; the instance decides whether
        // the result still belongs to what is on screen. Only an admin can reach
        // this action, so canCurate holds.
        if (view.phase !== 'active' || view.book?.id !== b.id) return;
        view.book = result.book;
        renderBookDetail(view, container, result.book, { canCurate: true });
    } catch (e) {
        showToast(errorMessage(e, 'Failed to write metadata to file'), { type: 'error' });
    }
}

// Soft-delete: the book moves to Trash (reversible, files kept) and we return to
// the library, where it no longer appears. The removal is reported first, so a
// library that is being held for this page has already dropped the book by the
// time it is shown again — and Back returns through that instance rather than
// reloading the document out from under it.
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
        notifyCatalogChanged({ kind: 'books-removed', ids: [b.id] });
        if (readPredecessorURL(window.history.state)) window.history.back();
        else navigateApp('/');
    } catch (e) {
        console.error('Failed to remove book:', e);
        showToast(errorMessage(e, 'Failed to remove book'), { type: 'error' });
    }
}
