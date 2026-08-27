package cli

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

type writebackRepairSummary struct {
	Finalized          int
	Replaced           int
	Cleared            int
	Unrecoverable      int
	Errors             int
	OrphanTempsRemoved int
}

func writebackAttemptReports(attempts []db.MetadataWritebackAttemptRow) []string {
	reports := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		reports = append(reports, fmt.Sprintf("%s: rev %d final=%s temp=%s", attempt.AssetID, attempt.MetadataRev, attempt.StoragePath, attempt.TempPath))
	}
	return reports
}

func writebackTempPaths(root storage.Root, attempts []db.MetadataWritebackAttemptRow) map[string]bool {
	tempAbs := make(map[string]bool, len(attempts))
	for _, attempt := range attempts {
		if abs, err := root.Resolve(attempt.TempPath); err == nil {
			tempAbs[abs] = true
		}
	}
	return tempAbs
}

func repairMetadataWritebackAttempts(ctx context.Context, database *db.DB, root storage.Root) (writebackRepairSummary, error) {
	var summary writebackRepairSummary
	attempts, err := db.ListMetadataWritebackAttempts(database)
	if err != nil {
		return summary, err
	}
	for _, attempt := range attempts {
		if err := context.Cause(ctx); err != nil {
			return summary, err
		}
		note, err := repairMetadataWritebackAttempt(ctx, database, root, attempt)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return summary, cause
			}
			fmt.Printf("Failed to repair write-back attempt for %s: %v\n", attempt.AssetID, err)
			summary.Errors++
			continue
		}
		switch note {
		case "finalized":
			summary.Finalized++
		case "replaced":
			summary.Replaced++
		case "cleared":
			summary.Cleared++
		case "failed":
			summary.Unrecoverable++
		}
	}

	// Attempts repaired above may have been cleared. Reload before deciding which
	// temp files are still live; the initial slice is intentionally stale here.
	if err := context.Cause(ctx); err != nil {
		return summary, err
	}
	attempts, err = db.ListMetadataWritebackAttempts(database)
	if err != nil {
		return summary, err
	}
	pendingTemps := writebackTempPaths(root, attempts)
	removed, err := removeOrphanWritebackTemps(ctx, root, pendingTemps)
	if err != nil {
		return summary, err
	}
	summary.OrphanTempsRemoved = removed
	return summary, nil
}

func repairMetadataWritebackAttempt(ctx context.Context, database *db.DB, root storage.Root, attempt db.MetadataWritebackAttemptRow) (string, error) {
	if attempt.CurrentStoragePath != attempt.StoragePath {
		if err := removeWritebackAttemptTemp(root, attempt.TempPath); err != nil {
			return "", err
		}
		if err := db.ClearMetadataWritebackAttempt(database, attempt.AssetID); err != nil {
			return "", err
		}
		return "cleared", nil
	}

	finalMatches, err := pathMatchesWritebackAttempt(ctx, root, attempt.StoragePath, attempt.SHA256, attempt.Size)
	if err != nil {
		return "", err
	}
	if finalMatches {
		if err := markWritebackAttemptSuccess(ctx, database, attempt); err != nil {
			return "", err
		}
		_ = removeWritebackAttemptTemp(root, attempt.TempPath)
		return "finalized", nil
	}

	tempMatches, err := pathMatchesWritebackAttempt(ctx, root, attempt.TempPath, attempt.SHA256, attempt.Size)
	if err != nil {
		return "", err
	}
	if tempMatches {
		if err := storage.ReplaceWithStaged(root, attempt.TempPath, attempt.StoragePath); err != nil {
			return "", err
		}
		if err := markWritebackAttemptSuccess(ctx, database, attempt); err != nil {
			return "", err
		}
		return "replaced", nil
	}

	_ = removeWritebackAttemptTemp(root, attempt.TempPath)
	err = database.Transact(ctx, func(tx *sql.Tx) error {
		if err := db.MarkMetadataWritebackError(tx, attempt.AssetID, fmt.Errorf("pending write-back attempt cannot be recovered: final and temp do not match recorded hash")); err != nil {
			return err
		}
		return db.ClearMetadataWritebackAttempt(tx, attempt.AssetID)
	})
	if err != nil {
		return "", err
	}
	return "failed", nil
}

func markWritebackAttemptSuccess(ctx context.Context, database *db.DB, attempt db.MetadataWritebackAttemptRow) error {
	return database.Transact(ctx, func(tx *sql.Tx) error {
		return db.MarkMetadataWritebackSuccess(tx, attempt.AssetID, attempt.StoragePath, attempt.SHA256, attempt.Size, attempt.KOReaderHash, attempt.MetadataRev)
	})
}

func pathMatchesWritebackAttempt(ctx context.Context, root storage.Root, relPath, wantHash string, wantSize int64) (bool, error) {
	absPath, err := root.Resolve(relPath)
	if err != nil {
		return false, nil
	}
	info, err := os.Stat(absPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", relPath, err)
	}
	if info.IsDir() || info.Size() != wantSize {
		return false, nil
	}
	gotHash, gotSize, err := fileSHA256AndSizeContext(ctx, absPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("hash %s: %w", relPath, err)
	}
	return gotSize == wantSize && gotHash == wantHash, nil
}

func removeWritebackAttemptTemp(root storage.Root, relPath string) error {
	absPath, err := root.Resolve(relPath)
	if err != nil {
		return nil
	}
	if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove write-back temp %s: %w", relPath, err)
	}
	return nil
}

func removeOrphanWritebackTemps(ctx context.Context, root storage.Root, pendingTemps map[string]bool) (int, error) {
	removed := 0
	err := storage.WalkBooks(root, func(path string, info os.FileInfo, err error) error {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !storage.IsWritebackTempFileName(info.Name()) || pendingTemps[path] {
			return nil
		}
		if err := os.Remove(path); err == nil {
			removed++
		} else if !os.IsNotExist(err) {
			return err
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return removed, fmt.Errorf("walk write-back temps: %w", err)
	}
	return removed, nil
}
