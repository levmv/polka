export function createRichEditor(
    initialHtml: string | null,
    initialRaw: string | null,
    onChange: (html: string) => void,
    onBlur: () => void,
): HTMLElement {
    const container = document.createElement('div');
    container.className = 'rich-editor-container form-input';

    const toolbar = document.createElement('div');
    toolbar.className = 'rich-editor-toolbar';

    const editor = document.createElement('div');
    editor.className = 'rich-editor-content';
    editor.contentEditable = 'true';

    if (initialHtml) {
        editor.innerHTML = initialHtml;
    } else if (initialRaw) {
        editor.textContent = initialRaw; // Use textContent to prevent raw XSS in DOM
    }

    const buttons = [
        { command: 'bold', icon: 'B', title: 'Bold (Ctrl+B)', style: 'font-weight: bold;' },
        { command: 'italic', icon: 'I', title: 'Italic (Ctrl+I)', style: 'font-style: italic;' },
        {
            command: 'underline',
            icon: 'U',
            title: 'Underline (Ctrl+U)',
            style: 'text-decoration: underline;',
        },
        { command: 'separator' },
        {
            command: 'insertUnorderedList',
            icon: '•',
            title: 'Bulleted List',
            style: 'font-weight: bold;',
        },
        {
            command: 'insertOrderedList',
            icon: '1.',
            title: 'Numbered List',
            style: 'font-weight: bold;',
        },
        { command: 'separator' },
        { command: 'createLink', icon: '🔗', title: 'Link' },
        { command: 'removeFormat', icon: '⌫', title: 'Clear formatting' },
    ];

    buttons.forEach((btn) => {
        if (btn.command === 'separator') {
            const sep = document.createElement('div');
            sep.className = 'rich-editor-separator';
            toolbar.appendChild(sep);
            return;
        }

        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'rich-editor-btn';
        if (btn.style) {
            button.innerHTML = `<span style="${btn.style}">${btn.icon}</span>`;
        } else {
            button.textContent = btn.icon as string;
        }
        button.title = btn.title as string;
        button.dataset.command = btn.command;

        button.addEventListener('mousedown', (e) => {
            e.preventDefault(); // Don't steal focus
        });

        button.addEventListener('click', (e) => {
            e.preventDefault();
            if (btn.command === 'createLink') {
                openLinkBar();
                return;
            }
            document.execCommand(btn.command as string, false, undefined);
            updateToolbarState();
            onChange(editor.innerHTML);
            editor.focus();
        });

        toolbar.appendChild(button);
    });

    // Inline link bar — a quiet replacement for blocking prompt()/alert(). The
    // editor selection is lost once the URL input takes focus, so we stash the
    // range when the bar opens and restore it before createLink.
    let savedRange: Range | null = null;
    const linkBar = document.createElement('div');
    linkBar.className = 'rich-editor-linkbar';
    linkBar.hidden = true;
    const linkInput = document.createElement('input');
    linkInput.type = 'text';
    linkInput.className = 'rich-editor-linkbar-input';
    linkInput.placeholder = 'https://… or mailto:…';
    const linkAdd = document.createElement('button');
    linkAdd.type = 'button';
    linkAdd.className = 'rich-editor-linkbar-add';
    linkAdd.textContent = 'Add link';
    const linkCancel = document.createElement('button');
    linkCancel.type = 'button';
    linkCancel.className = 'rich-editor-linkbar-cancel';
    linkCancel.textContent = 'Cancel';
    const linkError = document.createElement('div');
    linkError.className = 'rich-editor-linkbar-error';
    linkError.hidden = true;
    const linkFields = document.createElement('div');
    linkFields.className = 'rich-editor-linkbar-fields';
    linkFields.append(linkInput, linkAdd, linkCancel);
    linkBar.append(linkFields, linkError);

    function showLinkError(msg: string) {
        linkError.textContent = msg;
        linkError.hidden = false;
    }

    function openLinkBar() {
        const sel = window.getSelection();
        savedRange = sel && sel.rangeCount > 0 ? sel.getRangeAt(0).cloneRange() : null;
        linkError.hidden = true;
        linkInput.value = '';
        linkBar.hidden = false;
        if (
            savedRange &&
            (savedRange.collapsed || !editor.contains(savedRange.commonAncestorContainer))
        ) {
            showLinkError('Select some text in the description first, then add a link.');
        }
        linkInput.focus();
    }

    function closeLinkBar() {
        linkBar.hidden = true;
        savedRange = null;
        editor.focus();
    }

    function applyLink() {
        const url = linkInput.value.trim();
        if (!url) {
            closeLinkBar();
            return;
        }
        if (!/^(https?:\/\/|mailto:)/i.test(url)) {
            showLinkError('Link must start with http://, https:// or mailto:');
            return;
        }
        if (
            !savedRange ||
            savedRange.collapsed ||
            !editor.contains(savedRange.commonAncestorContainer)
        ) {
            showLinkError('Select some text in the description first, then add a link.');
            return;
        }
        const sel = window.getSelection();
        if (sel) {
            sel.removeAllRanges();
            sel.addRange(savedRange);
        }
        editor.focus();
        document.execCommand('createLink', false, url);
        linkBar.hidden = true;
        savedRange = null;
        updateToolbarState();
        onChange(editor.innerHTML);
    }

    linkAdd.addEventListener('click', (e) => {
        e.preventDefault();
        applyLink();
    });
    linkCancel.addEventListener('click', (e) => {
        e.preventDefault();
        closeLinkBar();
    });
    linkInput.addEventListener('keydown', (e) => {
        // Stop Enter/Escape from reaching the modal's document handler (which
        // would submit the form or close the whole modal) — these keys act on
        // the link bar only.
        if (e.key === 'Enter') {
            e.preventDefault();
            e.stopPropagation();
            applyLink();
        } else if (e.key === 'Escape') {
            e.preventDefault();
            e.stopPropagation();
            closeLinkBar();
        }
    });

    container.appendChild(toolbar);
    container.appendChild(linkBar);
    container.appendChild(editor);

    function updateToolbarState() {
        const btns = toolbar.querySelectorAll('.rich-editor-btn');
        btns.forEach((b) => {
            const btn = b as HTMLButtonElement;
            const cmd = btn.dataset.command;
            if (cmd && cmd !== 'createLink' && cmd !== 'removeFormat') {
                try {
                    const isActive = document.queryCommandState(cmd);
                    btn.classList.toggle('active', isActive);
                } catch (_e) {}
            }
        });
    }

    editor.addEventListener('keyup', updateToolbarState);
    editor.addEventListener('mouseup', updateToolbarState);

    editor.addEventListener('paste', (e) => {
        e.preventDefault();
        const text = e.clipboardData?.getData('text/plain') || '';
        document.execCommand('insertText', false, text);
    });

    editor.addEventListener('input', () => {
        onChange(editor.innerHTML);
    });

    editor.addEventListener('blur', () => {
        onBlur();
    });

    return container;
}
