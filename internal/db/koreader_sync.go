package db

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/maphash"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/levmv/polka/internal/position"
)

var ErrKOReaderInvalidInput = errors.New("invalid koreader input")

type KOReaderProgress struct {
	DocumentHash string
	Position     string
	Progress     float64
	DeviceID     string
	DeviceName   string
}

// KOReaderState is the native KOSync response, whether the book is in the library or not.
type KOReaderState struct {
	Position   string
	Progress   float64
	DeviceID   string
	DeviceName string
	UpdatedAt  int64
}

func (state *ReaderState) KOReaderState() *KOReaderState {
	deviceID := state.DeviceID
	if raw, ok := strings.CutPrefix(deviceID, "urn:koreader:"); ok {
		deviceID, _ = url.QueryUnescape(raw)
	}
	return &KOReaderState{Position: state.KOReaderPosition, Progress: state.Progress,
		DeviceID: deviceID, DeviceName: state.DeviceName, UpdatedAt: state.UpdatedAt}
}

// SaveKOReaderProgress saves a position and advances a matched book's reading status.
// Unchanged uploads and echoes preserve the saved observation.
func (db *DB) SaveKOReaderProgress(ctx context.Context, userID int64, input KOReaderProgress) (*KOReaderState, error) {
	input, err := normalizeKOReaderProgress(userID, input)
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Keep successful commits and cache updates in the same order. The writer
	// is acquired first, so waiting native requests retain its normal deadline.
	cache := &db.koReaderInputs
	cache.mu.Lock()
	defer cache.mu.Unlock()

	target, err := ResolveKOReaderHash(tx, input.DocumentHash)
	if err != nil {
		return nil, err
	}
	current, adopting, err := loadKOReaderPosition(tx, userID, target.AssetID, input.DocumentHash)
	if err != nil {
		return nil, err
	}
	device := input.DeviceID
	if device == "" {
		device = input.DeviceName
	}
	key := koReaderInputKey{userID, input.DocumentHash, device}
	address := position.KOReaderKey(input.Position)
	fingerprint := cache.fingerprint(address)
	repeated := current != nil && cache.repeated(key, fingerprint)
	echo := current != nil && position.KOReaderKey(current.Position) == address
	changed := !repeated && !echo
	if changed {
		current = &KOReaderState{Position: input.Position, Progress: input.Progress,
			DeviceID: input.DeviceID, DeviceName: input.DeviceName, UpdatedAt: time.Now().Unix()}
	}
	if changed || adopting {
		if target.AssetID == 0 {
			err = saveExternalKOReaderState(tx, userID, input.DocumentHash, current)
		} else {
			err = saveKOReaderAssetPosition(tx, userID, target.AssetID, input.DocumentHash, current)
		}
		if err != nil {
			return nil, err
		}
		if target.BookID != 0 {
			if _, err := advanceReadingStatus(tx, userID, target.BookID, current.Progress, ReadingStatusSourceKOSync); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	cache.remember(key, fingerprint)
	return current, nil
}

// A known asset's position, including a reset, takes precedence over the fallback.
// The flag marks an external position to adopt on the asset's first native upload.
func loadKOReaderPosition(queryer Queryer, userID, assetID int64, document string) (*KOReaderState, bool, error) {
	if assetID != 0 {
		state, err := GetReaderState(queryer, userID, assetID)
		if err != nil {
			return nil, false, err
		}
		if state.Revision > 0 {
			return state.KOReaderState(), false, nil
		}
	}
	external, err := GetExternalKOReaderState(queryer, userID, document)
	return external, assetID != 0 && external != nil, err
}

func saveKOReaderAssetPosition(tx *Tx, userID, assetID int64, document string, state *KOReaderState) error {
	_, err := tx.Exec(`INSERT INTO reading_positions
        (user_id, asset_id, koreader_position, progress, revision, device_id, device_name, updated_at)
        VALUES (?, ?, ?, ?, 1, ?, ?, ?)
        ON CONFLICT(user_id, asset_id) DO UPDATE SET
            koreader_position = excluded.koreader_position, progress = excluded.progress,
            locator = '{}', revision = reading_positions.revision + 1,
            device_id = excluded.device_id, device_name = excluded.device_name, updated_at = excluded.updated_at`,
		userID, assetID, state.Position, state.Progress,
		"urn:koreader:"+url.QueryEscape(state.DeviceID), state.DeviceName, state.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save KOReader asset position: %w", err)
	}
	return deleteExternalKOReaderState(tx, userID, document)
}

// KOReaderHashTarget identifies a unique live asset and/or book for a document.
// Each ID is zero when unknown or ambiguous. Multiple assets of one book have
// a BookID but no AssetID.
type KOReaderHashTarget struct {
	AssetID int64
	BookID  int64
}

func ResolveKOReaderHash(queryer Queryer, documentHash string) (KOReaderHashTarget, error) {
	hash, err := hex.DecodeString(strings.TrimSpace(documentHash))
	if err != nil || len(hash) != 16 {
		return KOReaderHashTarget{}, nil
	}
	var target KOReaderHashTarget
	err = queryer.QueryRow(`
		SELECT CASE WHEN COUNT(*) = 1 THEN MIN(a.id) ELSE 0 END,
		       CASE WHEN COUNT(DISTINCT a.book_id) = 1 THEN MIN(a.book_id) ELSE 0 END
		FROM koreader_hashes h
		JOIN assets a ON a.id = h.asset_id
		JOIN books b ON b.id = a.book_id
		WHERE h.hash = ? AND b.deleted_at IS NULL`, hash).Scan(&target.AssetID, &target.BookID)
	if err != nil {
		return KOReaderHashTarget{}, fmt.Errorf("resolve KOReader hash: %w", err)
	}
	return target, nil
}

// CacheAssetKOReaderHash records an original download with a short writer
// deadline. The downloaded hash is kept even after a concurrent replacement;
// the current-file cache is updated only while the expected SHA-256 matches.
func (db *DB) CacheAssetKOReaderHash(ctx context.Context, assetID int64, currentSHA256 []byte, hash string) error {
	_, err := bestEffortWrite(ctx, func(writeCtx context.Context) error {
		return db.Transact(writeCtx, func(tx *Tx) error {
			_, err := tx.Exec(`
				UPDATE assets SET koreader_hash = ?
				WHERE id = ? AND current_sha256 = ?
				  AND (koreader_hash IS NULL OR koreader_hash = '')
			`, hash, assetID, currentSHA256)
			if err != nil {
				return err
			}
			return rememberKOReaderHash(tx, assetID, hash)
		})
	})
	if err != nil {
		return fmt.Errorf("cache asset koreader hash: %w", err)
	}
	return nil
}

const rememberKOReaderHashSQL = `INSERT INTO koreader_hashes(asset_id, hash, conversion) VALUES (?, ?, ?)
    ON CONFLICT(asset_id, hash) DO NOTHING`

// RememberKOReaderHash records a converted download's hash and format with a
// short writer deadline, leaving the original file's cache unchanged.
func (db *DB) RememberKOReaderHash(ctx context.Context, assetID int64, hash, conversion string) error {
	decoded, err := hex.DecodeString(hash)
	if err != nil {
		return fmt.Errorf("decode asset KOReader hash: %w", err)
	}
	_, err = db.ExecBestEffort(ctx, rememberKOReaderHashSQL, assetID, decoded, conversion)
	return err
}

func rememberKOReaderHash(tx *Tx, assetID int64, hash string) error {
	decoded, err := hex.DecodeString(hash)
	if err != nil {
		return fmt.Errorf("decode asset KOReader hash: %w", err)
	}
	_, err = tx.Exec(rememberKOReaderHashSQL, assetID, decoded, "")
	return err
}

func normalizeKOReaderProgress(userID int64, p KOReaderProgress) (KOReaderProgress, error) {
	p.DocumentHash = strings.TrimSpace(p.DocumentHash)
	p.Position = strings.TrimSpace(p.Position)
	p.DeviceName = strings.TrimSpace(p.DeviceName)
	p.DeviceID = strings.TrimSpace(p.DeviceID)
	if userID <= 0 || p.DocumentHash == "" || p.Position == "" || p.DeviceName == "" ||
		!(p.Progress >= 0 && p.Progress <= 1) ||
		len(p.DocumentHash) > 256 || len(p.Position) > 4096 || len(p.DeviceName) > 256 || len(p.DeviceID) > 256 {
		return p, ErrKOReaderInvalidInput
	}
	return p, nil
}

type koReaderInputKey struct {
	userID   int64
	document string
	device   string
}

type koReaderInput struct {
	key  koReaderInputKey
	hash uint64
}

// Remember each device's last upload to ignore repeats after another reader saves.
// Entries do not expire while devices sleep. Eviction or restart loses duplicate
// detection, but the saved position remains in SQLite.
type koReaderInputCache struct {
	mu      sync.Mutex
	hasher  maphash.Hash
	entries []koReaderInput
}

func (c *koReaderInputCache) fingerprint(address string) uint64 {
	c.hasher.Reset()
	c.hasher.WriteString(address)
	return c.hasher.Sum64()
}

func (c *koReaderInputCache) repeated(key koReaderInputKey, hash uint64) bool {
	for _, entry := range c.entries {
		if entry.key == key {
			return entry.hash == hash
		}
	}
	return false
}

func (c *koReaderInputCache) remember(key koReaderInputKey, hash uint64) {
	for i, entry := range c.entries {
		if entry.key == key {
			c.entries = slices.Delete(c.entries, i, i+1)
			break
		}
	}
	if len(c.entries) == 256 {
		c.entries = slices.Delete(c.entries, 0, 1)
	}
	c.entries = append(c.entries, koReaderInput{key, hash})
}
