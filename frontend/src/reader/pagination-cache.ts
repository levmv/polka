const STORAGE_KEY = 'polka-pagination';
const MAX_ENTRIES = 256;
// Limit all books and layouts to 128K UTF-16 code units (256 KiB).
const MAX_JSON_LENGTH = 128 * 1024;

interface Entry {
    layout: unknown;
    counts: Array<number | null>;
}

export interface PaginationLayout {
    book: string;
    reader: [number, number];
    viewport: [number, number];
    style: string;
    fontScale: number;
    columnWidth?: number;
    lineHeight?: number;
}

export function createPaginationCache(
    version: string,
    browser: string,
): {
    read(layout: PaginationLayout, sectionCount: number): Map<number, number>;
    write(
        layout: PaginationLayout,
        sectionCount: number,
        counts: ReadonlyMap<number, number>,
    ): void;
} {
    const readEntries = (): Entry[] => {
        try {
            const raw = localStorage.getItem(STORAGE_KEY);
            if (!raw || raw.length > MAX_JSON_LENGTH) return [];
            const value = JSON.parse(raw);
            if (value?.version !== version || value?.browser !== browser) return [];
            if (!Array.isArray(value.entries)) return [];
            return value.entries
                .filter(
                    (entry: Entry) =>
                        entry?.layout !== null &&
                        typeof entry?.layout === 'object' &&
                        Array.isArray(entry.counts) &&
                        entry.counts.every(
                            (count: unknown) =>
                                count === null ||
                                (typeof count === 'number' &&
                                    Number.isSafeInteger(count) &&
                                    count > 0),
                        ) &&
                        entry.counts.some((count) => count !== null),
                )
                .slice(0, MAX_ENTRIES);
        } catch {
            return [];
        }
    };

    const writeEntries = (recent: Entry[]): void => {
        recent.length = Math.min(recent.length, MAX_ENTRIES);
        const serialize = () => JSON.stringify({ version, browser, entries: recent });
        let raw = serialize();
        while (raw.length > MAX_JSON_LENGTH && recent.length > 1) {
            recent.pop();
            raw = serialize();
        }
        if (raw.length > MAX_JSON_LENGTH) return;
        try {
            localStorage.setItem(STORAGE_KEY, raw);
        } catch {
            // Disabled or full storage only costs another background count.
        }
    };

    return {
        read(layout, sectionCount) {
            const key = JSON.stringify(layout);
            const recent = readEntries();
            const index = recent.findIndex((entry) => JSON.stringify(entry.layout) === key);
            const entry = recent[index];
            const counts = new Map<number, number>();
            if (entry?.counts.length === sectionCount) {
                entry.counts.forEach((count, index) => {
                    if (count !== null) counts.set(index, count);
                });
                if (index > 0) {
                    recent.splice(index, 1);
                    recent.unshift(entry);
                    writeEntries(recent);
                }
            }
            return counts;
        },
        write(layout, sectionCount, counts) {
            const key = JSON.stringify(layout);
            const recent = readEntries().filter((entry) => JSON.stringify(entry.layout) !== key);
            // Empty counts invalidate the saved layout.
            if (counts.size) {
                recent.unshift({
                    layout,
                    counts: Array.from({ length: sectionCount }, (_, i) => counts.get(i) ?? null),
                });
            }
            writeEntries(recent);
        },
    };
}
