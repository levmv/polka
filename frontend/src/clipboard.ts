export async function copyText(text: string): Promise<void> {
    if (navigator.clipboard?.writeText) {
        try {
            await navigator.clipboard.writeText(text);
            return;
        } catch {
            // The legacy path can still work when the Clipboard API rejects.
        }
    }

    const activeElement = document.activeElement;
    const textarea = document.createElement('textarea');
    textarea.value = text;
    textarea.readOnly = true;
    textarea.style.position = 'fixed';
    textarea.style.left = '-9999px';
    document.body.append(textarea);
    try {
        textarea.focus({ preventScroll: true });
        textarea.select();
        if (!document.execCommand('copy')) throw new Error('Copy failed');
    } finally {
        textarea.remove();
        if (activeElement instanceof HTMLElement) activeElement.focus({ preventScroll: true });
    }
}
