import { ANNOTATION_COLORS } from '../annotations';
import { iconElement } from '../icons';
import type { AnnotationColor } from '../types';

export function createAnnotationColorPicker(name: string) {
    const el = document.createElement('fieldset');
    el.className = 'annotation-colors';
    const legend = document.createElement('legend');
    legend.className = 'sr-only';
    legend.textContent = 'Highlight color';
    el.append(legend);
    const inputs = new Map<AnnotationColor, HTMLInputElement>();

    for (const color of Object.keys(ANNOTATION_COLORS) as AnnotationColor[]) {
        const label = document.createElement('label');
        label.className = 'annotation-color';
        label.title = color[0].toUpperCase() + color.slice(1);
        label.style.setProperty('--annotation-color', ANNOTATION_COLORS[color]);
        const input = document.createElement('input');
        input.type = 'radio';
        input.name = name;
        input.value = color;
        input.setAttribute('aria-label', label.title);
        const swatch = document.createElement('span');
        swatch.append(iconElement('check', 16));
        label.append(input, swatch);
        inputs.set(color, input);
        el.append(label);
    }

    return {
        el,
        getValue(): AnnotationColor {
            for (const [color, input] of inputs) if (input.checked) return color;
            return 'yellow';
        },
        setValue(color: AnnotationColor): void {
            for (const [value, input] of inputs) input.checked = value === color;
        },
    };
}
