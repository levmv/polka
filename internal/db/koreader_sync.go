package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrKOReaderInvalidInput     = errors.New("invalid koreader input")
	ErrKOReaderProgressNotFound = errors.New("koreader progress not found")
)

type KOReaderProgress struct {
	UserID       int64
	DocumentHash string
	Progress     string
	Percentage   float64
	Device       string
	DeviceID     string
	UpdatedAt    int64
}

// KOReaderHashTarget is the catalog meaning of one provider-owned document
// hash. A zero BookID means the hash is not known to the catalog. Ambiguous
// means matching live assets belong to more than one book; callers may retain
// the hash-scoped KOSync record, but must not infer access or reading state for
// an arbitrary book. Multiple matching assets of one live book remain
// unambiguous; assets in Trash do not define catalog identity.
type KOReaderHashTarget struct {
	AssetID   int64
	BookID    int64
	Ambiguous bool
}

// CacheAssetKOReaderHash uses a short writer deadline for download bookkeeping.
// It ignores hashes computed for a concurrently replaced file.
func (db *DB) CacheAssetKOReaderHash(ctx context.Context, assetID int64, currentSHA256 []byte, hash string) error {
	_, err := db.ExecBestEffort(ctx, `
		UPDATE assets SET koreader_hash = ?
		WHERE id = ? AND current_sha256 = ?
		  AND (koreader_hash IS NULL OR koreader_hash = '')
	`, hash, assetID, currentSHA256)
	if err != nil {
		return fmt.Errorf("cache asset koreader hash: %w", err)
	}
	return nil
}

func ResolveKOReaderHash(queryer Queryer, documentHash string) (KOReaderHashTarget, error) {
	documentHash = strings.TrimSpace(documentHash)
	if documentHash == "" {
		return KOReaderHashTarget{}, nil
	}
	rows, err := queryer.Query(`
		SELECT MIN(a.id), a.book_id
		FROM assets a
		JOIN books b ON b.id = a.book_id
		WHERE a.koreader_hash = ? AND b.deleted_at IS NULL
		GROUP BY a.book_id
		ORDER BY a.book_id
		LIMIT 2
	`, documentHash)
	if err != nil {
		return KOReaderHashTarget{}, fmt.Errorf("resolve asset by koreader hash: %w", err)
	}
	defer rows.Close()

	var target KOReaderHashTarget
	for rows.Next() {
		var assetID int64
		var bookID int64
		if err := rows.Scan(&assetID, &bookID); err != nil {
			return KOReaderHashTarget{}, fmt.Errorf("scan asset by koreader hash: %w", err)
		}
		if target.BookID == 0 {
			target.AssetID = assetID
			target.BookID = bookID
			continue
		}
		target.AssetID = 0
		target.BookID = 0
		target.Ambiguous = true
		return target, nil
	}
	if err := rows.Err(); err != nil {
		return KOReaderHashTarget{}, fmt.Errorf("assets by koreader hash: %w", err)
	}
	return target, nil
}

func (db *DB) SaveKOReaderProgressAndAdvanceStatus(
	ctx context.Context,
	userID int64,
	progress KOReaderProgress,
) (*KOReaderProgress, ReadingStatusChange, error) {
	progress, err := normalizeKOReaderProgress(userID, progress)
	if err != nil {
		return nil, ReadingStatusChange{}, err
	}

	var saved *KOReaderProgress
	var change ReadingStatusChange
	err = db.Transact(ctx, func(tx *Tx) error {
		var err error
		saved, err = saveKOReaderProgress(tx, progress)
		if err != nil {
			return err
		}
		change, err = advanceReadingStatusForDocumentHash(tx, progress.UserID, progress.DocumentHash, progress.Percentage)
		return err
	})
	if err != nil {
		return nil, ReadingStatusChange{}, err
	}
	return saved, change, nil
}

func normalizeKOReaderProgress(userID int64, progress KOReaderProgress) (KOReaderProgress, error) {
	progress.UserID = userID
	progress.DocumentHash = strings.TrimSpace(progress.DocumentHash)
	progress.Progress = strings.TrimSpace(progress.Progress)
	progress.Device = strings.TrimSpace(progress.Device)
	progress.DeviceID = strings.TrimSpace(progress.DeviceID)
	if err := validateKOReaderProgress(progress); err != nil {
		return KOReaderProgress{}, err
	}
	return progress, nil
}

func saveKOReaderProgress(tx *Tx, progress KOReaderProgress) (*KOReaderProgress, error) {
	if _, err := tx.Exec(`
		INSERT INTO koreader_progress
			(user_id, document_hash, progress, percentage, device, device_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, unixepoch())
		ON CONFLICT(user_id, document_hash) DO UPDATE SET
			progress = excluded.progress,
			percentage = excluded.percentage,
			device = excluded.device,
			device_id = excluded.device_id,
			updated_at = unixepoch()
	`, progress.UserID, progress.DocumentHash, progress.Progress, progress.Percentage, progress.Device, progress.DeviceID); err != nil {
		return nil, fmt.Errorf("save koreader progress: %w", err)
	}
	return GetKOReaderProgress(tx, progress.UserID, progress.DocumentHash)
}

func GetKOReaderProgress(queryer Queryer, userID int64, documentHash string) (*KOReaderProgress, error) {
	documentHash = strings.TrimSpace(documentHash)
	if userID <= 0 {
		return nil, errorWithDetail(ErrKOReaderInvalidInput, "user id required")
	}
	if documentHash == "" {
		return nil, errorWithDetail(ErrKOReaderInvalidInput, "document hash required")
	}

	p := &KOReaderProgress{UserID: userID, DocumentHash: documentHash}
	err := queryer.QueryRow(`
		SELECT progress, percentage, device, device_id, updated_at
		FROM koreader_progress
		WHERE user_id = ? AND document_hash = ?
	`, userID, documentHash).Scan(&p.Progress, &p.Percentage, &p.Device, &p.DeviceID, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrKOReaderProgressNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get koreader progress: %w", err)
	}
	return p, nil
}

func validateKOReaderProgress(p KOReaderProgress) error {
	if p.UserID <= 0 {
		return errorWithDetail(ErrKOReaderInvalidInput, "user id required")
	}
	if p.DocumentHash == "" {
		return errorWithDetail(ErrKOReaderInvalidInput, "document hash required")
	}
	if p.Progress == "" {
		return errorWithDetail(ErrKOReaderInvalidInput, "progress required")
	}
	if p.Device == "" {
		return errorWithDetail(ErrKOReaderInvalidInput, "device required")
	}
	if p.Percentage < 0 || p.Percentage > 1 {
		return errorWithDetail(ErrKOReaderInvalidInput, "percentage must be between 0 and 1")
	}
	if len(p.DocumentHash) > 256 || len(p.Progress) > 4096 || len(p.Device) > 256 || len(p.DeviceID) > 256 {
		return errorWithDetail(ErrKOReaderInvalidInput, "koreader field too long")
	}
	return nil
}
