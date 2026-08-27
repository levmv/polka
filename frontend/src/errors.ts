export function errorMessage(error: unknown, fallback?: string): string {
    if (error instanceof Error && error.message.trim()) {
        return error.message.trim();
    }
    if (fallback !== undefined) {
        return fallback;
    }
    return String(error);
}
