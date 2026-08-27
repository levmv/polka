export type ManagedToggle = {
    el: HTMLButtonElement;
    isChecked(): boolean;
    setChecked(checked: boolean): void;
};

export type ToggleOptions = {
    checked: boolean;
    onChange: (checked: boolean) => void;
    ariaLabel?: string;
};

// createToggle builds an accessible on/off switch (`role="switch"`). The visual
// track/knob is pure CSS keyed off `aria-checked`. Buttons fire click on
// Space/Enter, so no extra key handling is needed.
export function createToggle(opts: ToggleOptions): ManagedToggle {
    let checked = opts.checked;

    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'toggle';
    button.setAttribute('role', 'switch');
    if (opts.ariaLabel) button.setAttribute('aria-label', opts.ariaLabel);

    const knob = document.createElement('span');
    knob.className = 'toggle-knob';
    button.appendChild(knob);

    function sync(): void {
        button.setAttribute('aria-checked', String(checked));
    }

    button.addEventListener('click', () => {
        checked = !checked;
        sync();
        opts.onChange(checked);
    });

    sync();

    return {
        el: button,
        isChecked: () => checked,
        setChecked(next: boolean): void {
            checked = next;
            sync();
        },
    };
}
