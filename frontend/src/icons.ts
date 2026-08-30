// Icon references into the inline SVG sprite defined in layout.html (Material
// Symbols Outlined, copied as static SVG paths — no runtime dependency). Keep
// IconName in sync with the <symbol id="i-..."> ids in the sprite.
export type IconName =
    | 'bookmark'
    | 'sell'
    | 'person'
    | 'grid_view'
    | 'table_rows'
    | 'menu'
    | 'add'
    | 'upload'
    | 'search'
    | 'more_vert'
    | 'check'
    | 'close'
    | 'expand_more'
    | 'download'
    | 'edit'
    | 'menu_book'
    | 'calendar_month'
    | 'arrow_back'
    | 'delete'
    | 'content_copy'
    | 'logout'
    | 'settings';

// icon returns an <svg><use> snippet pointing at the shared sprite. extraClass
// lets callers attach styling hooks (e.g. 'format-icon').
export function icon(name: IconName, size = 20, extraClass = ''): string {
    const cls = extraClass ? `icon ${extraClass}` : 'icon';
    return `<svg class="${cls}" width="${size}" height="${size}" aria-hidden="true"><use href="#i-${name}"></use></svg>`;
}

export function iconElement(name: IconName, size = 20): SVGSVGElement {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.classList.add('icon');
    svg.setAttribute('width', String(size));
    svg.setAttribute('height', String(size));
    svg.setAttribute('aria-hidden', 'true');
    const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    use.setAttribute('href', `#i-${name}`);
    svg.append(use);
    return svg;
}
