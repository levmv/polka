import { bulkEditBooks, bulkShelfBooks, fetchShelves } from '../api';
import { formatAuthorList, parseAuthorList } from '../authors';
import {
    attachAuthorAutocomplete,
    attachSeriesAutocomplete,
    attachTagAutocomplete,
} from '../components/book-metadata-autocomplete';
import { escapeHtml } from '../dom';
import { openModal } from '../modal';
import type {
    BookSummary,
    BulkEditResult,
    BulkOperation,
    BulkSeriesIndexMode,
    BulkShelfOp,
    BulkTagMode,
    Shelf,
} from '../types';

// A shelf list longer than this grows a filter box; shorter ones just scan.
const SHELF_FILTER_THRESHOLD = 7;

export type BulkShelfOutcome = { changed: number; op: BulkShelfOp; shelfName: string };

// How many selected books the preview lists before collapsing the rest into a
// "…and N more" line. The change/unchanged summary always counts the full set.
const PREVIEW_LIMIT = 8;

type OnApplied = (result: BulkEditResult) => void;

// --- Pure tag transforms, mirroring internal/bookmeta/tags.go so the local preview
// matches exactly what the server will write. ---

export function parseTagList(s: string): string[] {
    const seen = new Set<string>();
    const out: string[] = [];
    for (const part of s.split(',')) {
        const t = part.trim();
        if (!t) continue;
        const key = t.toLowerCase();
        if (seen.has(key)) continue;
        seen.add(key);
        out.push(t);
    }
    return out;
}

export function formatTagList(tags: string[]): string {
    return tags.join(', ');
}

function normalizeTagValues(values: string[]): string[] {
    return parseTagList(values.join(','));
}

export function applyTagMode(current: string[], mode: BulkTagMode, values: string[]): string[] {
    switch (mode) {
        case 'clear':
            return [];
        case 'replace':
            return normalizeTagValues(values);
        case 'remove': {
            const drop = new Set(normalizeTagValues(values).map((v) => v.toLowerCase()));
            return normalizeTagValues(current).filter((t) => !drop.has(t.toLowerCase()));
        }
        case 'add': {
            const out = normalizeTagValues(current);
            const have = new Set(out.map((t) => t.toLowerCase()));
            for (const v of normalizeTagValues(values)) {
                const key = v.toLowerCase();
                if (have.has(key)) continue;
                have.add(key);
                out.push(v);
            }
            return out;
        }
    }
}

// --- Shared small widgets ---

type Segmented = {
    el: HTMLElement;
    getValue(): string;
    onChange(fn: () => void): void;
};

function createSegmented(options: { value: string; label: string }[], initial: string): Segmented {
    const el = document.createElement('div');
    el.className = 'bulk-segmented';
    el.setAttribute('role', 'radiogroup');
    let value = initial;
    let listener: (() => void) | null = null;
    const buttons = new Map<string, HTMLButtonElement>();

    const setValue = (next: string, fire: boolean) => {
        value = next;
        for (const [key, btn] of buttons) {
            const active = key === next;
            btn.classList.toggle('active', active);
            btn.setAttribute('aria-checked', active ? 'true' : 'false');
        }
        if (fire) listener?.();
    };

    for (const opt of options) {
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'bulk-segmented-btn';
        btn.textContent = opt.label;
        btn.setAttribute('role', 'radio');
        btn.dataset.value = opt.value;
        btn.addEventListener('click', () => setValue(opt.value, true));
        buttons.set(opt.value, btn);
        el.appendChild(btn);
    }
    setValue(initial, false);

    return {
        el,
        getValue: () => value,
        onChange: (fn) => {
            listener = fn;
        },
    };
}

// buildFooter creates the shared Cancel/Apply footer plus the inline status
// element that sits beside them. runBulk drives their state during apply.
function buildFooter(applyLabel: string): {
    actions: HTMLElement[];
    status: HTMLElement;
    cancelBtn: HTMLButtonElement;
    applyBtn: HTMLButtonElement;
} {
    const status = document.createElement('span');
    status.className = 'bulk-dialog-status';
    status.setAttribute('aria-live', 'polite');

    const cancelBtn = document.createElement('button');
    cancelBtn.type = 'button';
    cancelBtn.className = 'btn-confirm-cancel';
    cancelBtn.textContent = 'Cancel';
    cancelBtn.setAttribute('data-modal-close', '');

    const applyBtn = document.createElement('button');
    applyBtn.type = 'button';
    applyBtn.className = 'btn-confirm';
    applyBtn.textContent = applyLabel;

    return { actions: [status, cancelBtn, applyBtn], status, cancelBtn, applyBtn };
}

function summaryLine(selected: number, changed: number): string {
    const unchanged = selected - changed;
    const parts = [`${selected} selected`, `${changed} to change`];
    if (unchanged > 0) parts.push(`${unchanged} unchanged`);
    return parts.join(' · ');
}

function tagChips(tags: string[]): string {
    if (tags.length === 0) return '<span class="bulk-preview-empty">—</span>';
    return tags.map((t) => `<span class="bulk-preview-tag">${escapeHtml(t)}</span>`).join(' ');
}

function previewTable(rows: string, extra: number): string {
    const more = extra > 0 ? `<p class="bulk-preview-more">…and ${extra} more</p>` : '';
    return `
        <div class="bulk-preview-scroll">
            <table class="bulk-preview-table">
                <thead><tr><th>Title</th><th>Current</th><th>Result</th></tr></thead>
                <tbody>${rows}</tbody>
            </table>
        </div>
        ${more}`;
}

// --- Tags dialog ---

export function openBulkTagsDialog(books: BookSummary[], onApplied: OnApplied): void {
    const body = document.createElement('div');
    body.className = 'bulk-dialog';
    body.innerHTML = `
        <div class="bulk-dialog-field">
            <div data-mode-host></div>
        </div>
        <div class="bulk-dialog-field" data-tags-field>
            <label class="form-label" for="bulk-tags-input">Tags</label>
            <input id="bulk-tags-input" type="text" class="form-input" placeholder="tag, tag" autocomplete="off">
        </div>
        <div class="bulk-preview" data-preview></div>
    `;

    const mode = createSegmented(
        [
            { value: 'add', label: 'Add' },
            { value: 'remove', label: 'Remove' },
            { value: 'replace', label: 'Replace' },
            { value: 'clear', label: 'Clear' },
        ],
        'add',
    );
    body.querySelector('[data-mode-host]')?.appendChild(mode.el);

    const tagsField = body.querySelector('[data-tags-field]') as HTMLElement;
    const input = body.querySelector('#bulk-tags-input') as HTMLInputElement;
    const preview = body.querySelector('[data-preview]') as HTMLElement;
    const footer = buildFooter('Apply');

    const currentMode = () => mode.getValue() as BulkTagMode;
    const values = () => parseTagList(input.value);

    const hasInput = () => currentMode() === 'clear' || values().length > 0;

    const refresh = () => {
        const m = currentMode();
        tagsField.hidden = m === 'clear';
        const vals = values();
        let changed = 0;
        const rows: string[] = [];
        books.forEach((b, i) => {
            const current = parseTagList(b.tags || '');
            const result = applyTagMode(current, m, vals);
            if (formatTagList(result) !== formatTagList(current)) changed++;
            if (i < PREVIEW_LIMIT) {
                rows.push(`
                    <tr>
                        <td>${escapeHtml(b.title)}</td>
                        <td>${tagChips(current)}</td>
                        <td>${tagChips(result)}</td>
                    </tr>`);
            }
        });
        preview.innerHTML =
            `<p class="bulk-summary">${summaryLine(books.length, changed)}</p>` +
            previewTable(rows.join(''), Math.max(0, books.length - PREVIEW_LIMIT));
        footer.applyBtn.disabled = !hasInput();
    };

    mode.onChange(refresh);
    input.addEventListener('input', refresh);

    footer.applyBtn.addEventListener('click', () => {
        const m = currentMode();
        const op: BulkOperation =
            m === 'clear'
                ? { type: 'tags', mode: 'clear' }
                : { type: 'tags', mode: m, values: values() };
        void runBulk(books, [op], footer, onApplied, () => modal.close());
    });

    const { modal } = openModal({
        title: `Tags · ${books.length} books`,
        body,
        actions: footer.actions,
        modalClass: 'bulk-modal',
    });
    modal.open();
    attachTagAutocomplete(input);
    refresh();
}

// --- Series dialog ---

function seriesLabel(name: string | null, index: number | null): string {
    if (!name) return '';
    return index != null ? `${name} #${index}` : name;
}

export function openBulkSeriesDialog(books: BookSummary[], onApplied: OnApplied): void {
    const body = document.createElement('div');
    body.className = 'bulk-dialog';
    body.innerHTML = `
        <div class="bulk-dialog-field">
            <div data-mode-host></div>
        </div>
        <div class="bulk-dialog-field" data-series-field>
            <label class="form-label" for="bulk-series-input">Series</label>
            <input id="bulk-series-input" type="text" class="form-input" placeholder="Series name" autocomplete="off">
        </div>
        <div class="bulk-dialog-field" data-index-field>
            <span class="form-label">Numbering</span>
            <div data-index-host></div>
            <div class="bulk-index-range" data-range hidden>
                <label>Start <input type="number" step="0.1" class="form-input" data-start value="1"></label>
                <label>Step <input type="number" step="0.1" class="form-input" data-step value="1"></label>
            </div>
        </div>
        <div class="bulk-preview" data-preview></div>
    `;

    const mode = createSegmented(
        [
            { value: 'set', label: 'Set series' },
            { value: 'clear', label: 'Clear series' },
        ],
        'set',
    );
    body.querySelector('[data-mode-host]')?.appendChild(mode.el);

    const indexMode = createSegmented(
        [
            { value: 'keep', label: 'Keep' },
            { value: 'clear', label: 'Clear' },
            { value: 'assign', label: 'Number by order' },
        ],
        'keep',
    );
    body.querySelector('[data-index-host]')?.appendChild(indexMode.el);

    const seriesField = body.querySelector('[data-series-field]') as HTMLElement;
    const indexField = body.querySelector('[data-index-field]') as HTMLElement;
    const rangeRow = body.querySelector('[data-range]') as HTMLElement;
    const input = body.querySelector('#bulk-series-input') as HTMLInputElement;
    const startInput = body.querySelector('[data-start]') as HTMLInputElement;
    const stepInput = body.querySelector('[data-step]') as HTMLInputElement;
    const preview = body.querySelector('[data-preview]') as HTMLElement;
    const footer = buildFooter('Apply');

    const num = (el: HTMLInputElement, fallback: number) => {
        const v = Number.parseFloat(el.value);
        return Number.isFinite(v) ? v : fallback;
    };

    const refresh = () => {
        const setting = mode.getValue() === 'set';
        seriesField.hidden = !setting;
        indexField.hidden = !setting;
        const idxMode = indexMode.getValue() as BulkSeriesIndexMode;
        rangeRow.hidden = !setting || idxMode !== 'assign';

        const name = input.value.trim();
        const start = num(startInput, 1);
        const step = num(stepInput, 1);

        let changed = 0;
        const rows: string[] = [];
        books.forEach((b, i) => {
            const current = seriesLabel(b.series, b.series_index);
            let result = '';
            if (setting && name) {
                let idx: number | null = b.series_index;
                if (idxMode === 'clear') idx = null;
                else if (idxMode === 'assign') idx = start + step * i;
                result = seriesLabel(name, idx);
            }
            if (result !== current) changed++;
            if (i < PREVIEW_LIMIT) {
                rows.push(`
                    <tr>
                        <td>${escapeHtml(b.title)}</td>
                        <td>${current ? escapeHtml(current) : '<span class="bulk-preview-empty">—</span>'}</td>
                        <td>${result ? escapeHtml(result) : '<span class="bulk-preview-empty">—</span>'}</td>
                    </tr>`);
            }
        });
        preview.innerHTML =
            `<p class="bulk-summary">${summaryLine(books.length, changed)}</p>` +
            previewTable(rows.join(''), Math.max(0, books.length - PREVIEW_LIMIT));

        footer.applyBtn.disabled = setting && name === '';
    };

    mode.onChange(refresh);
    indexMode.onChange(refresh);
    for (const el of [input, startInput, stepInput]) el.addEventListener('input', refresh);

    footer.applyBtn.addEventListener('click', () => {
        let op: BulkOperation;
        if (mode.getValue() === 'clear') {
            op = { type: 'series', mode: 'clear' };
        } else {
            const idxMode = indexMode.getValue() as BulkSeriesIndexMode;
            op = {
                type: 'series',
                mode: 'set',
                name: input.value.trim(),
                index:
                    idxMode === 'assign'
                        ? { mode: 'assign', start: num(startInput, 1), step: num(stepInput, 1) }
                        : { mode: idxMode },
            };
        }
        void runBulk(books, [op], footer, onApplied, () => modal.close());
    });

    const { modal } = openModal({
        title: `Series · ${books.length} books`,
        body,
        actions: footer.actions,
        modalClass: 'bulk-modal',
    });
    modal.open();
    attachSeriesAutocomplete(input);
    refresh();
}

// --- Authors dialog ---

// Bulk authors is replace-only: every selected book gets the same author list.
// Authors are required, so there is no clear mode (unlike series/tags).
export function openBulkAuthorsDialog(books: BookSummary[], onApplied: OnApplied): void {
    const body = document.createElement('div');
    body.className = 'bulk-dialog';
    body.innerHTML = `
        <div class="bulk-dialog-field">
            <label class="form-label" for="bulk-authors-input">Authors<span class="field-hint"> — semicolon separated</span></label>
            <input id="bulk-authors-input" type="text" class="form-input" placeholder="Author; Author" autocomplete="off">
        </div>
        <div class="bulk-preview" data-preview></div>
    `;

    const input = body.querySelector('#bulk-authors-input') as HTMLInputElement;
    const preview = body.querySelector('[data-preview]') as HTMLElement;
    const footer = buildFooter('Apply');

    const refresh = () => {
        // Mirror the server: compare the canonical author-list form both ways.
        const target = formatAuthorList(parseAuthorList(input.value));
        let changed = 0;
        const rows: string[] = [];
        books.forEach((b, i) => {
            const current = formatAuthorList(b.authors_list.map((a) => a.name));
            if (target && target !== current) changed++;
            if (i < PREVIEW_LIMIT) {
                rows.push(`
                    <tr>
                        <td>${escapeHtml(b.title)}</td>
                        <td>${current ? escapeHtml(current) : '<span class="bulk-preview-empty">—</span>'}</td>
                        <td>${target ? escapeHtml(target) : '<span class="bulk-preview-empty">—</span>'}</td>
                    </tr>`);
            }
        });
        preview.innerHTML =
            `<p class="bulk-summary">${summaryLine(books.length, changed)}</p>` +
            previewTable(rows.join(''), Math.max(0, books.length - PREVIEW_LIMIT));
        footer.applyBtn.disabled = target === '';
    };

    input.addEventListener('input', refresh);

    footer.applyBtn.addEventListener('click', () => {
        const op: BulkOperation = { type: 'authors', mode: 'set', authors: input.value.trim() };
        void runBulk(books, [op], footer, onApplied, () => modal.close());
    });

    const { modal } = openModal({
        title: `Authors · ${books.length} books`,
        body,
        actions: footer.actions,
        modalClass: 'bulk-modal',
    });
    modal.open();
    attachAuthorAutocomplete(input);
    refresh();
}

// --- Shelves dialog ---

// Bulk shelves is a per-user membership action, not a work edit: pick one manual
// shelf and add or remove the whole selection to/from it. It returns how many
// memberships changed rather than updated book summaries.
export function openBulkShelvesDialog(
    books: BookSummary[],
    onDone: (outcome: BulkShelfOutcome) => void,
): void {
    const body = document.createElement('div');
    body.className = 'bulk-dialog';
    body.innerHTML = `
        <div class="bulk-dialog-field">
            <div data-mode-host></div>
        </div>
        <div class="bulk-dialog-field">
            <span class="form-label">Shelf</span>
            <input type="text" class="form-input bulk-shelf-filter" placeholder="Filter shelves" aria-label="Filter shelves" autocomplete="off" hidden>
            <div class="bulk-shelf-list" data-shelf-list role="radiogroup" aria-label="Shelf">
                <p class="bulk-preview-empty">Loading…</p>
            </div>
        </div>
    `;

    const mode = createSegmented(
        [
            { value: 'add', label: 'Add to shelf' },
            { value: 'remove', label: 'Remove from shelf' },
        ],
        'add',
    );
    body.querySelector('[data-mode-host]')?.appendChild(mode.el);

    const filter = body.querySelector('.bulk-shelf-filter') as HTMLInputElement;
    const list = body.querySelector('[data-shelf-list]') as HTMLElement;
    const footer = buildFooter('Apply');
    footer.applyBtn.disabled = true;

    let picked: Shelf | null = null;

    const renderList = (shelves: Shelf[]) => {
        list.replaceChildren();
        if (shelves.length === 0) {
            list.innerHTML = '<p class="bulk-preview-empty">No shelves yet.</p>';
            return;
        }
        for (const shelf of shelves) {
            const row = document.createElement('label');
            row.className = 'bulk-shelf-row';
            row.dataset.name = shelf.name.toLowerCase();
            const radio = document.createElement('input');
            radio.type = 'radio';
            radio.name = 'bulk-shelf';
            radio.addEventListener('change', () => {
                picked = shelf;
                footer.applyBtn.disabled = false;
            });
            const name = document.createElement('span');
            name.className = 'bulk-shelf-name';
            name.textContent = shelf.name;
            row.append(radio, name);
            list.appendChild(row);
        }
    };

    footer.applyBtn.addEventListener('click', () => {
        if (!picked) return;
        const shelf = picked;
        const op = mode.getValue() as BulkShelfOp;
        void (async () => {
            footer.applyBtn.disabled = true;
            footer.cancelBtn.disabled = true;
            footer.status.textContent = 'Applying…';
            footer.status.classList.remove('is-error');
            try {
                const result = await bulkShelfBooks(
                    shelf.id,
                    books.map((b) => b.id),
                    op,
                );
                onDone({ changed: result.changed, op, shelfName: shelf.name });
                modal.close();
            } catch (e) {
                footer.status.textContent = e instanceof Error ? e.message : 'Shelf update failed';
                footer.status.classList.add('is-error');
                footer.applyBtn.disabled = false;
                footer.cancelBtn.disabled = false;
            }
        })();
    });

    const { modal } = openModal({
        title: `Shelves · ${books.length} books`,
        body,
        actions: footer.actions,
        modalClass: 'bulk-modal',
    });
    modal.open();

    void fetchShelves()
        .then((shelves) => {
            const manual = shelves.filter((s) => s.kind === 'manual');
            renderList(manual);
            if (manual.length > SHELF_FILTER_THRESHOLD) {
                filter.hidden = false;
                filter.addEventListener('input', () => {
                    const q = filter.value.trim().toLowerCase();
                    for (const row of list.querySelectorAll<HTMLElement>('.bulk-shelf-row')) {
                        row.hidden = !(row.dataset.name || '').includes(q);
                    }
                });
            }
        })
        .catch(() => {
            list.innerHTML = '<p class="bulk-preview-empty">Could not load shelves.</p>';
        });
}

// runBulk performs the shared apply lifecycle for a dialog footer: it disables
// Apply while the request is in flight, closes the dialog on success, and shows an
// inline status on failure (the dialog stays open so the user can retry).
async function runBulk(
    books: BookSummary[],
    operations: BulkOperation[],
    footer: ReturnType<typeof buildFooter>,
    onApplied: OnApplied,
    close: () => void,
): Promise<void> {
    footer.applyBtn.disabled = true;
    footer.cancelBtn.disabled = true;
    footer.status.textContent = 'Applying…';
    footer.status.classList.remove('is-error');
    try {
        const result = await bulkEditBooks({ ids: books.map((b) => b.id), operations });
        onApplied(result);
        close();
    } catch (e) {
        footer.status.textContent = e instanceof Error ? e.message : 'Bulk edit failed';
        footer.status.classList.add('is-error');
        footer.applyBtn.disabled = false;
        footer.cancelBtn.disabled = false;
    }
}
