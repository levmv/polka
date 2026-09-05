import { textEl } from './dom';

// A completed import with failures stays readable until explicitly dismissed.
// Both uploads and server-folder imports use the same compact result surface.
export function createImportResult(
    summary: string,
    errors: string[],
    failed: number,
    onDismiss: () => void,
): HTMLElement {
    const result = document.createElement('section');
    result.className = 'import-result';
    result.setAttribute('role', 'status');
    result.setAttribute('aria-label', 'Import result');

    const header = document.createElement('div');
    header.className = 'import-result-header';
    const dismiss = document.createElement('button');
    dismiss.type = 'button';
    dismiss.className = 'import-result-dismiss';
    dismiss.textContent = '×';
    dismiss.setAttribute('aria-label', 'Dismiss import result');
    dismiss.addEventListener('click', onDismiss);
    header.append(textEl('strong', '', summary), dismiss);
    result.append(header);

    const details = document.createElement('div');
    details.className = 'import-result-details';
    details.tabIndex = 0;
    details.setAttribute('role', 'region');
    details.setAttribute('aria-label', 'Import errors');
    if (errors.length > 0) {
        const list = document.createElement('ul');
        for (const error of errors) list.append(textEl('li', '', error));
        details.append(list);
    }
    if (failed > errors.length) {
        details.append(
            textEl('p', '', `Showing ${errors.length} of ${failed} errors reported by the server.`),
        );
    }
    result.append(details);
    return result;
}
