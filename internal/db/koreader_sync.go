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
	UserID       string
	DocumentHash string
	Progress     string
	Percentage   float64
	Device       string
	DeviceID     string
	UpdatedAt    int64
}

// KOReaderHashTarget is the catalog meaning of one provider-owned document
// hash. An empty WorkID means the hash is not known to the catalog. Ambiguous
// means matching live assets belong to more than one work; callers may retain
// the hash-scoped KOSync record, but must not infer access or reading state for
// an arbitrary work. Multiple matching assets of one live work remain
// unambiguous; assets in Trash do not define catalog identity.
type KOReaderHashTarget struct {
	AssetID   string
	WorkID    string
	Ambiguous bool
}

func SetAssetKOReaderHash(execer Execer, assetID, hash string) error {
	assetID = strings.TrimSpace(assetID)
	hash = strings.TrimSpace(hash)
	if assetID == "" || hash == "" {
		return nil
	}
	res, err := execer.Exec("UPDATE assets SET koreader_hash = ? WHERE id = ?", hash, assetID)
	if err != nil {
		return fmt.Errorf("set asset koreader hash: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func ResolveKOReaderHash(queryer Queryer, documentHash string) (KOReaderHashTarget, error) {
	documentHash = strings.TrimSpace(documentHash)
	if documentHash == "" {
		return KOReaderHashTarget{}, nil
	}
	rows, err := queryer.Query(`
		SELECT MIN(a.id), a.work_id
		FROM assets a
		JOIN works w ON w.id = a.work_id
		WHERE a.koreader_hash = ? AND w.deleted_at IS NULL
		GROUP BY a.work_id
		ORDER BY a.work_id
		LIMIT 2
	`, documentHash)
	if err != nil {
		return KOReaderHashTarget{}, fmt.Errorf("resolve asset by koreader hash: %w", err)
	}
	defer rows.Close()

	var target KOReaderHashTarget
	for rows.Next() {
		var assetID, workID string
		if err := rows.Scan(&assetID, &workID); err != nil {
			return KOReaderHashTarget{}, fmt.Errorf("scan asset by koreader hash: %w", err)
		}
		if target.WorkID == "" {
			target.AssetID = assetID
			target.WorkID = workID
			continue
		}
		target.AssetID = ""
		target.WorkID = ""
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
	userID string,
	progress KOReaderProgress,
) (*KOReaderProgress, ReadingStatusChange, error) {
	progress, err := normalizeKOReaderProgress(userID, progress)
	if err != nil {
		return nil, ReadingStatusChange{}, err
	}

	var saved *KOReaderProgress
	var change ReadingStatusChange
	err = db.Transact(ctx, func(tx *sql.Tx) error {
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

func normalizeKOReaderProgress(userID string, progress KOReaderProgress) (KOReaderProgress, error) {
	progress.UserID = strings.TrimSpace(userID)
	progress.DocumentHash = strings.TrimSpace(progress.DocumentHash)
	progress.Progress = strings.TrimSpace(progress.Progress)
	progress.Device = strings.TrimSpace(progress.Device)
	progress.DeviceID = strings.TrimSpace(progress.DeviceID)
	if err := validateKOReaderProgress(progress); err != nil {
		return KOReaderProgress{}, err
	}
	return progress, nil
}

func saveKOReaderProgress(tx *sql.Tx, progress KOReaderProgress) (*KOReaderProgress, error) {
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
	return getKOReaderProgress(tx, progress.UserID, progress.DocumentHash)
}

func (db *DB) GetKOReaderProgress(userID, documentHash string) (*KOReaderProgress, error) {
	return getKOReaderProgress(db, userID, documentHash)
}

func getKOReaderProgress(queryer Queryer, userID, documentHash string) (*KOReaderProgress, error) {
	userID = strings.TrimSpace(userID)
	documentHash = strings.TrimSpace(documentHash)
	if userID == "" {
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
	if p.UserID == "" {
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
