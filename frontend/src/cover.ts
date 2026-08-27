import { escapeHtml } from './dom';

export function coverUrl(
    workId: string,
    coverVersion: number,
    variant: 'display' | 'thumb' = 'display',
): string {
    const params = new URLSearchParams();
    if (variant !== 'display') {
        params.set('variant', variant);
    }
    if (coverVersion > 0) {
        // Server-side cover cache paths are stable by work id. v only changes
        // the browser URL after a cover upload so stale cached images are not reused.
        params.set('v', String(coverVersion));
    }
    const qs = params.toString();
    return `/covers/${workId}${qs ? `?${qs}` : ''}`;
}

// coverImgHtml renders a cover <img> for any screen. It lives here rather than
// in a page module so the edit dialog can reuse it without importing the book
// detail page it is opened from.
export function coverImgHtml(
    workId: string,
    coverVersion: number,
    idAttr?: string,
    imgClass = 'detail-cover-image',
): string {
    const idStr = idAttr ? ` id="${escapeHtml(idAttr)}"` : '';
    return `<img src="${coverUrl(workId, coverVersion)}"${idStr} draggable="false" class="${escapeHtml(imgClass)}" alt="">`;
}
