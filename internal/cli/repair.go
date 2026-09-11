package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	ID            int64
	StoragePath   string
	Format        format.Format
	CurrentSHA256 []byte
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
	root, err := storage.OpenRoot(database.Read(parent), dataDir)
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
	template, err := storage.OpenBookPathTemplate(database.Read(ctx))
	if err != nil {
		return err
	}

	writebackRepair, err := repairMetadataWritebackAttempts(ctx, database, root)
	if err != nil {
		return err
	}

	assets, err := db.AllAssetsWithPrimaryAuthor(database.Read(ctx))
	if err != nil {
		return err
	}
	bookCovers, err := db.AllBookCovers(database.Read(ctx))
	if err != nil {
		return err
	}

	assetRepair, err := repairAssets(ctx, database, root, template, assets)
	if err != nil {
		return err
	}
	coverRepair, err := repairCovers(ctx, database, root, dataRoot, bookCovers, assetRepair.VerifiedHashes)
	if err != nil {
		return err
	}

	// Delete authors no longer referenced by any book.
	if err := context.Cause(ctx); err != nil {
		return err
	}
	orphanAuthors, err := db.DeleteOrphanAuthors(database.Write(ctx))
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
	HashMismatches         int
	Formats                int
	ReaderCapabilities     int
	VerifiedHashes         map[int64]struct{}
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
	recoverableByName := newRecoverableAssetNameIndex(ctx, root, referencedPaths)
	summary := assetRepairResult{
		VerifiedHashes: make(map[int64]struct{}, len(assets)),
	}

	writer := database.Write(ctx)
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
			fmt.Printf("Invalid database path: %d (%s): %v\n", asset.ID, asset.StoragePath, pathErr)
			summary.InvalidPaths++
		} else if _, err := os.Stat(actualFileAbsPath); err == nil {
			foundAbsPath = actualFileAbsPath
		}
		if foundAbsPath == "" {
			foundAbsPath, err = recoverableByName.find(asset.ID, asset.CurrentSHA256)
			if err != nil {
				return summary, err
			}
		}
		if err := context.Cause(ctx); err != nil {
			return summary, err
		}
		recoveredByHash := false
		if foundAbsPath == "" {
			if match, err := recoverableByHash.find(asset.CurrentSHA256); err != nil {
				fmt.Printf("Failed to scan orphans for %d: %v\n", asset.ID, err)
			} else if match != "" {
				foundAbsPath = match
				recoveredByHash = true
			}
		}

		if foundAbsPath == "" {
			fmt.Printf("Missing/unrecoverable: %d\n", asset.ID)
			summary.Missing++
			continue
		}
		if err := context.Cause(ctx); err != nil {
			return summary, err
		}

		foundRelPath, err := filepath.Rel(root.Path, foundAbsPath)
		if err != nil {
			fmt.Printf("Failed to resolve found path for %d: %v\n", asset.ID, err)
			continue
		}
		// Compare and Move both want slash-separated relative paths (canonical
		// paths are slash-based); without ToSlash a Windows Rel always
		// mis-compares and Move fails Root.Resolve's backslash validation.
		foundRelPath = filepath.ToSlash(foundRelPath)

		currentHash, currentSize, err := fileSHA256AndSizeContext(ctx, foundAbsPath)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return summary, cause
			}
			fmt.Printf("Failed to hash %d: %v\n", asset.ID, err)
			continue
		}
		if !bytes.Equal(asset.CurrentSHA256, currentHash) {
			fmt.Printf("Hash mismatch: %d (%s)\n", asset.ID, foundRelPath)
			summary.HashMismatches++
			if asset.CurrentSize.Valid && asset.CurrentSize.Int64 != currentSize {
				summary.SizeMismatches++
			}
			continue
		}

		if foundRelPath != canonicalPath {
			if err := storage.Move(root, foundRelPath, canonicalPath); err != nil {
				fmt.Printf("Failed to move %d: %v\n", asset.ID, err)
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
			err = database.Transact(ctx, func(tx *db.Tx) error {
				if _, err := tx.Exec("UPDATE assets SET storage_path = ?, filename = ? WHERE id = ?", canonicalPath, filepath.Base(canonicalPath), asset.ID); err != nil {
					return err
				}
				return db.UpdateSearchIndex(tx, asset.BookID)
			})
			if err != nil {
				fmt.Printf("Failed to update database for %d: %v\n", asset.ID, err)
				continue
			}
			if foundRelPath == canonicalPath {
				summary.FixedPaths++
			}
		}

		finalAbsPath, err := root.Resolve(canonicalPath)
		if err != nil {
			fmt.Printf("Invalid canonical path for %d (%s): %v\n", asset.ID, canonicalPath, err)
			continue
		}
		if !asset.CurrentSize.Valid || asset.CurrentSize.Int64 != currentSize {
			if _, err := writer.Exec("UPDATE assets SET current_size = ?, updated_at = unixepoch() WHERE id = ?", currentSize, asset.ID); err != nil {
				fmt.Printf("Failed to repair current size for %d: %v\n", asset.ID, err)
				continue
			}
			summary.SizeBackfilled++
		}
		summary.VerifiedHashes[asset.ID] = struct{}{}
		capability, err := detectAssetReaderCapability(canonicalPath, finalAbsPath)
		if err != nil {
			fmt.Printf("Failed to recompute reader capability for %d: %v\n", asset.ID, err)
			continue
		}
		if capability.Format != asset.Format || capability.CanRead != asset.CanRead {
			if _, err := writer.Exec("UPDATE assets SET format = ?, can_read = ?, updated_at = unixepoch() WHERE id = ?", format.FormatKey(capability.Format), capability.CanRead, asset.ID); err != nil {
				fmt.Printf("Failed to repair format/capability for %d: %v\n", asset.ID, err)
				continue
			}
			if capability.Format != asset.Format {
				summary.Formats++
			}
			if capability.CanRead != asset.CanRead {
				summary.ReaderCapabilities++
			}
		}
		if !asset.OriginalSize.Valid && bytes.Equal(asset.OriginalSHA256, currentHash) {
			if _, err := writer.Exec("UPDATE assets SET original_size = ?, updated_at = unixepoch() WHERE id = ?", currentSize, asset.ID); err != nil {
				fmt.Printf("Failed to backfill original size for %d: %v\n", asset.ID, err)
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

func repairCovers(ctx context.Context, database *db.DB, booksRoot, coverRoot storage.Root, books []db.BookCoverRow, verifiedAssetHashes map[int64]struct{}) (coverRepairResult, error) {
	recoverable := newRecoverableCoverIndex(ctx, database.Read(ctx), coverRoot)
	summary := coverRepairResult{}

	// Covers are derived presentation data keyed by SQLite state: unlike book
	// assets, stale originals and impossible cover_version flags are safe to
	// clear so check converges back to the DB truth.
	expectedOriginals := make(map[string]bool)
	for _, book := range books {
		if err := context.Cause(ctx); err != nil {
			return summary, err
		}
		if book.CoverVersion <= 0 {
			continue
		}
		coverRel := covers.OriginalPath(book.ID)
		coverAbs, err := coverRoot.Resolve(coverRel)
		if err != nil {
			fmt.Printf("Invalid cover path for %d (%s): %v\n", book.ID, coverRel, err)
			continue
		}
		expectedOriginals[coverAbs] = true
		if info, err := os.Stat(coverAbs); err == nil && !info.IsDir() {
			continue
		} else if err == nil && info.IsDir() {
			fmt.Printf("Cover original is a directory: %d (%s)\n", book.ID, coverRel)
			continue
		} else if err != nil && !os.IsNotExist(err) {
			fmt.Printf("Failed to stat cover for %d: %v\n", book.ID, err)
			continue
		}

		foundAbs, err := recoverable.find(book.ID)
		if err != nil {
			return summary, err
		}
		if err := context.Cause(ctx); err != nil {
			return summary, err
		}
		if foundAbs != "" {
			foundRel, err := filepath.Rel(coverRoot.Path, foundAbs)
			if err != nil {
				fmt.Printf("Failed to resolve staged cover for %d: %v\n", book.ID, err)
				continue
			}
			if err := storage.Move(coverRoot, filepath.ToSlash(foundRel), coverRel); err != nil {
				fmt.Printf("Failed to restore cover for %d: %v\n", book.ID, err)
				continue
			}
			covers.RemoveDerived(coverRoot, book.ID)
			summary.Restored++
			continue
		}

		extracted, fallback, err := restoreCoverFromPrimaryAsset(ctx, database, booksRoot, coverRoot, book, verifiedAssetHashes)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return summary, cause
			}
			fmt.Printf("Failed to extract cover for %d: %v\n", book.ID, err)
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

		if _, err := database.Write(ctx).Exec("UPDATE books SET cover_version = 0, updated_at = unixepoch() WHERE id = ?", book.ID); err != nil {
			fmt.Printf("Failed to clear missing cover for %d: %v\n", book.ID, err)
			continue
		}
		delete(expectedOriginals, coverAbs)
		covers.RemoveDerived(coverRoot, book.ID)
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

func restoreCoverFromPrimaryAsset(ctx context.Context, database *db.DB, booksRoot, coverRoot storage.Root, w db.BookCoverRow, verifiedAssetHashes map[int64]struct{}) (extracted bool, fallback bool, err error) {
	asset, ok, err := primaryAssetForCoverRecovery(database.Read(ctx), w.ID)
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
		return false, false, fmt.Errorf("%d (%s): %w", asset.ID, asset.StoragePath, err)
	}
	if len(coverBytes) == 0 {
		return false, false, nil
	}
	if err := context.Cause(ctx); err != nil {
		return false, false, err
	}

	fallback = bookmeta.ParseOverrides(w.ManualOverrides)["cover"]
	coverRel := covers.OriginalPath(w.ID)
	err = storage.Place(coverRoot, coverRel, covers.TempLabel(w.ID), bytes.NewReader(coverBytes), func() error {
		overrides := bookmeta.ParseOverrides(w.ManualOverrides)
		if fallback {
			delete(overrides, "cover")
		}
		_, err := database.Write(ctx).Exec(`
			UPDATE books
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
		return fmt.Errorf("%d (%s): primary asset size mismatch, db %d, disk %d", asset.ID, asset.StoragePath, asset.CurrentSize.Int64, size)
	}
	if hashAlreadyVerified {
		return nil
	}
	got, err := fileSHA256Context(ctx, absPath)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, asset.CurrentSHA256) {
		return fmt.Errorf("%d (%s): primary asset hash mismatch, db %x, disk %x", asset.ID, asset.StoragePath, asset.CurrentSHA256, got)
	}
	return nil
}

func primaryAssetForCoverRecovery(queryer db.Queryer, bookID int64) (coverRecoveryAsset, bool, error) {
	var asset coverRecoveryAsset
	var formatKey string
	err := queryer.QueryRow(`
		SELECT id, storage_path, COALESCE(format, ''), current_sha256, current_size
		FROM assets
		WHERE book_id = ? AND is_primary = 1
		LIMIT 1
	`, bookID).Scan(&asset.ID, &asset.StoragePath, &formatKey, &asset.CurrentSHA256, &asset.CurrentSize)
	if errors.Is(err, sql.ErrNoRows) {
		return coverRecoveryAsset{}, false, nil
	}
	if err != nil {
		return coverRecoveryAsset{}, false, err
	}
	asset.Format = format.FormatFromKey(formatKey)
	return asset, true, nil
}

// recoverableAssetNameIndex indexes managed [a<ID>] tags and import hashes.
// Names select candidates; only matching bytes may be moved into a missing path.
type recoverableAssetNameIndex struct {
	ctx          context.Context
	root         storage.Root
	referenced   map[string]bool
	byTag        map[string][]string
	bySourceHash map[[sha256.Size]byte][]string
	built        bool
	buildErr     error
}

func newRecoverableAssetNameIndex(ctx context.Context, root storage.Root, referenced map[string]bool) *recoverableAssetNameIndex {
	return &recoverableAssetNameIndex{
		ctx: ctx, root: root, referenced: referenced,
		byTag:        make(map[string][]string),
		bySourceHash: make(map[[sha256.Size]byte][]string),
	}
}

func (idx *recoverableAssetNameIndex) find(assetID int64, wantSHA256 []byte) (string, error) {
	if !idx.built {
		idx.buildErr = idx.build()
		idx.built = true
	}
	if idx.buildErr != nil {
		return "", idx.buildErr
	}
	candidates := idx.byTag[storage.AssetTag(assetID)]
	if path, err := matchingAssetFile(idx.ctx, candidates, wantSHA256, idx.referenced); path != "" || err != nil {
		return path, err
	}
	return matchingAssetFile(idx.ctx, idx.bySourceHash[[sha256.Size]byte(wantSHA256)], wantSHA256, idx.referenced)
}

func (idx *recoverableAssetNameIndex) build() error {
	visit := func(path string, info os.FileInfo, err error) error {
		if cause := context.Cause(idx.ctx); cause != nil {
			return cause
		}
		if err != nil {
			return err
		}
		if info.IsDir() || idx.referenced[path] {
			return nil
		}
		for _, tag := range bracketTags(info.Name()) {
			idx.byTag[tag] = append(idx.byTag[tag], path)
		}
		return nil
	}
	if err := storage.WalkBooks(idx.root, visit); err != nil && !os.IsNotExist(err) {
		return err
	}
	err := filepath.Walk(idx.root.StagingDir(), func(path string, info os.FileInfo, err error) error {
		if err := visit(path, info, err); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		label, ok := storage.ParseStagedTempName(info.Name())
		if !ok {
			return nil
		}
		encoded, _, _ := strings.Cut(label, ".")
		sum, err := hex.DecodeString(encoded)
		if err == nil && len(sum) == sha256.Size {
			key := [sha256.Size]byte(sum)
			idx.bySourceHash[key] = append(idx.bySourceHash[key], path)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func matchingAssetFile(ctx context.Context, candidates []string, wantSHA256 []byte, referenced map[string]bool) (string, error) {
	for _, path := range candidates {
		if err := context.Cause(ctx); err != nil {
			return "", err
		}
		if referenced[path] {
			continue
		}
		got, err := fileSHA256Context(ctx, path)
		if cause := context.Cause(ctx); cause != nil {
			return "", cause
		}
		if err == nil && bytes.Equal(got, wantSHA256) {
			return path, nil
		}
	}
	return "", nil
}

// bracketTags returns the `[...]` segments (brackets included) in a filename,
// e.g. "Title [a12].epub" -> ["[a12]"]. Managed filenames carry the asset id
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
	bySHA256   map[[sha256.Size]byte][]string
	built      bool
	buildErr   error
}

func newRecoverableAssetHashIndex(ctx context.Context, root storage.Root, referenced map[string]bool) *recoverableAssetHashIndex {
	return &recoverableAssetHashIndex{
		ctx:        ctx,
		root:       root,
		referenced: referenced,
		bySHA256:   make(map[[sha256.Size]byte][]string),
	}
}

func (idx *recoverableAssetHashIndex) find(wantSHA256 []byte) (string, error) {
	if !idx.built {
		idx.buildErr = idx.build()
		idx.built = true
	}
	if idx.buildErr != nil {
		return "", idx.buildErr
	}

	return matchingAssetFile(idx.ctx, idx.bySHA256[[sha256.Size]byte(wantSHA256)], wantSHA256, idx.referenced)
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
		key := [sha256.Size]byte(got)
		idx.bySHA256[key] = append(idx.bySHA256[key], path)
		return nil
	})
}

// recoverableCoverIndex maps crash-left cover temp names to the book whose
// cover they contain: importer Stage files in .staging, and Place/adjacent
// replacement files in covers/. Staging is indexed before covers/ so an
// explicitly staged importer file wins. Within a directory filepath.Walk's
// lexical order and first-wins map insertion make selection deterministic.
type recoverableCoverIndex struct {
	ctx    context.Context
	db     db.Queryer
	root   storage.Root
	byBook map[int64]string
	built  bool
}

func newRecoverableCoverIndex(ctx context.Context, database db.Queryer, root storage.Root) *recoverableCoverIndex {
	return &recoverableCoverIndex{ctx: ctx, db: database, root: root, byBook: make(map[int64]string)}
}

func (idx *recoverableCoverIndex) find(bookID int64) (string, error) {
	if !idx.built {
		if err := idx.indexDir(idx.root.StagingDir(), idx.stagedBookID); err != nil {
			return "", err
		}
		if err := idx.indexDir(idx.root.Abs("covers"), func(name string) (int64, error) {
			id, _ := coverDirTempBookID(name)
			return id, nil
		}); err != nil {
			return "", err
		}
		idx.built = true
	}
	return idx.byBook[bookID], nil
}

func (idx *recoverableCoverIndex) stagedBookID(name string) (int64, error) {
	sourceHash, ok := stagedCoverSourceHash(name)
	if !ok {
		return 0, nil
	}
	var bookID int64
	err := idx.db.QueryRow("SELECT book_id FROM assets WHERE original_sha256 = ?", sourceHash).Scan(&bookID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("resolve staged cover source %x: %w", sourceHash, err)
	}
	return bookID, nil
}

func (idx *recoverableCoverIndex) indexDir(dir string, resolveBookID func(string) (int64, error)) error {
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if cause := context.Cause(idx.ctx); cause != nil {
			return cause
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		id, err := resolveBookID(info.Name())
		if err != nil {
			return err
		}
		if id == 0 {
			return nil
		}
		if _, exists := idx.byBook[id]; !exists {
			idx.byBook[id] = path
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func stagedCoverSourceHash(name string) ([]byte, bool) {
	label, ok := storage.ParseStagedTempName(name)
	if !ok {
		return nil, false
	}
	return covers.ParseImportTempLabel(label)
}

func coverDirTempBookID(name string) (int64, bool) {
	label, ok := storage.ParseWritebackTempName(name)
	if !ok {
		return 0, false
	}
	return covers.ParseTempLabel(label)
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
	_, ok := stagedCoverSourceHash(name)
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
