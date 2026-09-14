export type AutosavedValue<T> = {
    readonly value: T;
    set(value: T): void;
    subscribe(listener: (value: T, previous: T) => void): () => void;
};

// Each choice replaces the previous request; only its own result can update the value.
export function createAutosavedValue<T>(
    initial: T,
    save: (value: T, signal: AbortSignal) => Promise<T>,
    onError: (error: unknown) => void,
): AutosavedValue<T> {
    let value = initial;
    let saved = initial;
    let request: AbortController | null = null;
    const listeners = new Set<(value: T, previous: T) => void>();

    function publish(next: T): void {
        if (next === value) return;
        const previous = value;
        value = next;
        for (const listener of [...listeners]) listener(value, previous);
    }

    async function persist(next: T, controller: AbortController): Promise<void> {
        try {
            const result = await save(next, controller.signal);
            if (controller.signal.aborted) return;
            saved = result;
            publish(saved);
        } catch (error) {
            if (controller.signal.aborted) return;
            publish(saved);
            onError(error);
        } finally {
            if (request === controller) request = null;
        }
    }

    return {
        get value() {
            return value;
        },
        set(next) {
            if (next === value) return;
            request?.abort();
            const controller = new AbortController();
            request = controller;
            publish(next);
            void persist(next, controller);
        },
        subscribe(listener) {
            listeners.add(listener);
            return () => listeners.delete(listener);
        },
    };
}
