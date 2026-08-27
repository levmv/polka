import { fetchMetadataCandidates, fetchMetadataDescription } from '../api';
import { createSelect } from '../components/select';
import { escapeHtml } from '../dom';
import { errorMessage } from '../errors';
import { formatIdentifiers, parseIdentifiers } from '../identifiers';
import { openModal } from '../modal';
import type { Book, BookUpdate, MetadataCandidate } from '../types';

type MetadataFieldKey =
    | 'title'
    | 'authors'
    | 'series'
    | 'series_index'
    | 'description'
    | 'tags'
    | 'language'
    | 'publisher'
    | 'date'
    | 'identifiers';

type MetadataField = {
    key: MetadataFieldKey;
    label: string;
    value: (candidate: MetadataCandidate) => string | number | null | undefined;
};

type CandidateImpact = {
    missingFields: number;
    replaceFields: number;
    skippedDirtyFields: number;
    addCover: boolean;
    replaceCover: boolean;
    skippedCover: boolean;
};

export type MetadataDraftApply = {
    patch: Partial<BookUpdate>;
    providerName: string;
    fields: MetadataFieldKey[];
    coverUrl?: string;
};

type RegisterAbortController = (controller: AbortController) => () => void;

const metadataFields: MetadataField[] = [
    { key: 'title', label: 'Title', value: (candidate) => candidate.title },
    { key: 'authors', label: 'Authors', value: (candidate) => candidate.authors },
    { key: 'series', label: 'Series', value: (candidate) => candidate.series },
    {
        key: 'series_index',
        label: 'Series index',
        value: (candidate) => candidate.series_index,
    },
    { key: 'description', label: 'Description', value: (candidate) => candidate.description },
    { key: 'tags', label: 'Tags', value: (candidate) => candidate.tags },
    { key: 'language', label: 'Language', value: (candidate) => candidate.language },
    { key: 'publisher', label: 'Publisher', value: (candidate) => candidate.publisher },
    { key: 'date', label: 'Published', value: (candidate) => candidate.date },
    { key: 'identifiers', label: 'Identifiers', value: (candidate) => candidate.identifiers },
];

const descriptionLookups = new WeakSet<MetadataCandidate>();

export function openMetadataCandidatesModal(
    b: Book,
    draft: BookUpdate,
    saved: BookUpdate,
    coverDraftURL: string | null,
    onApply: (apply: MetadataDraftApply) => void,
) {
    let providerSelect: ReturnType<typeof createSelect> | null = null;
    let closed = false;
    let candidateRequestID = 0;
    let candidateController: AbortController | null = null;
    const applyControllers = new Set<AbortController>();
    const isClosed = () => closed;
    const cancelCandidateFetch = () => {
        candidateController?.abort();
        candidateController = null;
        candidateRequestID += 1;
    };
    const registerController: RegisterAbortController = (controller) => {
        applyControllers.add(controller);
        return () => {
            applyControllers.delete(controller);
        };
    };
    const { modal, root } = openModal({
        title: 'Fetch metadata',
        body: `
            <div class="metadata-provider-row">
                <span class="metadata-provider-label">Provider</span>
                <div id="metadata-provider-control-${b.id}" class="metadata-provider-control"></div>
                <button type="button" id="metadata-run-fetch-${b.id}" class="metadata-fetch-action">Fetch</button>
            </div>
            <div id="metadata-status-${b.id}" class="metadata-status"></div>
            <div id="metadata-candidates-${b.id}" class="metadata-candidates"></div>
        `,
        backdropClass: 'modal-wide',
        modalClass: 'metadata-modal',
        onClose: () => {
            closed = true;
            cancelCandidateFetch();
            for (const controller of applyControllers) {
                controller.abort();
            }
            applyControllers.clear();
            providerSelect?.destroy();
        },
    });
    const providerControl = root.querySelector(`#metadata-provider-control-${b.id}`) as HTMLElement;
    const fetchBtn = root.querySelector(`#metadata-run-fetch-${b.id}`) as HTMLButtonElement;
    const status = root.querySelector(`#metadata-status-${b.id}`) as HTMLElement;
    const list = root.querySelector(`#metadata-candidates-${b.id}`) as HTMLElement;
    const resetCandidates = () => {
        cancelCandidateFetch();
        fetchBtn.disabled = false;
        list.replaceChildren();
        status.textContent = 'Choose a provider, then fetch candidates.';
        status.classList.remove('error');
    };

    providerSelect = createSelect({
        ariaLabel: 'Metadata provider',
        className: 'metadata-provider-select',
        options: [
            { value: 'openlibrary', label: 'Open Library' },
            { value: 'google', label: 'Google Books' },
        ],
        value: 'openlibrary',
        onChange: resetCandidates,
    });
    providerControl.appendChild(providerSelect.el);

    const load = async () => {
        if (closed) return;
        cancelCandidateFetch();
        const requestID = candidateRequestID;
        const controller = new AbortController();
        candidateController = controller;
        fetchBtn.disabled = true;
        status.textContent = 'Loading candidates...';
        status.classList.remove('error');
        list.replaceChildren();
        try {
            const candidates = await fetchMetadataCandidates(
                b.id,
                providerSelect?.getValue(),
                controller.signal,
            );
            if (closed || controller.signal.aborted || requestID !== candidateRequestID) return;
            renderMetadataCandidateList({
                book: b,
                draft,
                saved,
                coverDraftURL,
                modal,
                status,
                list,
                candidates,
                onApply,
                isClosed,
                registerController,
            });
        } catch (err) {
            if (isAbortError(err) || closed || requestID !== candidateRequestID) return;
            status.textContent = errorMessage(err, 'Failed to fetch metadata');
            status.classList.add('error');
        } finally {
            if (candidateController === controller) {
                candidateController = null;
            }
            if (!closed && requestID === candidateRequestID) {
                fetchBtn.disabled = false;
            }
        }
    };

    resetCandidates();
    fetchBtn.addEventListener('click', () => {
        void load();
    });
    modal.open(providerSelect.el);
}

function renderMetadataCandidateList(args: {
    book: Book;
    draft: BookUpdate;
    saved: BookUpdate;
    coverDraftURL: string | null;
    modal: { close(): void };
    status: HTMLElement;
    list: HTMLElement;
    candidates: MetadataCandidate[];
    onApply: (apply: MetadataDraftApply) => void;
    isClosed: () => boolean;
    registerController: RegisterAbortController;
}) {
    const {
        book,
        draft,
        saved,
        coverDraftURL,
        modal,
        status,
        list,
        candidates,
        onApply,
        isClosed,
        registerController,
    } = args;
    list.replaceChildren();
    status.classList.remove('error');
    if (candidates.length === 0) {
        status.textContent = 'No candidates found.';
        return;
    }
    status.textContent = `${candidates.length} ${candidates.length === 1 ? 'candidate' : 'candidates'} found.`;

    for (const candidate of candidates) {
        const impact = analyzeCandidate(candidate, book, draft, saved, coverDraftURL);
        const item = document.createElement('div');
        item.className = 'metadata-candidate';

        const cover = candidate.cover_url
            ? `<img src="${escapeHtml(candidate.cover_url)}" class="metadata-candidate-cover" alt="">`
            : '<div class="metadata-candidate-cover"></div>';
        const authors = candidate.authors ? `<div>${escapeHtml(candidate.authors)}</div>` : '';
        const facts = [candidate.publisher, candidate.date].filter(Boolean).join(' · ');
        const tags = candidate.tags
            ? `<div class="metadata-candidate-tags">${escapeHtml(candidate.tags)}</div>`
            : '';
        const fields = metadataCandidateFields(candidate);
        const fieldBadges =
            fields.length > 0
                ? `<div class="metadata-candidate-fields">${fields
                      .map((field) => `<span>${escapeHtml(field)}</span>`)
                      .join('')}</div>`
                : '';

        item.innerHTML = `
            ${cover}
            <div class="metadata-candidate-main">
                <div class="metadata-candidate-title">${escapeHtml(candidate.title || 'Untitled')}</div>
                ${authors}
                ${facts ? `<div>${escapeHtml(facts)}</div>` : ''}
                ${tags}
                <div class="metadata-candidate-provider">${escapeHtml(candidate.provider_name)}</div>
                ${fieldBadges}
                <div class="metadata-candidate-impact">${candidateImpactText(impact)}</div>
            </div>
            <div class="metadata-candidate-actions">
                ${
                    candidateHasMissingChanges(impact)
                        ? `<button type="button" class="btn-upload-cover metadata-fill-btn">${missingButtonLabel(impact)}</button>`
                        : ''
                }
                ${
                    candidateHasReplacementChanges(impact)
                        ? `<button type="button" class="btn-upload-cover metadata-replace-btn">${replaceButtonLabel(impact)}</button>`
                        : ''
                }
                ${
                    !candidateHasMissingChanges(impact) && !candidateHasReplacementChanges(impact)
                        ? '<button type="button" class="btn-upload-cover metadata-noop-btn" disabled>No changes</button>'
                        : ''
                }
            </div>
        `;
        item.querySelector<HTMLButtonElement>('.metadata-fill-btn')?.addEventListener(
            'click',
            (event) => {
                void applyCandidate({
                    candidate,
                    draft,
                    saved,
                    book,
                    coverDraftURL,
                    mode: 'missing',
                    trigger: event.currentTarget as HTMLButtonElement,
                    status,
                    modal,
                    onApply,
                    isClosed,
                    registerController,
                });
            },
        );
        item.querySelector<HTMLButtonElement>('.metadata-replace-btn')?.addEventListener(
            'click',
            (event) => {
                void applyCandidate({
                    candidate,
                    draft,
                    saved,
                    book,
                    coverDraftURL,
                    mode: 'replace',
                    trigger: event.currentTarget as HTMLButtonElement,
                    status,
                    modal,
                    onApply,
                    isClosed,
                    registerController,
                });
            },
        );
        list.appendChild(item);
    }
}

async function applyCandidate(args: {
    candidate: MetadataCandidate;
    draft: BookUpdate;
    saved: BookUpdate;
    book: Book;
    coverDraftURL: string | null;
    mode: 'missing' | 'replace';
    trigger: HTMLButtonElement;
    status: HTMLElement;
    modal: { close(): void };
    onApply: (apply: MetadataDraftApply) => void;
    isClosed: () => boolean;
    registerController: RegisterAbortController;
}) {
    const {
        candidate,
        draft,
        saved,
        book,
        coverDraftURL,
        mode,
        trigger,
        status,
        modal,
        onApply,
        isClosed,
        registerController,
    } = args;
    const previousText = trigger.textContent || '';
    const controller = new AbortController();
    const unregisterController = registerController(controller);
    trigger.disabled = true;
    trigger.textContent = 'Applying...';
    status.classList.remove('error');
    try {
        await ensureCandidateDescription(candidate, status, controller.signal);
        if (isClosed() || controller.signal.aborted) return;
        const patch = candidatePatch(candidate, book, draft, saved, coverDraftURL, mode);
        if (Object.keys(patch.patch).length === 0 && !patch.coverUrl) {
            status.textContent =
                mode === 'missing'
                    ? 'This candidate has no missing fields or cover to fill.'
                    : 'This candidate has no fields or cover that can be applied.';
            status.classList.add('error');
            return;
        }
        onApply({
            patch: patch.patch,
            providerName: candidate.provider_name,
            fields: patch.fields,
            coverUrl: patch.coverUrl,
        });
        modal.close();
    } catch (err) {
        if (isAbortError(err) || isClosed()) return;
        status.textContent = errorMessage(err, 'Failed to apply metadata');
        status.classList.add('error');
    } finally {
        unregisterController();
        if (!isClosed()) {
            trigger.disabled = false;
            trigger.textContent = previousText;
        }
    }
}

async function ensureCandidateDescription(
    candidate: MetadataCandidate,
    status: HTMLElement,
    signal?: AbortSignal,
) {
    if (candidate.description || !candidate.provider_id || descriptionLookups.has(candidate))
        return;
    status.textContent = 'Loading description...';
    try {
        candidate.description = await fetchMetadataDescription(
            candidate.provider,
            candidate.provider_id,
            signal,
        );
        descriptionLookups.add(candidate);
    } catch (e) {
        if (isAbortError(e)) throw e;
        /* applying proceeds without an out-of-band description */
    } finally {
        if (!signal?.aborted) {
            status.textContent = '';
        }
    }
}

function isAbortError(e: unknown): boolean {
    return e instanceof DOMException
        ? e.name === 'AbortError'
        : typeof e === 'object' && e !== null && (e as { name?: string }).name === 'AbortError';
}

function analyzeCandidate(
    candidate: MetadataCandidate,
    book: Book,
    draft: BookUpdate,
    saved: BookUpdate,
    coverDraftURL: string | null,
) {
    const impact: CandidateImpact = {
        missingFields: 0,
        replaceFields: 0,
        skippedDirtyFields: 0,
        addCover: false,
        replaceCover: false,
        skippedCover: false,
    };
    if (candidate.cover_url) {
        if (coverDraftURL) {
            impact.skippedCover = !sameMetadataValue(coverDraftURL, candidate.cover_url);
        } else if (book.has_cover) {
            impact.replaceCover = true;
        } else {
            impact.addCover = true;
        }
    }
    for (const field of metadataFields) {
        const proposed = candidateFieldValue(field, candidate, draft);
        const deferredDescription =
            field.key === 'description' && hasDeferredDescription(candidate);
        if (isEmptyMetadataValue(field.key, proposed) && !deferredDescription) continue;
        const current = draft[field.key];
        if (!deferredDescription && sameMetadataValue(current, proposed)) continue;
        if (fieldIsDirty(field.key, draft, saved)) {
            impact.skippedDirtyFields += 1;
            continue;
        }
        if (field.key === 'identifiers') {
            // Candidate identifiers are additive provenance. Existing ISBNs and
            // provider references are retained rather than counted as fields
            // the candidate would replace.
            impact.missingFields += 1;
            continue;
        }
        if (isEmptyMetadataValue(field.key, current)) {
            impact.missingFields += 1;
        } else {
            impact.replaceFields += 1;
        }
    }
    return impact;
}

function candidateHasMissingChanges(impact: CandidateImpact): boolean {
    return impact.missingFields > 0 || impact.addCover;
}

function candidateHasReplacementChanges(impact: CandidateImpact): boolean {
    return impact.replaceFields > 0 || impact.replaceCover;
}

function missingButtonLabel(impact: CandidateImpact): string {
    if (impact.addCover && impact.missingFields > 0) return 'Fill missing & cover';
    if (impact.addCover) return 'Use cover';
    return 'Fill missing';
}

function replaceButtonLabel(impact: CandidateImpact): string {
    if (impact.replaceCover && impact.replaceFields > 0) return 'Replace fields & cover';
    if (impact.replaceCover) return 'Replace cover';
    return 'Replace fields';
}

function plural(n: number, one: string, many = `${one}s`): string {
    return n === 1 ? one : many;
}

function candidateImpactText(impact: CandidateImpact) {
    const parts: string[] = [];
    if (impact.missingFields > 0) {
        parts.push(
            `fills ${impact.missingFields} missing ${plural(impact.missingFields, 'field')}`,
        );
    }
    if (impact.addCover) {
        parts.push('adds cover');
    }
    if (impact.replaceFields > 0) {
        parts.push(
            `would replace ${impact.replaceFields} ${plural(impact.replaceFields, 'field')}`,
        );
    }
    if (impact.replaceCover) {
        parts.push('would replace cover');
    }
    if (impact.skippedDirtyFields > 0) {
        parts.push(
            `skips ${impact.skippedDirtyFields} edited ${plural(
                impact.skippedDirtyFields,
                'field',
            )}`,
        );
    }
    if (impact.skippedCover) {
        parts.push('keeps selected cover');
    }
    return parts.length > 0 ? parts.join(' · ') : 'No applicable changes';
}

function candidatePatch(
    candidate: MetadataCandidate,
    book: Book,
    draft: BookUpdate,
    saved: BookUpdate,
    coverDraftURL: string | null,
    mode: 'missing' | 'replace',
): { patch: Partial<BookUpdate>; fields: MetadataFieldKey[]; coverUrl?: string } {
    const patch: Partial<BookUpdate> = {};
    const fields: MetadataFieldKey[] = [];
    let coverUrl: string | undefined;
    if (
        candidate.cover_url &&
        !coverDraftURL &&
        (mode === 'replace' || (mode === 'missing' && !book.has_cover))
    ) {
        coverUrl = candidate.cover_url;
    }
    for (const field of metadataFields) {
        const proposed = candidateFieldValue(field, candidate, draft);
        if (isEmptyMetadataValue(field.key, proposed)) continue;
        const current = draft[field.key];
        if (sameMetadataValue(current, proposed)) continue;
        if (fieldIsDirty(field.key, draft, saved)) continue;
        if (
            mode === 'missing' &&
            field.key !== 'identifiers' &&
            !isEmptyMetadataValue(field.key, current)
        )
            continue;
        (patch as Record<MetadataFieldKey, string | number | null>)[field.key] =
            proposed == null ? null : proposed;
        fields.push(field.key);
    }
    return { patch, fields, coverUrl };
}

function candidateFieldValue(
    field: MetadataField,
    candidate: MetadataCandidate,
    draft: BookUpdate,
) {
    const proposed = field.value(candidate);
    if (field.key !== 'identifiers' || typeof proposed !== 'string') return proposed;
    return mergeIdentifiers(String(draft.identifiers ?? ''), proposed);
}

function mergeIdentifiers(current: string, proposed: string): string {
    const merged = [...parseIdentifiers(current), ...parseIdentifiers(proposed)];
    const seen = new Set<string>();
    return formatIdentifiers(
        merged.filter((id) => {
            const key = `${id.type.trim().toLowerCase()}\u0000${id.value.trim().toLowerCase()}`;
            if (seen.has(key)) return false;
            seen.add(key);
            return true;
        }),
    );
}

function metadataCandidateFields(candidate: MetadataCandidate): string[] {
    const fields: string[] = [];
    if (candidate.cover_url) fields.push('Cover');
    for (const field of metadataFields) {
        if (
            !isEmptyMetadataValue(field.key, field.value(candidate)) ||
            (field.key === 'description' && hasDeferredDescription(candidate))
        ) {
            fields.push(field.label);
        }
    }
    return fields;
}

function hasDeferredDescription(candidate: MetadataCandidate): boolean {
    return !candidate.description && !!candidate.provider_id && !descriptionLookups.has(candidate);
}

function fieldIsDirty(key: MetadataFieldKey, draft: BookUpdate, saved: BookUpdate): boolean {
    return !sameMetadataValue(draft[key], saved[key]);
}

function isEmptyMetadataValue(key: MetadataFieldKey, value: unknown): boolean {
    if (value == null) return true;
    const s = String(value).trim();
    if (s === '') return true;
    return key === 'authors' && s.toLowerCase() === 'unknown author';
}

function sameMetadataValue(a: unknown, b: unknown): boolean {
    if (a == null && b == null) return true;
    return String(a ?? '').trim() === String(b ?? '').trim();
}
