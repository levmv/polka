import { fetchBookSequence } from '../api';
import type { BookListContext } from '../book-list-context';
import { requiredElement } from '../dom';
import type { BookSequenceItem, BookSequenceWindow } from '../types';

export type SequenceDirection = 'previous' | 'next';

export type BookEditSequenceController = {
    previous: () => BookSequenceItem | null;
    next: () => BookSequenceItem | null;
    update: (dirty: boolean, busy: boolean) => void;
    snapshot: () => BookSequenceWindow | null;
    targetIndex: (targetID: number) => number;
    directionForIndex: (targetIndex: number) => SequenceDirection;
    setCurrentIndex: (index: number) => void;
    restore: (previous: BookSequenceWindow | null) => void;
    maybeRefreshExhausted: (direction: SequenceDirection) => void;
    start: () => void;
};

export function createBookEditSequenceController(opts: {
    root: HTMLElement;
    initialSequence?: BookSequenceWindow | null;
    listContext?: BookListContext | null;
    currentBookID: () => number;
    signal: AbortSignal;
    isDirty: () => boolean;
    isBusy: () => boolean;
    onOpen: (target: BookSequenceItem | null) => void;
}): BookEditSequenceController {
    const actions = requiredElement(opts.root, '[id^="edit-sequence-actions-"]');
    const previousBtn = requiredElement<HTMLButtonElement>(opts.root, '[id^="btn-edit-previous-"]');
    const nextBtn = requiredElement<HTMLButtonElement>(opts.root, '[id^="btn-edit-next-"]');

    let sequence: BookSequenceWindow | null = normalizeInitialSequence(
        opts.initialSequence,
        opts.currentBookID(),
    );

    const previous = () => sequenceItemAt(sequence, (sequence?.current_index ?? -1) - 1);
    const next = () => sequenceItemAt(sequence, (sequence?.current_index ?? -1) + 1);

    const syncVisibility = () => {
        actions.hidden = !previous() && !next();
    };

    const update = (dirty: boolean, busy: boolean) => {
        updateSequenceButton(previousBtn, previous(), dirty, busy, 'Previous', 'Save & Previous');
        updateSequenceButton(nextBtn, next(), dirty, busy, 'Next', 'Save & Next');
    };

    const refresh = async () => {
        if (!opts.listContext) return;
        const requestedID = opts.currentBookID();
        try {
            const nextSequence = await fetchBookSequence(
                requestedID,
                opts.listContext,
                opts.signal,
            );
            if (opts.signal.aborted || opts.currentBookID() !== requestedID) return;
            if (nextSequence.current_index < 0 || nextSequence.items.length === 0) {
                if (!sequence) actions.hidden = true;
                return;
            }
            sequence = nextSequence;
            syncVisibility();
            update(opts.isDirty(), opts.isBusy());
        } catch (e) {
            if (opts.signal.aborted || opts.currentBookID() !== requestedID) return;
            console.error('Failed to fetch book sequence:', e);
            if (!sequence) actions.hidden = true;
        }
    };

    const maybeRefreshExhausted = (direction: SequenceDirection) => {
        if (!opts.listContext || !sequence || sequence.current_index < 0) return;
        if (direction === 'previous' && previous()) return;
        if (direction === 'next' && next()) return;
        if (sequence.total !== undefined && sequence.items.length >= sequence.total) return;
        void refresh();
    };

    previousBtn.addEventListener('click', () => {
        opts.onOpen(previous());
    });
    nextBtn.addEventListener('click', () => {
        opts.onOpen(next());
    });

    return {
        previous,
        next,
        update,
        snapshot: () => sequence,
        targetIndex: (targetID) => sequence?.items.findIndex((item) => item.id === targetID) ?? -1,
        directionForIndex: (targetIndex) => {
            const currentIndex = sequence?.current_index ?? -1;
            return targetIndex >= 0 && currentIndex >= 0 && targetIndex < currentIndex
                ? 'previous'
                : 'next';
        },
        setCurrentIndex: (index) => {
            if (sequence && index >= 0) {
                sequence = { ...sequence, current_index: index };
                syncVisibility();
            }
        },
        restore: (previousSequence) => {
            sequence = previousSequence;
            syncVisibility();
            update(opts.isDirty(), opts.isBusy());
        },
        maybeRefreshExhausted,
        start: () => {
            syncVisibility();
            update(opts.isDirty(), opts.isBusy());
            if (!sequence) {
                void refresh();
            } else {
                maybeRefreshExhausted('next');
            }
        },
    };
}

function updateSequenceButton(
    button: HTMLButtonElement,
    item: BookSequenceItem | null | undefined,
    dirty: boolean,
    saving: boolean,
    cleanLabel: string,
    dirtyLabel: string,
): void {
    const label = dirty ? dirtyLabel : cleanLabel;
    button.disabled = saving || !item;
    const accessibleLabel = item ? `${label}: ${item.title}` : label;
    button.title = accessibleLabel;
    button.setAttribute('aria-label', accessibleLabel);
    button.dataset.state = dirty ? 'dirty' : 'clean';
}

function normalizeInitialSequence(
    sequence: BookSequenceWindow | null | undefined,
    currentID: number,
): BookSequenceWindow | null {
    if (!sequence || sequence.items.length === 0) return null;
    let currentIndex = sequence.current_index;
    if (
        currentIndex < 0 ||
        currentIndex >= sequence.items.length ||
        sequence.items[currentIndex]?.id !== currentID
    ) {
        currentIndex = sequence.items.findIndex((item) => item.id === currentID);
    }
    if (currentIndex < 0) return null;
    return { ...sequence, current_index: currentIndex };
}

function sequenceItemAt(
    sequence: BookSequenceWindow | null | undefined,
    index: number,
): BookSequenceItem | null {
    if (!sequence || index < 0 || index >= sequence.items.length) return null;
    return sequence.items[index];
}
