package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/storage"
)

type coverRecoveryAsset struct {
	ID            string
	StoragePath   string
	Format        format.Format
	CurrentSHA256 string
	CurrentSize   sql.NullInt64
}

func runRepair(parent context.Context, dataDir string, args []string) (retErr error) {
	fs := commandFlagSet("repair", "polka repair [--force]")
	force := fs.Bool("force", false, "override a fresh writer lease from another polka process")
	if help, err := parseCommandFlags(fs, args); help || err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		fs.Usage()
		return reportedErrorf("usage: polka repair [--force]")
	}

	database, err := openDatabase(dataDir)
	if err != nil {
		return err
	}
	defer database.Close()
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		return err
	}
	root, err = requireStorageLayout(dataDir, root)
	if err != nil {
		return err
	}
	lease, err := acquireCLIWriterLease(parent, database, "repair", *force)
	if err != nil {
		return err
	}
	defer func() { retErr = lease.finish(retErr) }()
	ctx := lease.Context()

	// Covers live in the app data dir, separate from the books root.
	dataRoot := storage.NewRoot(dataDir)
	template, err := storage.OpenBookPathTemplate(database.DB)
	if err != nil {
		return err
	}

	writebackRepair, err := repairMetadataWritebackAttempts(ctx, database, root)
	if err != nil {
		return err
	}

	assets, err := db.AllAssetsWithPrimaryAuthor(database)
	if err != nil {
		return err
	}
	workCovers, err := db.AllWorkCovers(database)
	if err != nil {
		return err
	}

	assetRepair, err := repairAssets(ctx, database, root, template, assets)
	if err != nil {
		return err
	}
	coverRepair, err := repairCovers(ctx, database, root, dataRoot, workCovers, assetRepair.VerifiedHashes)
	if err != nil {
		return err
	}

	// Delete authors no longer referenced by any work.
	if err := context.Cause(ctx); err != nil {
		return err
	}
	orphanAuthors, err := db.DeleteOrphanAuthors(database)
	if err != nil {
		return fmt.Errorf("delete orphan authors: %w", err)
	}

	if err := pruneEmptyBookDirs(ctx, root); err != nil {
		return err
	}

	fmt.Println("Repair completed:")
	printRepairSummary([]repairSummaryItem{
		{"Relocated files:", assetRepair.Relocated},
		{"Hash recoveries:", assetRepair.HashRecovered},
		{"Fixed database paths:", assetRepair.FixedPaths},
		{"Invalid paths:", assetRepair.InvalidPaths},
		{"Size backfills:", assetRepair.SizeBackfilled},
		{"Original sizes:", assetRepair.OriginalSizeBackfilled},
		{"Size mismatches:", assetRepair.SizeMismatches},
		{"Unrecoverable:", assetRepair.Missing},
		{"Hash backfills:", assetRepair.HashBackfilled},
		{"Hash mismatches:", assetRepair.HashMismatches},
		{"Formats:", assetRepair.Formats},
		{"Reader capabilities:", assetRepair.ReaderCapabilities},
		{"Writes finalized:", writebackRepair.Finalized},
		{"Temporary files recovered:", writebackRepair.Replaced},
		{"Stale attempts cleared:", writebackRepair.Cleared},
		{"Orphan temporary files removed:", writebackRepair.OrphanTempsRemoved},
		{"Writes unrecoverable:", writebackRepair.Unrecoverable},
		{"Write-back repair errors:", writebackRepair.Errors},
		{"Covers restored:", coverRepair.Restored},
		{"Covers extracted:", coverRepair.Extracted},
		{"Covers fallback:", coverRepair.Fallback},
		{"Covers cleared:", coverRepair.VersionCleared},
		{"Orphan covers:", coverRepair.OrphanOriginalsRemoved},
		{"Staged covers:", coverRepair.StagedRemoved},
		{"Orphan authors:", int(orphanAuthors)},
	})
	if assetRepair.Missing > 0 || assetRepair.HashMismatches > 0 || assetRepair.SizeMismatches > 0 || writebackRepair.Unrecoverable > 0 || writebackRepair.Errors > 0 {
		fmt.Println()
		fmt.Println("Some issues require manual action. Run `polka check` for the full remaining report.")
		if assetRepair.Missing > 0 {
			fmt.Println("  - Missing/unrecoverable assets: re-import the original source or restore the file from backup.")
		}
		if assetRepair.HashMismatches > 0 || assetRepair.SizeMismatches > 0 {
			fmt.Println("  - Hash/size mismatches are left untouched; inspect the file before accepting or replacing it.")
		}
		if writebackRepair.Unrecoverable > 0 {
			fmt.Println("  - Unrecoverable metadata write-back attempts were cleared, and a persistent error was recorded on each asset.")
		}
		if writebackRepair.Errors > 0 {
			fmt.Println("  - Some metadata write-back attempts could not be repaired; their details are listed above.")
		}
		fmt.Println("  - Orphan book files and arbitrary staged files are not deleted automatically.")
	}

	return nil
}

type assetRepairResult struct {
	Relocated              int
	HashRecovered          int
	FixedPaths             int
	InvalidPaths           int
	SizeBackfilled         int
	OriginalSizeBackfilled int
	SizeMismatches         int
	Missing                int
	HashBackfilled         int
	HashMismatches         int
	Formats                int
	ReaderCapabilities     int
	VerifiedHashes         map[string]struct{}
}

func repairAssets(ctx context.Context, database *db.DB, root storage.Root, template string, assets []db.AssetWithAuthorRow) (assetRepairResult, error) {
	referencedPaths := make(map[string]bool, len(assets))
	for _, asset := range assets {
		if err := context.Cause(ctx); err != nil {
			return assetRepairResult{}, err
		}
		if absPath, err := root.Resolve(asset.StoragePath); err == nil {
			referencedPaths[absPath] = true
		}
	}
	recoverableByHash := newRecoverableAssetHashIndex(ctx, root, referencedPaths)
	recoverableByTag := newRecoverableAssetTagIndex(ctx, root)
	summary := assetRepairResult{
		VerifiedHashes: make(map[string]struct{}, len(assets)),
	}

	for _, asset := range assets {
		if err := context.Cause(ctx); err != nil {
			return summary, err
		}
		canonicalPath, err := storage.BookPath(template, assetBookPathData(asset))
		if err != nil {
			return summary, err
		}
		var foundAbsPath string
		actualFileAbsPath, pathErr := root.Resolve(asset.StoragePath)
		if pathErr != nil {
			fmt.Printf("Invalid database path: %s (%s): %v\n", asset.ID, asset.StoragePath, pathErr)
			summary.InvalidPaths++
		} else if _, err := os.Stat(actualFileAbsPath); err == nil {
			foundAbsPath = actualFileAbsPath
		}
		if foundAbsPath == "" {
			foundAbsPath = recoverableByTag.find(asset.ID)
		}
		if err := context.Cause(ctx); err != nil {
			return summary, err
		}
		recoveredByHash := false
		if foundAbsPath == "" && asset.CurrentSHA256 != "" {
			if match, err := recoverableByHash.find(asset.CurrentSHA256); err != nil {
				fmt.Printf("Failed to scan orphans for %s: %v\n", asset.ID, err)
			} else if match != "" {
				foundAbsPath = match
				recoveredByHash = true
			}
		}

		if foundAbsPath == "" {
			fmt.Printf("Missing/unrecoverable: %s\n", asset.ID)
			summary.Missing++
			continue
		}
		if err := context.Cause(ctx); err != nil {
			return summary, err
		}

		foundRelPath, err := filepath.Rel(root.Path, foundAbsPath)
		if err != nil {
			fmt.Printf("Failed to resolve found path for %s: %v\n", asset.ID, err)
			continue
		}
		// Compare and Move both want slash-separated relative paths (canonical
		// paths are slash-based); without ToSlash a Windows Rel always
		// mis-compares and Move fails Root.Resolve's backslash validation.
		foundRelPath = filepath.ToSlash(foundRelPath)

		if foundRelPath != canonicalPath {
			if err := storage.Move(root, foundRelPath, canonicalPath); err != nil {
				fmt.Printf("Failed to move %s: %v\n", asset.ID, err)
				continue
			}
			summary.Relocated++
			if recoveredByHash {
				summary.HashRecovered++
			}
			delete(referencedPaths, foundAbsPath)
			if finalAbs, err := root.Resolve(canonicalPath); err == nil {
				referencedPaths[finalAbs] = true
			}
		}

		if asset.StoragePath != canonicalPath {
			_, err = database.Exec("UPDATE assets SET storage_path = ?, filename = ? WHERE id = ?", canonicalPath, filepath.Base(canonicalPath), asset.ID)
			if err != nil {
				fmt.Printf("Failed to update database for %s: %v\n", asset.ID, err)
				continue
			}
			if foundRelPath == canonicalPath {
				summary.FixedPaths++
			}
		}

		finalAbsPath, err := root.Resolve(canonicalPath)
		if err != nil {
			fmt.Printf("Invalid canonical path for %s (%s): %v\n", asset.ID, canonicalPath, err)
			continue
		}
		currentHash, currentSize, err := fileSHA256AndSizeContext(ctx, finalAbsPath)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return summary, cause
			}
			fmt.Printf("Failed to hash %s: %v\n", asset.ID, err)
			continue
		}
		currentBytesTrusted := false
		if asset.CurrentSHA256 == "" {
			if _, err := database.Exec("UPDATE assets SET current_sha256 = ?, current_size = ?, updated_at = unixepoch() WHERE id = ?", currentHash, currentSize, asset.ID); err != nil {
				fmt.Printf("Failed to backfill current hash for %s: %v\n", asset.ID, err)
				continue
			}
			currentBytesTrusted = true
			summary.HashBackfilled++
			if !asset.CurrentSize.Valid || asset.CurrentSize.Int64 != currentSize {
				summary.SizeBackfilled++
			}
		} else if asset.CurrentSHA256 != currentHash {
			fmt.Printf("Hash mismatch: %s (%s)\n", asset.ID, canonicalPath)
			summary.HashMismatches++
		} else {
			currentBytesTrusted = true
		}
		if currentBytesTrusted {
			summary.VerifiedHashes[asset.ID] = struct{}{}
			capability, err := detectAssetReaderCapability(canonicalPath, finalAbsPath)
			if err != nil {
				fmt.Printf("Failed to recompute reader capability for %s: %v\n", asset.ID, err)
				continue
			}
			if capability.Format != asset.Format || capability.CanRead != asset.CanRead {
				canRead := 0
				if capability.CanRead {
					canRead = 1
				}
				if _, err := database.Exec("UPDATE assets SET format = ?, can_read = ?, updated_at = unixepoch() WHERE id = ?", format.FormatKey(capability.Format), canRead, asset.ID); err != nil {
					fmt.Printf("Failed to repair format/capability for %s: %v\n", asset.ID, err)
					continue
				}
				if capability.Format != asset.Format {
					summary.Formats++
				}
				if capability.CanRead != asset.CanRead {
					summary.ReaderCapabilities++
				}
			}
		}
		if asset.CurrentSHA256 != "" {
			if !asset.CurrentSize.Valid {
				if currentBytesTrusted {
					if _, err := database.Exec("UPDATE assets SET current_size = ?, updated_at = unixepoch() WHERE id = ?", currentSize, asset.ID); err != nil {
						fmt.Printf("Failed to backfill current size for %s: %v\n", asset.ID, err)
						continue
					}
					summary.SizeBackfilled++
				} else {
					fmt.Printf("Missing current size with hash mismatch: %s (%s)\n", asset.ID, canonicalPath)
					summary.SizeMismatches++
				}
			} else if asset.CurrentSize.Int64 != currentSize {
				if currentBytesTrusted {
					if _, err := database.Exec("UPDATE assets SET current_size = ?, updated_at = unixepoch() WHERE id = ?", currentSize, asset.ID); err != nil {
						fmt.Printf("Failed to repair current size for %s: %v\n", asset.ID, err)
						continue
					}
					summary.SizeBackfilled++
				} else {
					fmt.Printf("Size mismatch: %s (%s)\n", asset.ID, canonicalPath)
					summary.SizeMismatches++
				}
			}
		}
		if !asset.OriginalSize.Valid && asset.OriginalSHA256 != "" && asset.OriginalSHA256 == currentHash {
			if _, err := database.Exec("UPDATE assets SET original_size = ?, updated_at = unixepoch() WHERE id = ?", currentSize, asset.ID); err != nil {
				fmt.Printf("Failed to backfill original size for %s: %v\n", asset.ID, err)
				continue
			}
			summary.OriginalSizeBackfilled++
		}
	}

	return summary, nil
}

type coverRepairResult struct {
	Restored               int
	Extracted              int
	Fallback               int
	VersionCleared         int
	OrphanOriginalsRemoved int
	StagedRemoved          int
}

func repairCovers(ctx context.Context, database *db.DB, booksRoot, coverRoot storage.Root, works []db.WorkCoverRow, verifiedAssetHashes map[string]struct{}) (coverRepairResult, error) {
	recoverable := newRecoverableCoverIndex(ctx, coverRoot)
	summary := coverRepairResult{}

	// Covers are derived presentation data keyed by SQLite state: unlike book
	// assets, stale originals and impossible cover_version flags are safe to
	// clear so check converges back to the DB truth.
	expectedOriginals := make(map[string]bool)
	for _, work := range works {
		if err := context.Cause(ctx); err != nil {
			return summary, err
		}
		if work.CoverVersion <= 0 {
			continue
		}
		coverRel := covers.OriginalPath(work.ID)
		coverAbs, err := coverRoot.Resolve(coverRel)
		if err != nil {
			fmt.Printf("Invalid cover path for %s (%s): %v\n", work.ID, coverRel, err)
			continue
		}
		expectedOriginals[coverAbs] = true
		if info, err := os.Stat(coverAbs); err == nil && !info.IsDir() {
			continue
		} else if err == nil && info.IsDir() {
			fmt.Printf("Cover original is a directory: %s (%s)\n", work.ID, coverRel)
			continue
		} else if err != nil && !os.IsNotExist(err) {
			fmt.Printf("Failed to stat cover for %s: %v\n", work.ID, err)
			continue
		}

		foundAbs := recoverable.find(work.ID)
		if err := context.Cause(ctx); err != nil {
			return summary, err
		}
		if foundAbs != "" {
			foundRel, err := filepath.Rel(coverRoot.Path, foundAbs)
			if err != nil {
				fmt.Printf("Failed to resolve staged cover for %s: %v\n", work.ID, err)
				continue
			}
			if err := storage.Move(coverRoot, filepath.ToSlash(foundRel), coverRel); err != nil {
				fmt.Printf("Failed to restore cover for %s: %v\n", work.ID, err)
				continue
			}
			covers.RemoveDerived(coverRoot, work.ID)
			summary.Restored++
			continue
		}

		extracted, fallback, err := restoreCoverFromPrimaryAsset(ctx, database, booksRoot, coverRoot, work, verifiedAssetHashes)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return summary, cause
			}
			fmt.Printf("Failed to extract cover for %s: %v\n", work.ID, err)
			continue
		}
		if extracted {
			if fallback {
				summary.Fallback++
			} else {
				summary.Extracted++
			}
			continue
		}

		if _, err := database.Exec("UPDATE works SET cover_version = 0, updated_at = unixepoch() WHERE id = ?", work.ID); err != nil {
			fmt.Printf("Failed to clear missing cover for %s: %v\n", work.ID, err)
			continue
		}
		delete(expectedOriginals, coverAbs)
		covers.RemoveDerived(coverRoot, work.ID)
		summary.VersionCleared++
	}

	if err := context.Cause(ctx); err != nil {
		return summary, err
	}
	var err error
	summary.OrphanOriginalsRemoved, err = removeOrphanCoverOriginals(ctx, coverRoot, expectedOriginals)
	if err != nil {
		return summary, err
	}
	summary.StagedRemoved, err = removeStaleStagedCovers(ctx, coverRoot)
	if err != nil {
		return summary, err
	}
	return summary, nil
}

func pruneEmptyBookDirs(ctx context.Context, root storage.Root) error {
	booksDir := root.BooksDir()
	// Walk is parent-first. Remember every managed directory and let storage
	// attempt them child-first, including parents that only become empty after a
	// child is removed.
	var bookDirs []string
	if err := storage.WalkBooks(root, func(path string, info os.FileInfo, err error) error {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() || path == booksDir {
			return nil
		}
		rel, err := filepath.Rel(booksDir, path)
		if err != nil {
			return err
		}
		bookDirs = append(bookDirs, rel)
		return nil
	}); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("walk books for empty dirs: %w", err)
	}

	for _, bookDir := range slices.Backward(bookDirs) {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		storage.PruneEmptyParents(root, bookDir)
	}
	return nil
}

type repairSummaryItem struct {
	Label string
	Count int
}

func printRepairSummary(items []repairSummaryItem) {
	for _, item := range items {
		fmt.Printf("  %-32s %d\n", item.Label, item.Count)
	}
}

func restoreCoverFromPrimaryAsset(ctx context.Context, database *db.DB, booksRoot, coverRoot storage.Root, w db.WorkCoverRow, verifiedAssetHashes map[string]struct{}) (extracted bool, fallback bool, err error) {
	asset, ok, err := primaryAssetForCoverRecovery(database, w.ID)
	if err != nil {
		return false, false, err
	}
	if !ok {
		return false, false, nil
	}

	assetAbs, err := booksRoot.Resolve(asset.StoragePath)
	if err != nil {
		return false, false, err
	}
	f, err := os.Open(assetAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, nil
		}
		return false, false, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return false, false, err
	}
	_, hashAlreadyVerified := verifiedAssetHashes[asset.ID]
	if err := validateCoverRecoveryAssetBytes(ctx, asset, assetAbs, stat.Size(), hashAlreadyVerified); err != nil {
		return false, false, err
	}
	coverBytes, _, err := format.ExtractCover(f, stat.Size(), asset.Format)
	if err != nil {
		return false, false, fmt.Errorf("%s (%s): %w", asset.ID, asset.StoragePath, err)
	}
	if len(coverBytes) == 0 {
		return false, false, nil
	}
	if err := context.Cause(ctx); err != nil {
		return false, false, err
	}

	fallback = bookmeta.ParseOverrides(w.ManualOverrides)["cover"]
	coverRel := covers.OriginalPath(w.ID)
	err = storage.Place(coverRoot, coverRel, bytes.NewReader(coverBytes), func() error {
		overrides := bookmeta.ParseOverrides(w.ManualOverrides)
		if fallback {
			delete(overrides, "cover")
		}
		_, err := database.Exec(`
			UPDATE works
			SET cover_version = cover_version + 1,
			    metadata_rev = metadata_rev + 1,
			    manual_overrides = ?,
			    updated_at = unixepoch()
			WHERE id = ?
		`, bookmeta.MarshalOverrides(overrides), w.ID)
		return err
	})
	if err != nil {
		return false, fallback, err
	}
	covers.RemoveDerived(coverRoot, w.ID)
	return true, fallback, nil
}

func validateCoverRecoveryAssetBytes(ctx context.Context, asset coverRecoveryAsset, absPath string, size int64, hashAlreadyVerified bool) error {
	if asset.CurrentSize.Valid && asset.CurrentSize.Int64 != size {
		return fmt.Errorf("%s (%s): primary asset size mismatch, db %d, disk %d", asset.ID, asset.StoragePath, asset.CurrentSize.Int64, size)
	}
	if asset.CurrentSHA256 == "" || hashAlreadyVerified {
		return nil
	}
	got, err := fileSHA256Context(ctx, absPath)
	if err != nil {
		return err
	}
	if got != asset.CurrentSHA256 {
		return fmt.Errorf("%s (%s): primary asset hash mismatch, db %s, disk %s", asset.ID, asset.StoragePath, asset.CurrentSHA256, got)
	}
	return nil
}

func primaryAssetForCoverRecovery(queryer db.Queryer, workID string) (coverRecoveryAsset, bool, error) {
	var asset coverRecoveryAsset
	var formatKey string
	err := queryer.QueryRow(`
		SELECT id, storage_path, COALESCE(format, ''), COALESCE(current_sha256, ''), current_size
		FROM assets
		WHERE work_id = ? AND is_primary = 1
		LIMIT 1
	`, workID).Scan(&asset.ID, &asset.StoragePath, &formatKey, &asset.CurrentSHA256, &asset.CurrentSize)
	if errors.Is(err, sql.ErrNoRows) {
		return coverRecoveryAsset{}, false, nil
	}
	if err != nil {
		return coverRecoveryAsset{}, false, err
	}
	asset.Format = format.FormatFromKey(formatKey)
	return asset, true, nil
}

// recoverableAssetTagIndex maps each asset-id tag (`[asset_id]`, embedded in a
// managed filename) to the on-disk path carrying it. It is built once by a
// single walk of the books tree and staging on first lookup, so recovering N
// missing assets costs one tree walk instead of N (each old
// findRecoverableAsset call walked the whole books + staging tree). Managed
// content is walked first with first-wins, so it stays preferred over a staged
// temp carrying the same tag.
type recoverableAssetTagIndex struct {
	ctx   context.Context
	root  storage.Root
	byTag map[string]string
	built bool
}

func newRecoverableAssetTagIndex(ctx context.Context, root storage.Root) *recoverableAssetTagIndex {
	return &recoverableAssetTagIndex{ctx: ctx, root: root, byTag: make(map[string]string)}
}

func (idx *recoverableAssetTagIndex) find(assetID string) string {
	if !idx.built {
		idx.build()
		idx.built = true
	}
	return idx.byTag["["+assetID+"]"]
}

func (idx *recoverableAssetTagIndex) build() {
	visit := func(path string, info os.FileInfo, err error) error {
		if cause := context.Cause(idx.ctx); cause != nil {
			return cause
		}
		if err != nil || info.IsDir() {
			return nil
		}
		for _, tag := range bracketTags(info.Name()) {
			if _, seen := idx.byTag[tag]; !seen {
				idx.byTag[tag] = path
			}
		}
		return nil
	}
	_ = storage.WalkBooks(idx.root, visit)
	_ = filepath.Walk(idx.root.StagingDir(), visit)
}

// bracketTags returns the `[...]` segments (brackets included) in a filename,
// e.g. "Title [as_1].epub" -> ["[as_1]"]. Managed filenames carry the asset id
// this way, so it is the index key.
func bracketTags(name string) []string {
	var tags []string
	for {
		open := strings.IndexByte(name, '[')
		if open < 0 {
			break
		}
		closeRel := strings.IndexByte(name[open:], ']')
		if closeRel < 0 {
			break
		}
		end := open + closeRel
		tags = append(tags, name[open:end+1])
		name = name[end+1:]
	}
	return tags
}

type recoverableAssetHashIndex struct {
	ctx        context.Context
	root       storage.Root
	referenced map[string]bool
	bySHA256   map[string][]string
	built      bool
	buildErr   error
}

func newRecoverableAssetHashIndex(ctx context.Context, root storage.Root, referenced map[string]bool) *recoverableAssetHashIndex {
	return &recoverableAssetHashIndex{
		ctx:        ctx,
		root:       root,
		referenced: referenced,
		bySHA256:   make(map[string][]string),
	}
}

func (idx *recoverableAssetHashIndex) find(wantSHA256 string) (string, error) {
	if wantSHA256 == "" {
		return "", nil
	}
	if !idx.built {
		idx.buildErr = idx.build()
		idx.built = true
	}
	if idx.buildErr != nil {
		return "", idx.buildErr
	}

	candidates := idx.bySHA256[wantSHA256]
	for len(candidates) > 0 {
		if err := context.Cause(idx.ctx); err != nil {
			return "", err
		}
		path := candidates[0]
		candidates = candidates[1:]
		idx.bySHA256[wantSHA256] = candidates
		if idx.referenced[path] {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		if info.IsDir() {
			continue
		}
		got, err := fileSHA256Context(idx.ctx, path)
		if err != nil {
			continue
		}
		if got == wantSHA256 {
			return path, nil
		}
	}
	return "", nil
}

func (idx *recoverableAssetHashIndex) build() error {
	return storage.WalkBooks(idx.root, func(path string, info os.FileInfo, err error) error {
		if cause := context.Cause(idx.ctx); cause != nil {
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
		if idx.referenced[path] {
			return nil
		}
		got, err := fileSHA256Context(idx.ctx, path)
		if err != nil {
			return nil
		}
		idx.bySHA256[got] = append(idx.bySHA256[got], path)
		return nil
	})
}

// recoverableCoverIndex maps crash-left cover temp names to the work whose
// cover they contain: importer Stage files in .staging, and Place/adjacent
// replacement files in covers/. Staging is indexed before covers/ so an
// explicitly staged importer file wins. Within a directory filepath.Walk's
// lexical order and first-wins map insertion make selection deterministic.
type recoverableCoverIndex struct {
	ctx    context.Context
	root   storage.Root
	byWork map[string]string
	built  bool
}

func newRecoverableCoverIndex(ctx context.Context, root storage.Root) *recoverableCoverIndex {
	return &recoverableCoverIndex{ctx: ctx, root: root, byWork: make(map[string]string)}
}

func (idx *recoverableCoverIndex) find(workID string) string {
	if !idx.built {
		idx.indexDir(idx.root.StagingDir(), stagedCoverWorkID)
		idx.indexDir(idx.root.Abs("covers"), coverDirTempWorkID)
		idx.built = true
	}
	return idx.byWork[workID]
}

func (idx *recoverableCoverIndex) indexDir(dir string, parseWorkID func(string) (string, bool)) {
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if cause := context.Cause(idx.ctx); cause != nil {
			return cause
		}
		if err != nil || info.IsDir() {
			return nil
		}
		id, ok := parseWorkID(info.Name())
		if !ok {
			return nil
		}
		if _, exists := idx.byWork[id]; !exists {
			idx.byWork[id] = path
		}
		return nil
	})
}

func stagedCoverWorkID(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, ".tmp-")
	if !ok {
		return "", false
	}
	_, label, ok := strings.Cut(rest, "-")
	if !ok {
		return "", false
	}
	if !strings.HasPrefix(label, "w_") || !strings.HasSuffix(label, "-cover") {
		return "", false
	}
	return strings.TrimSuffix(label, "-cover"), true
}

func coverDirTempWorkID(name string) (string, bool) {
	if rest, ok := strings.CutPrefix(name, ".tmp-"); ok {
		if _, workID, ok := strings.Cut(rest, "-"); ok {
			if strings.HasPrefix(workID, "w_") {
				return workID, true
			}
		}
		return "", false
	}

	if !strings.HasPrefix(name, ".writeback-") || !strings.HasSuffix(name, ".tmp") {
		return "", false
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(name, ".writeback-"), ".tmp")
	label, _, ok := strings.CutLast(rest, "-")
	if !ok {
		return "", false
	}
	if !strings.HasPrefix(label, "w_") || !strings.HasSuffix(label, "-cover") {
		return "", false
	}
	return strings.TrimSuffix(label, "-cover"), true
}

func removeStaleStagedCovers(ctx context.Context, root storage.Root) (int, error) {
	removed := 0
	err := filepath.Walk(root.StagingDir(), func(path string, info os.FileInfo, err error) error {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		if err != nil || info.IsDir() {
			return nil
		}
		if isStagedCoverFileName(info.Name()) {
			if err := os.Remove(path); err == nil {
				removed++
			}
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return removed, err
	}
	return removed, nil
}

func isStagedCoverFileName(name string) bool {
	_, ok := stagedCoverWorkID(name)
	return ok
}

func removeOrphanCoverOriginals(ctx context.Context, root storage.Root, expected map[string]bool) (int, error) {
	removed := 0
	err := filepath.Walk(root.Abs("covers"), func(path string, info os.FileInfo, err error) error {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		if err != nil || info.IsDir() {
			return nil
		}
		if expected[path] {
			return nil
		}
		if err := os.Remove(path); err == nil {
			removed++
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return removed, err
	}
	return removed, nil
}
