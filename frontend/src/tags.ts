// Keep in sync with bookmeta.MaxTagDepth in internal/bookmeta/tags.go.
const MAX_TAG_DEPTH = 16;

// Empty components or excessive depth make the whole name literal,
// including names such as .NET and A..B.
export function tagParts(name: string): string[] {
    if (!name.includes('.')) return [name];
    const parts = name.split('.', MAX_TAG_DEPTH + 1).map((part) => part.trim());
    return parts.length <= MAX_TAG_DEPTH && parts.every(Boolean) ? parts : [name];
}

export function parseTagList(value: string): string[] {
    const seen = new Set<string>();
    const names: string[] = [];
    for (const part of value.split(',')) {
        const trimmed = part.trim();
        if (!trimmed) continue;
        const name = tagParts(trimmed).join('.');
        const key = name.toLowerCase();
        if (seen.has(key)) continue;
        seen.add(key);
        names.push(name);
    }
    return names;
}

// Shorten canonical names for one book, keeping full paths where leaves collide.
export function tagDisplayLabels(names: readonly string[]): readonly string[] {
    if (!names.some((name) => name.includes('.'))) return names;

    const counts = new Map<string, number>();
    const labels = names.map((name) => {
        const parts = tagParts(name);
        const label = parts[parts.length - 1];
        const key = label.toLowerCase();
        counts.set(key, (counts.get(key) || 0) + 1);
        return label;
    });
    for (let i = 0; i < labels.length; i++) {
        if (counts.get(labels[i].toLowerCase()) !== 1) labels[i] = names[i];
    }
    return labels;
}
