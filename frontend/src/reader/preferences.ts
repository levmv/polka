import { saveReaderPreferences } from '../api';
import { clamp } from '../dom';
import { iconElement } from '../icons';
import type { ReaderFlow, ReaderPreferences } from '../types';
import { focusReaderSurface } from './controls';
import {
    applyFoliateDisplay,
    DEFAULT_READER_DISPLAY_STYLE,
    type FoliateViewElement,
    normalizeReaderDisplayStyle,
    readerDisplayPalette,
    setCurrentFoliateDocumentJustification,
} from './foliate-engine';

export { DEFAULT_READER_DISPLAY_STYLE, normalizeReaderDisplayStyle } from './foliate-engine';

export const DEFAULT_READER_FLOW: ReaderFlow = 'paginated';
export const DEFAULT_READER_FONT_SCALE = 0;
export const DEFAULT_READER_CUSTOM_COLUMN_WIDTH = 760;
export const DEFAULT_READER_CUSTOM_LINE_HEIGHT = 1.72;

export const DEFAULT_READER_PREFERENCES: ReaderPreferences = {
    epub_flow: DEFAULT_READER_FLOW,
    display_style: DEFAULT_READER_DISPLAY_STYLE,
    font_scale: DEFAULT_READER_FONT_SCALE,
    custom_column_width: DEFAULT_READER_CUSTOM_COLUMN_WIDTH,
    custom_line_height: DEFAULT_READER_CUSTOM_LINE_HEIGHT,
};

interface DisplayPanelControls {
    backdrop: HTMLButtonElement;
    panel: HTMLElement;
    toggle: HTMLButtonElement;
    styleButtons: HTMLButtonElement[];
    flowButtons: HTMLButtonElement[];
    smallerButton: HTMLButtonElement;
    largerButton: HTMLButtonElement;
    scaleValue: HTMLElement;
    customSection: HTMLElement;
    widthInput: HTMLInputElement;
    widthValue: HTMLElement;
    lineInput: HTMLInputElement;
    lineValue: HTMLElement;
}

export function wireReaderPreferences(
    page: HTMLElement,
    view: FoliateViewElement,
    initialPreferences: ReaderPreferences,
): void {
    const toggle = page.querySelector<HTMLButtonElement>('[data-reader-display-toggle]');
    if (!toggle) return;

    let savedPreferences = normalizeReaderPreferences(initialPreferences);
    let currentPreferences = savedPreferences;
    let saveSequence = 0;
    let saveChain = Promise.resolve(savedPreferences);
    applyReaderPreferences(page, view, currentPreferences);

    if (view.isFixedLayout) {
        toggle.hidden = true;
        return;
    }

    const controls = createDisplayPanel(page, toggle);
    renderDisplayPanel(page, view, controls, currentPreferences);

    toggle.addEventListener('click', () => {
        if (controls.panel.hidden) openDisplayPanel(page, controls);
        else closeDisplayPanel(page, controls, true);
    });
    controls.backdrop.addEventListener('click', () => closeDisplayPanel(page, controls, true));
    controls.panel
        .querySelector<HTMLButtonElement>('[data-reader-display-close]')
        ?.addEventListener('click', () => closeDisplayPanel(page, controls, true));
    document.addEventListener('keydown', (event) => {
        if (event.key !== 'Escape' || controls.panel.hidden) return;
        event.preventDefault();
        closeDisplayPanel(page, controls, true);
    });

    for (const button of controls.styleButtons) {
        button.addEventListener('click', () => {
            commitPreference({
                display_style: normalizeReaderDisplayStyle(button.dataset.readerStyleOption),
            });
        });
    }
    for (const button of controls.flowButtons) {
        button.addEventListener('click', () => {
            commitPreference({ epub_flow: normalizeReaderFlow(button.dataset.readerFlowOption) });
        });
    }
    controls.smallerButton.addEventListener('click', () => {
        commitPreference({ font_scale: clamp(currentPreferences.font_scale - 1, -4, 6) });
    });
    controls.largerButton.addEventListener('click', () => {
        commitPreference({ font_scale: clamp(currentPreferences.font_scale + 1, -4, 6) });
    });
    controls.widthInput.addEventListener('input', () => {
        previewPreference({ custom_column_width: readNumber(controls.widthInput, 760) });
    });
    controls.widthInput.addEventListener('change', () => {
        commitPreference({ custom_column_width: readNumber(controls.widthInput, 760) });
    });
    controls.lineInput.addEventListener('input', () => {
        previewPreference({ custom_line_height: readNumber(controls.lineInput, 1.72) });
    });
    controls.lineInput.addEventListener('change', () => {
        commitPreference({ custom_line_height: readNumber(controls.lineInput, 1.72) });
    });

    function previewPreference(partial: Partial<ReaderPreferences>): void {
        currentPreferences = normalizeReaderPreferences({ ...currentPreferences, ...partial });
        applyReaderPreferences(page, view, currentPreferences);
        renderDisplayPanel(page, view, controls, currentPreferences);
    }

    function commitPreference(partial: Partial<ReaderPreferences>): void {
        const sequence = ++saveSequence;
        currentPreferences = normalizeReaderPreferences({ ...currentPreferences, ...partial });
        applyReaderPreferences(page, view, currentPreferences);
        renderDisplayPanel(page, view, controls, currentPreferences);

        const request = saveChain
            .catch(() => savedPreferences)
            .then(() => saveReaderPreferences(partial));
        saveChain = request;

        request
            .then((saved) => {
                if (sequence !== saveSequence) return;
                savedPreferences = normalizeReaderPreferences(saved);
                currentPreferences = savedPreferences;
                applyReaderPreferences(page, view, currentPreferences);
                renderDisplayPanel(page, view, controls, currentPreferences);
            })
            .catch((e) => {
                if (sequence !== saveSequence) return;
                console.error('Failed to save reader preferences:', e);
                currentPreferences = savedPreferences;
                applyReaderPreferences(page, view, currentPreferences);
                renderDisplayPanel(page, view, controls, currentPreferences);
            });
    }
}

export function applyReaderPreferences(
    page: HTMLElement,
    view: FoliateViewElement,
    preferences: ReaderPreferences,
): void {
    const normalized = normalizeReaderPreferences(preferences);
    const palette = readerDisplayPalette(normalized.display_style);
    setReaderFlow(page, view, normalized.epub_flow);
    page.dataset.readerStyle = normalized.display_style;
    page.dataset.readerFontScale = String(normalized.font_scale);
    page.dataset.readerColumnWidth = String(normalized.custom_column_width);
    page.dataset.readerLineHeight = String(normalized.custom_line_height);
    page.style.setProperty('--reader-bg-color', palette.background);
    page.style.setProperty('--reader-text-color', palette.text);
    applyFoliateDisplay(view, normalized);
    setCurrentFoliateDocumentJustification(view, normalized.display_style !== 'original');
}

export function normalizeReaderPreferences(
    preferences: Partial<ReaderPreferences>,
): ReaderPreferences {
    return {
        epub_flow: normalizeReaderFlow(preferences.epub_flow),
        display_style: normalizeReaderDisplayStyle(preferences.display_style),
        font_scale: clamp(preferences.font_scale ?? DEFAULT_READER_FONT_SCALE, -4, 6),
        custom_column_width: clamp(
            preferences.custom_column_width ?? DEFAULT_READER_CUSTOM_COLUMN_WIDTH,
            560,
            920,
        ),
        custom_line_height: clamp(
            preferences.custom_line_height ?? DEFAULT_READER_CUSTOM_LINE_HEIGHT,
            1.2,
            2.2,
        ),
        updated_at: preferences.updated_at,
    };
}

export function normalizeReaderFlow(flow: unknown): ReaderFlow {
    return flow === 'scrolled' ? 'scrolled' : DEFAULT_READER_FLOW;
}

function setReaderFlow(page: HTMLElement, view: FoliateViewElement, flow: ReaderFlow): void {
    const normalized = normalizeReaderFlow(flow);
    view.renderer?.setAttribute('flow', normalized);
    page.dataset.readerFlow = normalized;
}

function createDisplayPanel(page: HTMLElement, toggle: HTMLButtonElement): DisplayPanelControls {
    const panelID = 'reader-display-panel';
    toggle.setAttribute('aria-controls', panelID);
    toggle.setAttribute('aria-expanded', 'false');

    const backdrop = document.createElement('button');
    backdrop.className = 'reader-display-backdrop';
    backdrop.type = 'button';
    backdrop.hidden = true;
    backdrop.tabIndex = -1;
    backdrop.setAttribute('aria-label', 'Close display settings');

    const panel = document.createElement('aside');
    panel.id = panelID;
    panel.className = 'reader-display-panel';
    panel.hidden = true;
    panel.setAttribute('aria-label', 'Display settings');

    const header = document.createElement('div');
    header.className = 'reader-display-header';
    const title = document.createElement('h2');
    title.className = 'reader-display-title';
    title.textContent = 'Display';
    const close = document.createElement('button');
    close.className = 'reader-display-close';
    close.type = 'button';
    close.title = 'Close display settings';
    close.dataset.readerDisplayClose = 'true';
    close.setAttribute('aria-label', 'Close display settings');
    close.append(iconElement('close'));
    header.append(title, close);

    const styleSection = createSection('Style');
    const styleGroup = createSegmentedGroup('Style');
    const styleButtons = [
        createOptionButton('Original', 'readerStyleOption', 'original'),
        createOptionButton('Paper', 'readerStyleOption', 'paper'),
        createOptionButton('Custom', 'readerStyleOption', 'custom'),
    ];
    styleGroup.append(...styleButtons);
    styleSection.append(styleGroup);

    const textSection = createSection('Text');
    const fontRow = document.createElement('div');
    fontRow.className = 'reader-display-font-row';
    const smallerButton = document.createElement('button');
    smallerButton.className = 'reader-display-font-btn reader-display-font-btn--small';
    smallerButton.type = 'button';
    smallerButton.textContent = 'A';
    smallerButton.setAttribute('aria-label', 'Smaller text');
    const scaleValue = document.createElement('span');
    scaleValue.className = 'reader-display-scale';
    scaleValue.setAttribute('aria-live', 'polite');
    const largerButton = document.createElement('button');
    largerButton.className = 'reader-display-font-btn reader-display-font-btn--large';
    largerButton.type = 'button';
    largerButton.textContent = 'A';
    largerButton.setAttribute('aria-label', 'Larger text');
    fontRow.append(smallerButton, scaleValue, largerButton);
    textSection.append(fontRow);

    const flowSection = createSection('Flow');
    const flowGroup = createSegmentedGroup('Flow');
    const flowButtons = [
        createOptionButton('Paged', 'readerFlowOption', 'paginated'),
        createOptionButton('Scroll', 'readerFlowOption', 'scrolled'),
    ];
    flowGroup.append(...flowButtons);
    flowSection.append(flowGroup);

    const customSection = createSection('Custom');
    customSection.classList.add('reader-display-custom');
    const width = createRangeRow('Width', 'readerWidthInput', '560', '920', '20');
    const line = createRangeRow('Line', 'readerLineInput', '1.2', '2.2', '0.05');
    customSection.append(width.row, line.row);

    panel.append(header, styleSection, textSection, flowSection, customSection);
    page.append(backdrop, panel);

    return {
        backdrop,
        panel,
        toggle,
        styleButtons,
        flowButtons,
        smallerButton,
        largerButton,
        scaleValue,
        customSection,
        widthInput: width.input,
        widthValue: width.value,
        lineInput: line.input,
        lineValue: line.value,
    };
}

function renderDisplayPanel(
    page: HTMLElement,
    view: FoliateViewElement,
    controls: DisplayPanelControls,
    preferences: ReaderPreferences,
): void {
    for (const button of controls.styleButtons) {
        const selected = button.dataset.readerStyleOption === preferences.display_style;
        setOptionSelected(button, selected);
    }
    for (const button of controls.flowButtons) {
        const selected = button.dataset.readerFlowOption === preferences.epub_flow;
        setOptionSelected(button, selected);
    }

    controls.smallerButton.disabled = preferences.font_scale <= -4;
    controls.largerButton.disabled = preferences.font_scale >= 6;
    controls.scaleValue.textContent =
        preferences.font_scale === 0
            ? '100%'
            : `${Math.round((1 + preferences.font_scale * 0.06) * 100)}%`;

    controls.customSection.hidden = preferences.display_style !== 'custom';
    controls.widthInput.value = String(preferences.custom_column_width);
    controls.widthValue.textContent = `${preferences.custom_column_width}px`;
    controls.lineInput.value = preferences.custom_line_height.toFixed(2);
    controls.lineValue.textContent = preferences.custom_line_height.toFixed(2);

    page.dataset.readerFlow = preferences.epub_flow;
    page.dataset.readerStyle = preferences.display_style;
    page.dataset.readerFontScale = String(preferences.font_scale);
    page.dataset.readerColumnWidth = String(preferences.custom_column_width);
    page.dataset.readerLineHeight = String(preferences.custom_line_height);
    if (!view.isFixedLayout) controls.toggle.hidden = false;
}

function createSection(titleText: string): HTMLElement {
    const section = document.createElement('section');
    section.className = 'reader-display-section';
    const title = document.createElement('h3');
    title.className = 'reader-display-label';
    title.textContent = titleText;
    section.append(title);
    return section;
}

function createSegmentedGroup(label: string): HTMLElement {
    const group = document.createElement('div');
    group.className = 'reader-display-segmented';
    group.setAttribute('aria-label', label);
    group.setAttribute('role', 'group');
    return group;
}

function createOptionButton(label: string, dataKey: string, value: string): HTMLButtonElement {
    const button = document.createElement('button');
    button.className = 'reader-display-option';
    button.type = 'button';
    button.textContent = label;
    button.dataset[dataKey] = value;
    return button;
}

function createRangeRow(
    labelText: string,
    dataKey: string,
    min: string,
    max: string,
    step: string,
): { row: HTMLElement; input: HTMLInputElement; value: HTMLElement } {
    const row = document.createElement('label');
    row.className = 'reader-display-range-row';
    const label = document.createElement('span');
    label.className = 'reader-display-range-label';
    label.textContent = labelText;
    const input = document.createElement('input');
    input.type = 'range';
    input.min = min;
    input.max = max;
    input.step = step;
    input.dataset[dataKey] = 'true';
    const value = document.createElement('span');
    value.className = 'reader-display-range-value';
    row.append(label, input, value);
    return { row, input, value };
}

function openDisplayPanel(page: HTMLElement, controls: DisplayPanelControls): void {
    page.classList.add('reader-display-open');
    controls.panel.hidden = false;
    controls.backdrop.hidden = false;
    controls.toggle.setAttribute('aria-expanded', 'true');
    controls.panel
        .querySelector<HTMLElement>('.reader-display-option, .reader-display-close')
        ?.focus();
}

function closeDisplayPanel(
    page: HTMLElement,
    controls: DisplayPanelControls,
    restoreFocus: boolean,
): void {
    page.classList.remove('reader-display-open');
    controls.panel.hidden = true;
    controls.backdrop.hidden = true;
    controls.toggle.setAttribute('aria-expanded', 'false');
    if (restoreFocus) focusReaderSurface(page);
}

function setOptionSelected(button: HTMLButtonElement, selected: boolean): void {
    button.classList.toggle('active', selected);
    button.setAttribute('aria-pressed', selected ? 'true' : 'false');
}

function readNumber(input: HTMLInputElement, fallback: number): number {
    const value = Number(input.value);
    return Number.isFinite(value) ? value : fallback;
}
