// Live results share one history step until the reader finishes editing the
// query. Flush before another navigation or overlay takes ownership of history.
export function trackSearch(
    input: HTMLInputElement,
    apply: (newEntry: boolean) => void,
): { finish(): void; destroy(): void } {
    let applied = input.value.trim();
    let editing = false;
    let blurred = false;
    let timer = 0;
    const update = () => {
        window.clearTimeout(timer);
        timer = 0;
        const value = input.value.trim();
        if (value !== applied) {
            apply(!editing);
            applied = value;
            editing = true;
        }
        if (blurred) editing = false;
    };
    const finish = () => {
        update();
        editing = false;
    };
    const onInput = () => {
        window.clearTimeout(timer);
        timer = window.setTimeout(update, 250);
    };
    const onKeydown = (event: KeyboardEvent) => {
        if (event.key === 'Enter') finish();
    };
    // Replacing results during blur can remove the link between pointerdown
    // and click. Navigation flushes the query after it has captured that link.
    const onBlur = () => {
        blurred = true;
        if (!timer) editing = false;
    };
    const onFocus = () => {
        if (blurred) finish();
        blurred = false;
    };
    input.addEventListener('input', onInput);
    input.addEventListener('blur', onBlur);
    input.addEventListener('focus', onFocus);
    input.addEventListener('keydown', onKeydown);
    return {
        finish,
        destroy() {
            window.clearTimeout(timer);
            input.removeEventListener('input', onInput);
            input.removeEventListener('blur', onBlur);
            input.removeEventListener('focus', onFocus);
            input.removeEventListener('keydown', onKeydown);
        },
    };
}
