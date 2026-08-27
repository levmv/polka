import { bootReader } from './boot';
import { showReaderError } from './controls';
import { initPDFReader } from './pdf-reader';
import { handleReadingStatusChange } from './reading-status';

bootReader(() => {
    const page = document.querySelector<HTMLElement>('.reader-page');
    const assetId = page?.dataset.readerAssetId;
    if (!page || !assetId) return;

    initPDFReader(page, assetId, { onStateSaved: handleReadingStatusChange }).catch((error) => {
        console.error('Failed to initialize PDF reader:', error);
        showReaderError(page, 'Could not open this PDF.');
    });
});
