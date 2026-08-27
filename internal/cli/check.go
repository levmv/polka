package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/storage"
)

var ErrIssuesFound = errors.New("issues found")

func runCheck(dataDir string, args []string) error {
	fs := commandFlagSet("check", "polka check [--deep]")
	deep := fs.Bool("deep", false, "verify hashes and reader capabilities by reading asset contents")
	if help, err := parseCommandFlags(fs, args); help || err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return reportedErrorf("usage: polka check [--deep]")
	}

	database, err := openDatabaseReadOnly(dataDir)
	if err != nil {
		return err
	}
	defer database.Close()
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		return err
	}
	// Covers live in the app data dir, separate from the books root.
	dataRoot := storage.NewRoot(dataDir)
	if err := storage.RequireLayout(root); errors.Is(err, storage.ErrLayoutMissing) {
		fmt.Printf("Storage unavailable:\n  - %v\n\n", err)
		return ErrIssuesFound
	} else if err != nil {
		return err
	}
	template, err := storage.OpenBookPathTemplate(database.DB)
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

	var missingFiles []string
	var invalidStoragePaths []string
	var staleLayouts []string
	var ioErrors []string
	var missingCurrentSizes []string
	var sizeMismatches []string
	var missingCurrentHashes []string
	var hashMismatches []string
	var formatMismatches []string
	var readerCapabilityMismatches []string
	var missingCoverOriginals []string
	var orphanCoverOriginals []string
	var pendingWritebackAttempts []string
	var orphanWritebackTemps []string

	referencedPaths := make(map[string]bool)
	referencedCoverPaths := make(map[string]bool)

	for _, a := range assets {
		absPath, pathErr := root.Resolve(a.StoragePath)
		if pathErr != nil {
			invalidStoragePaths = append(invalidStoragePaths, fmt.Sprintf("%s (%s): %v", a.ID, a.StoragePath, pathErr))
		} else {
			referencedPaths[absPath] = true

			info, err := os.Stat(absPath)
			if os.IsNotExist(err) {
				missingFiles = append(missingFiles, fmt.Sprintf("%s (%s)", a.ID, a.StoragePath))
			} else if err != nil {
				ioErrors = append(ioErrors, fmt.Sprintf("stat %s: %v", a.StoragePath, err))
			} else {
				sizeMatches := true
				if !a.CurrentSize.Valid {
					sizeMatches = false
					missingCurrentSizes = append(missingCurrentSizes, fmt.Sprintf("%s (%s)", a.ID, a.StoragePath))
				} else if a.CurrentSize.Int64 != info.Size() {
					sizeMatches = false
					sizeMismatches = append(sizeMismatches, fmt.Sprintf("%s (%s): db %d, disk %d", a.ID, a.StoragePath, a.CurrentSize.Int64, info.Size()))
				}
				if a.CurrentSHA256 == "" {
					missingCurrentHashes = append(missingCurrentHashes, fmt.Sprintf("%s (%s)", a.ID, a.StoragePath))
				} else if *deep && sizeMatches {
					gotHash, err := fileSHA256(absPath)
					if err != nil {
						ioErrors = append(ioErrors, fmt.Sprintf("hash %s: %v", a.StoragePath, err))
						continue
					}
					if gotHash != a.CurrentSHA256 {
						hashMismatches = append(hashMismatches, fmt.Sprintf("%s (%s): db %s, disk %s", a.ID, a.StoragePath, a.CurrentSHA256, gotHash))
					}
				}
				if *deep {
					capability, err := detectAssetReaderCapability(a.StoragePath, absPath)
					if err != nil {
						ioErrors = append(ioErrors, fmt.Sprintf("detect reader capability %s: %v", a.StoragePath, err))
						continue
					}
					if capability.Format != a.Format {
						formatMismatches = append(formatMismatches, fmt.Sprintf("%s (%s): db %s, detected %s", a.ID, a.StoragePath, format.FormatLabel(a.Format), format.FormatLabel(capability.Format)))
					}
					if capability.CanRead != a.CanRead {
						readerCapabilityMismatches = append(readerCapabilityMismatches, fmt.Sprintf("%s (%s): db %t, detected %t (%s)", a.ID, a.StoragePath, a.CanRead, capability.CanRead, format.FormatLabel(capability.Format)))
					}
				}
			}
		}

		cPath, err := storage.BookPath(template, assetBookPathData(a))
		if err != nil {
			return err
		}
		if a.StoragePath != cPath {
			staleLayouts = append(staleLayouts, fmt.Sprintf("%s: %s -> %s", a.ID, a.StoragePath, cPath))
		}
	}

	for _, w := range workCovers {
		rel := covers.OriginalPath(w.ID)
		absPath, err := dataRoot.Resolve(rel)
		if err != nil {
			invalidStoragePaths = append(invalidStoragePaths, fmt.Sprintf("cover %s (%s): %v", w.ID, rel, err))
			continue
		}
		if w.CoverVersion > 0 {
			referencedCoverPaths[absPath] = true
			info, err := os.Stat(absPath)
			if os.IsNotExist(err) {
				missingCoverOriginals = append(missingCoverOriginals, fmt.Sprintf("%s (%s)", w.ID, rel))
			} else if err != nil {
				ioErrors = append(ioErrors, fmt.Sprintf("stat cover %s: %v", rel, err))
			} else if info.IsDir() {
				ioErrors = append(ioErrors, fmt.Sprintf("stat cover %s: is a directory", rel))
			}
		}
	}

	var orphanFiles []string
	var emptyDirs []string
	var stagedFiles []string
	writebackAttempts, err := db.ListMetadataWritebackAttempts(database)
	if err != nil {
		return err
	}
	pendingWritebackAttempts = writebackAttemptReports(writebackAttempts)
	pendingWritebackTemps := writebackTempPaths(root, writebackAttempts)

	booksDir := root.BooksDir()
	err = storage.WalkBooks(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			ioErrors = append(ioErrors, fmt.Sprintf("walk books %s: %v", relToRoot(root, path), err))
			return nil
		}
		if info.IsDir() {
			if path != booksDir {
				f, err := os.Open(path)
				if err == nil {
					_, err = f.Readdirnames(1)
					if err == io.EOF {
						emptyDirs = append(emptyDirs, relToRoot(root, path))
					}
					f.Close()
				} else {
					ioErrors = append(ioErrors, fmt.Sprintf("read dir %s: %v", relToRoot(root, path), err))
				}
			}
		} else {
			if storage.IsWritebackTempFileName(info.Name()) {
				if !pendingWritebackTemps[path] {
					orphanWritebackTemps = append(orphanWritebackTemps, relToRoot(root, path))
				}
				return nil
			}
			if !referencedPaths[path] {
				orphanFiles = append(orphanFiles, relToRoot(root, path))
			}
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		ioErrors = append(ioErrors, fmt.Sprintf("walk books: %v", err))
	}

	// Book assets stage under the books root; covers stage under the data root.
	// Walk both (skipping a duplicate when they coincide) so leftover staged
	// files from an interrupted import surface wherever they landed.
	walkedStaging := map[string]bool{}
	for _, sr := range []storage.Root{root, dataRoot} {
		stagingDir := sr.StagingDir()
		if walkedStaging[stagingDir] {
			continue
		}
		walkedStaging[stagingDir] = true
		stagingRoot := sr
		err = filepath.Walk(stagingDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				ioErrors = append(ioErrors, fmt.Sprintf("walk staging %s: %v", relToRoot(stagingRoot, path), err))
				return nil
			}
			if !info.IsDir() {
				stagedFiles = append(stagedFiles, relToRoot(stagingRoot, path))
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			ioErrors = append(ioErrors, fmt.Sprintf("walk staging: %v", err))
		}
	}

	coversOriginalsDir := dataRoot.Abs("covers")
	err = filepath.Walk(coversOriginalsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			ioErrors = append(ioErrors, fmt.Sprintf("walk cover originals %s: %v", relToRoot(dataRoot, path), err))
			return nil
		}
		if !info.IsDir() && !referencedCoverPaths[path] {
			orphanCoverOriginals = append(orphanCoverOriginals, relToRoot(dataRoot, path))
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		ioErrors = append(ioErrors, fmt.Sprintf("walk cover originals: %v", err))
	}

	hasErrors := false
	for _, section := range []checkReportSection{
		{"Invalid storage paths", invalidStoragePaths},
		{"Missing files", missingFiles},
		{"Missing cover originals", missingCoverOriginals},
		{"Stale layout / drift", staleLayouts},
		{"Missing current hashes", missingCurrentHashes},
		{"Missing current sizes", missingCurrentSizes},
		{"Current size mismatches", sizeMismatches},
		{"Current hash mismatches", hashMismatches},
		{"Format mismatches", formatMismatches},
		{"Reader capability mismatches", readerCapabilityMismatches},
		{"I/O errors", ioErrors},
		{"Orphan files", orphanFiles},
		{"Orphan cover originals", orphanCoverOriginals},
		{"Pending metadata write-back attempts", pendingWritebackAttempts},
		{"Orphan metadata write-back temps", orphanWritebackTemps},
		{"Staged files", stagedFiles},
		{"Empty directories", emptyDirs},
	} {
		if printCheckSection(section.Title, section.Items) {
			hasErrors = true
		}
	}

	if !hasErrors {
		fmt.Println("Check completed: no issues found.")
		return nil
	}

	return ErrIssuesFound
}

type checkReportSection struct {
	Title string
	Items []string
}

func printCheckSection(title string, items []string) bool {
	if len(items) == 0 {
		return false
	}
	fmt.Printf("%s (%d):\n", title, len(items))
	for _, msg := range items {
		fmt.Printf("  - %s\n", msg)
	}
	fmt.Println()
	return true
}

func relToRoot(root storage.Root, absPath string) string {
	rel, err := filepath.Rel(root.Path, absPath)
	if err != nil {
		return absPath
	}
	return rel
}
