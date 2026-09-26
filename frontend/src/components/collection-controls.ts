import { icon } from '../icons';
import { createSelect, type SelectOption } from './select';

export type CollectionView = 'grid' | 'table';

type CollectionControlsOptions = {
    view: CollectionView;
    tableLabel?: string;
    sort: string;
    sortOptions: SelectOption[];
    sortLabel: string;
    onViewChange(view: CollectionView): void;
    onSortChange(sort: string): void;
};

export type CollectionControls = {
    el: HTMLElement;
    // Programmatic updates do not emit onSortChange. The page owns the query,
    // including defaults that depend on the search or other page state.
    setSort(value: string, options?: SelectOption[]): void;
    close(): void;
    destroy(): void;
};

export function createCollectionControls(opts: CollectionControlsOptions): CollectionControls {
    let view = opts.view;
    const el = document.createElement('div');
    el.className = 'collection-controls';
    const toggle = document.createElement('div');
    toggle.className = 'view-toggle';
    toggle.setAttribute('role', 'group');
    toggle.setAttribute('aria-label', 'View');

    const buttons = (['grid', 'table'] as const).map((mode) => {
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'view-btn';
        button.setAttribute(
            'aria-label',
            mode === 'grid' ? 'Grid view' : (opts.tableLabel ?? 'Table view'),
        );
        button.innerHTML = icon(mode === 'grid' ? 'grid_view' : 'table_rows', 18);
        button.addEventListener('click', () => {
            if (view === mode) return;
            view = mode;
            syncView();
            opts.onViewChange(view);
        });
        toggle.append(button);
        return { mode, button };
    });

    function syncView(): void {
        for (const { mode, button } of buttons) {
            button.setAttribute('aria-pressed', String(mode === view));
        }
    }

    const makeSort = (value: string, options: SelectOption[]) =>
        createSelect({
            ariaLabel: opts.sortLabel,
            value,
            options,
            onChange: opts.onSortChange,
        });
    let sort = makeSort(opts.sort, opts.sortOptions);
    el.append(toggle, sort.el);
    syncView();

    return {
        el,
        setSort(value, options): void {
            if (!options) {
                sort.setValue(value);
                return;
            }
            const previous = sort.el;
            sort.destroy();
            sort = makeSort(value, options);
            previous.replaceWith(sort.el);
        },
        close(): void {
            sort.close();
        },
        destroy(): void {
            sort.destroy();
            el.remove();
        },
    };
}
