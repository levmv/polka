export function browserTimeZone(): string {
    try {
        return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
    } catch {
        return 'UTC';
    }
}

export function suggestedTimeZones(current: string): string[] {
    // Older Safari can still validate and use named zones without providing a
    // list. The editable field remains usable there, with local/UTC suggestions.
    let zones: string[] = [];
    try {
        const intl = Intl as typeof Intl & { supportedValuesOf?: (key: string) => string[] };
        zones = intl.supportedValuesOf?.('timeZone') ?? [];
    } catch {
        // Suggestions are optional; validation happens on the server.
    }
    return [...new Set(['UTC', browserTimeZone(), current, ...zones].filter(Boolean))].sort();
}
