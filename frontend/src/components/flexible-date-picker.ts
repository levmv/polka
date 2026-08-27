import { escapeHtml } from '../dom';
import { icon } from '../icons';
import { createPopover, type ManagedPopover } from '../popover';

type DatePrecision = 'year' | 'month' | 'day';

type DateParts = {
    year: string;
    month: string;
    day: string;
};

type PickerState = {
    mode: DatePrecision;
    year: number;
    month: number;
    day: number;
};

type DateDisplayFormat = 'iso' | 'day-month-year' | 'month-day-year';

type FlexibleDatePickerOptions = {
    value: () => string;
    onCommit: (value: string) => void | Promise<void>;
};

const MONTHS = [
    { value: '01', short: 'Jan', long: 'January' },
    { value: '02', short: 'Feb', long: 'February' },
    { value: '03', short: 'Mar', long: 'March' },
    { value: '04', short: 'Apr', long: 'April' },
    { value: '05', short: 'May', long: 'May' },
    { value: '06', short: 'Jun', long: 'June' },
    { value: '07', short: 'Jul', long: 'July' },
    { value: '08', short: 'Aug', long: 'August' },
    { value: '09', short: 'Sep', long: 'September' },
    { value: '10', short: 'Oct', long: 'October' },
    { value: '11', short: 'Nov', long: 'November' },
    { value: '12', short: 'Dec', long: 'December' },
] as const;
const WEEKDAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
const TEXT_DATE_RE =
    /^(?:(\d{1,2})(?:st|nd|rd|th)?\s+)?([a-z]+)\s+(?:(\d{1,2})(?:st|nd|rd|th)?(?:,\s*|\s+))?(\d{4})$/i;

export function attachFlexibleDatePicker(
    input: HTMLInputElement,
    trigger: HTMLButtonElement,
    options: FlexibleDatePickerOptions,
): ManagedPopover {
    return createPopover(trigger, (panel, popover) => {
        renderDatePicker(panel, popover, input, options);
    });
}

function renderDatePicker(
    panel: HTMLElement,
    popover: ManagedPopover,
    input: HTMLInputElement,
    options: FlexibleDatePickerOptions,
): void {
    panel.classList.add('date-picker-popover');
    const rawValue = options.value() || input.value;
    const state = initialState(rawValue);
    const displayFormat = dateDisplayFormat(rawValue);

    const commit = (precision: DatePrecision) => {
        const value = formatDateValue(precision, state, displayFormat);
        input.value = value;
        input.dispatchEvent(new Event('input', { bubbles: true }));
        Promise.resolve(options.onCommit(value)).then(() => {
            popover.close();
        });
    };

    const redraw = () => {
        renderPickerState(panel, state);
        bindPickerControls(panel, state, redraw, commit);
        popover.reposition();
    };

    redraw();
}

function dateDisplayFormat(raw: string): DateDisplayFormat {
    const normalized = raw.trim();
    if (/^\d{4}(?:-\d{2}(?:-\d{2})?)?$/.test(normalized)) {
        return 'iso';
    }
    const text = TEXT_DATE_RE.exec(normalized);
    if (text?.[3]) {
        return 'month-day-year';
    }
    return 'day-month-year';
}

function dateParts(raw: string): DateParts {
    const normalized = raw.trim();
    const m = /^(\d{4})(?:-(\d{2})(?:-(\d{2}))?)?$/.exec(normalized);
    if (m) {
        return { year: m[1], month: m[2] || '', day: m[3] || '' };
    }
    const text = TEXT_DATE_RE.exec(normalized);
    if (text) {
        const month = monthValue(text[2]);
        if (month) {
            const day = text[1] || text[3] || '';
            return {
                year: text[4],
                month,
                day: day ? String(Number(day)).padStart(2, '0') : '',
            };
        }
    }
    const year = /\b(1[0-9]{3}|20[0-9]{2})\b/.exec(normalized)?.[1] || '';
    return { year, month: '', day: '' };
}

function monthValue(raw: string): string {
    const prefix = raw.trim().toLowerCase().slice(0, 3);
    return MONTHS.find((month) => month.short.toLowerCase() === prefix)?.value || '';
}

function initialState(raw: string): PickerState {
    const now = new Date();
    const parts = dateParts(raw);
    const year = clampYear(Number(parts.year) || now.getFullYear());
    const month = clampMonth(Number(parts.month) || now.getMonth() + 1);
    const parsedDay = Number(parts.day) || 1;
    const day = validDay(year, month, parsedDay) ? parsedDay : 1;
    const mode: DatePrecision = parts.day ? 'day' : parts.month ? 'month' : 'year';
    return { mode, year, month, day };
}

function renderPickerState(panel: HTMLElement, state: PickerState): void {
    const title =
        state.mode === 'day'
            ? `${monthLabel(state.month, 'long')} ${state.year}`
            : state.mode === 'month'
              ? String(state.year)
              : yearRangeLabel(state.year);

    panel.innerHTML = `
        <div class="date-picker-mode" role="group" aria-label="Date precision">
            ${modeButton('year', state.mode)}
            ${modeButton('month', state.mode)}
            ${modeButton('day', state.mode)}
        </div>
        <div class="date-picker-header">
            <button type="button" class="date-picker-nav" data-nav="-1" aria-label="${state.mode === 'day' ? 'Previous month' : 'Previous year'}">${icon('arrow_back', 17)}</button>
            <div class="date-picker-current">${escapeHtml(title)}</div>
            <button type="button" class="date-picker-nav" data-nav="1" aria-label="${state.mode === 'day' ? 'Next month' : 'Next year'}">${icon('arrow_back', 17, 'date-picker-next-icon')}</button>
        </div>
        <div class="date-picker-view">
            ${state.mode === 'year' ? yearView(state) : ''}
            ${state.mode === 'month' ? monthView(state) : ''}
            ${state.mode === 'day' ? dayView(state) : ''}
        </div>
    `;
}

function bindPickerControls(
    panel: HTMLElement,
    state: PickerState,
    redraw: () => void,
    commit: (precision: DatePrecision) => void,
): void {
    for (const button of panel.querySelectorAll<HTMLButtonElement>('.date-picker-mode-btn')) {
        button.addEventListener('click', () => {
            const nextMode = button.dataset.mode as DatePrecision | undefined;
            if (!nextMode || nextMode === state.mode) return;
            state.mode = nextMode;
            redraw();
        });
    }

    for (const button of panel.querySelectorAll<HTMLButtonElement>('.date-picker-nav')) {
        button.addEventListener('click', () => {
            const delta = Number(button.dataset.nav) || 0;
            if (state.mode === 'day') {
                shiftMonth(state, delta);
            } else if (state.mode === 'year') {
                state.year = clampYear(state.year + delta * 12);
            } else {
                state.year = clampYear(state.year + delta);
            }
            redraw();
        });
    }

    for (const button of panel.querySelectorAll<HTMLButtonElement>('[data-year]')) {
        button.addEventListener('click', () => {
            state.year = clampYear(Number(button.dataset.year) || state.year);
            commit('year');
        });
    }

    for (const button of panel.querySelectorAll<HTMLButtonElement>('[data-month]')) {
        button.addEventListener('click', () => {
            state.month = clampMonth(Number(button.dataset.month) || state.month);
            commit('month');
        });
    }

    for (const button of panel.querySelectorAll<HTMLButtonElement>('[data-day]')) {
        button.addEventListener('click', () => {
            const day = Number(button.dataset.day) || state.day;
            if (!validDay(state.year, state.month, day)) return;
            state.day = day;
            commit('day');
        });
    }
}

function modeButton(mode: DatePrecision, activeMode: DatePrecision): string {
    const label = mode[0].toUpperCase() + mode.slice(1);
    return `<button type="button" class="date-picker-mode-btn" data-mode="${mode}" aria-pressed="${mode === activeMode}">${label}</button>`;
}

function yearView(state: PickerState): string {
    const start = state.year - 5;
    const years = Array.from({ length: 12 }, (_, index) => start + index);
    return `<div class="date-picker-year-grid">
        ${years
            .map(
                (year) =>
                    `<button type="button" class="date-picker-cell ${year === state.year ? 'is-active' : ''}" data-year="${year}">${year}</button>`,
            )
            .join('')}
    </div>`;
}

function monthView(state: PickerState): string {
    return `<div class="date-picker-month-grid">
        ${MONTHS.map((month, index) => {
            const number = index + 1;
            return `<button type="button" class="date-picker-cell ${number === state.month ? 'is-active' : ''}" data-month="${number}" aria-label="${month.long}">${month.short}</button>`;
        }).join('')}
    </div>`;
}

function dayView(state: PickerState): string {
    const firstWeekday = new Date(Date.UTC(state.year, state.month - 1, 1)).getUTCDay();
    const leading = (firstWeekday + 6) % 7;
    const days = daysInMonth(state.year, state.month);
    const blanks = Array.from(
        { length: leading },
        () => '<div class="date-picker-day-empty"></div>',
    );
    const dayButtons = Array.from({ length: days }, (_, index) => {
        const day = index + 1;
        return `<button type="button" class="date-picker-cell date-picker-day ${day === state.day ? 'is-active' : ''}" data-day="${day}" aria-label="${day}">${day}</button>`;
    });

    return `<div class="date-picker-calendar">
        ${WEEKDAYS.map((day) => `<div class="date-picker-weekday">${day}</div>`).join('')}
        ${blanks.join('')}
        ${dayButtons.join('')}
    </div>`;
}

function formatDateValue(
    precision: DatePrecision,
    state: PickerState,
    displayFormat: DateDisplayFormat,
): string {
    const year = String(clampYear(state.year)).padStart(4, '0');
    if (precision === 'year') return year;
    const month = String(clampMonth(state.month)).padStart(2, '0');
    if (displayFormat === 'iso') {
        if (precision === 'month') return `${year}-${month}`;
        const day = String(state.day).padStart(2, '0');
        return `${year}-${month}-${day}`;
    }
    const monthName = monthLabel(state.month, 'long');
    if (precision === 'month') return `${monthName} ${year}`;
    const day = String(state.day).padStart(2, '0');
    if (displayFormat === 'month-day-year') {
        return `${monthName} ${Number(day)}, ${year}`;
    }
    return `${Number(day)} ${monthName} ${year}`;
}

function shiftMonth(state: PickerState, delta: number): void {
    const shifted = new Date(Date.UTC(state.year, state.month - 1 + delta, 1));
    state.year = clampYear(shifted.getUTCFullYear());
    state.month = clampMonth(shifted.getUTCMonth() + 1);
    state.day = Math.min(state.day, daysInMonth(state.year, state.month));
}

function yearRangeLabel(centerYear: number): string {
    return `${centerYear - 5}-${centerYear + 6}`;
}

function monthLabel(month: number, length: 'short' | 'long'): string {
    return MONTHS[clampMonth(month) - 1][length];
}

function clampYear(year: number): number {
    if (!Number.isFinite(year)) return new Date().getFullYear();
    return Math.min(Math.max(Math.trunc(year), 1000), 9999);
}

function clampMonth(month: number): number {
    if (!Number.isFinite(month)) return 1;
    return Math.min(Math.max(Math.trunc(month), 1), 12);
}

function daysInMonth(year: number, month: number): number {
    return new Date(Date.UTC(year, month, 0)).getUTCDate();
}

function validDay(year: number, month: number, day: number): boolean {
    if (!Number.isInteger(day) || day < 1 || day > 31) return false;
    const d = new Date(Date.UTC(year, month - 1, day));
    return d.getUTCFullYear() === year && d.getUTCMonth() === month - 1 && d.getUTCDate() === day;
}

export function datePickerButton(id: string): string {
    return `<button type="button" id="${escapeHtml(id)}" class="date-picker-trigger" aria-label="Choose date" title="Choose date">${icon('calendar_month', 18)}</button>`;
}
