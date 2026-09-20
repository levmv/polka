import { DjvuError } from '../../../internal/format/djvu/vendor/decoder.mjs';
import { bootReader } from './boot';
import { showReaderError } from './chrome';
import { initDjvuReader } from './djvu-reader';
import { handleReadingStatusChange } from './reading-status';

bootReader(() => {
    const page = document.querySelector<HTMLElement>('.reader-page');
    const assetId = Number(page?.dataset.readerAssetId);
    if (!page || !assetId) return;

    initDjvuReader(page, assetId, { onPositionSaved: handleReadingStatusChange }).catch((error) => {
        console.error('Failed to initialize DjVu reader:', error);
        showReaderError(
            page,
            error instanceof Error && !(error instanceof DjvuError)
                ? error.message
                : 'Could not open this DjVu. Download it to try another reader.',
        );
    });
});
