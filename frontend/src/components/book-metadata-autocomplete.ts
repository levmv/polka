import { fetchAuthors, fetchSeriesPage, fetchTags } from '../api';
import { isAuthorListDelimiter } from '../authors';
import type { Author } from '../types';
import {
    attachTextListAutocomplete,
    type TextListAutocompleteController,
} from './text-list-autocomplete';

// These adapters keep catalog-backed metadata suggestions consistent between
// the single-book editor and bulk edit without teaching the generic text-list
// control about book fields.
export function attachAuthorAutocomplete(
    input: HTMLInputElement,
    options: { onPick?: (author: Author) => void } = {},
): TextListAutocompleteController {
    return attachTextListAutocomplete(input, {
        className: 'author-list-input',
        // Authors are edited as one text-compatible list. Semicolon is
        // canonical; a delimiter-like ampersand remains compatible with
        // calibre-style strings.
        isDelimiter: isAuthorListDelimiter,
        load: async (query) =>
            (await fetchAuthors(query)).map((author) => ({
                value: author.name,
                label: author.name,
                meta: author.sort_name && author.sort_name !== author.name ? author.sort_name : '',
                data: author,
            })),
        onPick: (suggestion) => {
            const author = suggestion.data as Author | undefined;
            if (author) options.onPick?.(author);
        },
    });
}

export function attachTagAutocomplete(input: HTMLInputElement): TextListAutocompleteController {
    return attachTextListAutocomplete(input, {
        className: 'tag-list-input',
        load: async (query) => (await fetchTags(query)).map((tag) => ({ value: tag })),
    });
}

export function attachSeriesAutocomplete(input: HTMLInputElement): TextListAutocompleteController {
    return attachTextListAutocomplete(input, {
        // A series name is one value, never a comma-separated list.
        isDelimiter: () => false,
        load: async (query) => {
            const page = await fetchSeriesPage('', query.trim(), 12);
            return page.items.map((series) => ({ value: series.name }));
        },
    });
}
