export const CATALOG_CHANGED = 'polka:catalog-changed';

export function notifyCatalogChanged(): void {
    document.dispatchEvent(new CustomEvent(CATALOG_CHANGED));
}
