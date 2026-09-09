package cli

import (
	"bytes"
	"context"
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

func runCheck(ctx context.Context, dataDir string, args []string) error {
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
	root, err := storage.OpenRoot(database.Read(ctx), dataDir)
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
	template, err := storage.OpenBookPathTemplate(database.Read(ctx))
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

	var report checkReport
	referencedPaths, err := report.checkAssets(ctx, root, template, assets, *deep)
	if err != nil {
		return err
	}
	referencedCoverPaths, err := report.checkCoverOriginals(ctx, dataRoot, bookCovers)
	if err != nil {
		return err
	}
	writebackAttempts, err := db.ListMetadataWritebackAttempts(database.Read(ctx))
	if err != nil {
		return err
	}
	report.pendingWritebackAttempts = writebackAttemptReports(writebackAttempts)
	if err := report.checkBookFiles(ctx, root, referencedPaths, writebackTempPaths(root, writebackAttempts)); err != nil {
		return err
	}
	if err := report.checkStagingFiles(ctx, root, dataRoot); err != nil {
		return err
	}
	if err := report.checkOrphanCoverOriginals(ctx, dataRoot, referencedCoverPaths); err != nil {
		return err
	}
	return report.print()
}

// Checks accumulate issues here and continue with the remaining files. They
// return an error only when the whole run must stop, such as on cancellation.
type checkReport struct {
	invalidStoragePaths        []string
	missingFiles               []string
	missingCoverOriginals      []string
	staleLayouts               []string
	pathCollisions             []string
	missingCurrentSizes        []string
	sizeMismatches             []string
	hashMismatches             []string
	formatMismatches           []string
	readerCapabilityMismatches []string
	ioErrors                   []string
	orphanFiles                []string
	orphanCoverOriginals       []string
	pendingWritebackAttempts   []string
	orphanWritebackTemps       []string
	stagedFiles                []string
	emptyDirs                  []string
}

func (report *checkReport) checkAssets(ctx context.Context, root storage.Root, template string, assets []db.AssetWithAuthorRow, deep bool) (map[string]bool, error) {
	referencedPaths := make(map[string]bool)
	canonicalPaths := make([]storage.BookPathCandidate, 0, len(assets))

	for _, a := range assets {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		absPath, pathErr := root.Resolve(a.StoragePath)
		if pathErr != nil {
			report.invalidStoragePaths = append(report.invalidStoragePaths, fmt.Sprintf("%d (%s): %v", a.ID, a.StoragePath, pathErr))
		} else {
			referencedPaths[absPath] = true

			info, err := os.Stat(absPath)
			if os.IsNotExist(err) {
				report.missingFiles = append(report.missingFiles, fmt.Sprintf("%d (%s)", a.ID, a.StoragePath))
			} else if err != nil {
				report.ioErrors = append(report.ioErrors, fmt.Sprintf("stat %s: %v", a.StoragePath, err))
			} else {
				sizeMatches := true
				if !a.CurrentSize.Valid {
					sizeMatches = false
					report.missingCurrentSizes = append(report.missingCurrentSizes, fmt.Sprintf("%d (%s)", a.ID, a.StoragePath))
				} else if a.CurrentSize.Int64 != info.Size() {
					sizeMatches = false
					report.sizeMismatches = append(report.sizeMismatches, fmt.Sprintf("%d (%s): db %d, disk %d", a.ID, a.StoragePath, a.CurrentSize.Int64, info.Size()))
				}
				if deep && sizeMatches {
					gotHash, err := fileSHA256Context(ctx, absPath)
					if err != nil {
						if cause := context.Cause(ctx); cause != nil {
							return nil, cause
						}
						report.ioErrors = append(report.ioErrors, fmt.Sprintf("hash %s: %v", a.StoragePath, err))
						continue
					}
					if !bytes.Equal(gotHash, a.CurrentSHA256) {
						report.hashMismatches = append(report.hashMismatches, fmt.Sprintf("%d (%s): db %x, disk %x", a.ID, a.StoragePath, a.CurrentSHA256, gotHash))
					}
				}
				if deep {
					capability, err := detectAssetReaderCapability(a.StoragePath, absPath)
					if err != nil {
						report.ioErrors = append(report.ioErrors, fmt.Sprintf("detect reader capability %s: %v", a.StoragePath, err))
						continue
					}
					if capability.Format != a.Format {
						report.formatMismatches = append(report.formatMismatches, fmt.Sprintf("%d (%s): db %s, detected %s", a.ID, a.StoragePath, format.FormatLabel(a.Format), format.FormatLabel(capability.Format)))
					}
					if capability.CanRead != a.CanRead {
						report.readerCapabilityMismatches = append(report.readerCapabilityMismatches, fmt.Sprintf("%d (%s): db %t, detected %t (%s)", a.ID, a.StoragePath, a.CanRead, capability.CanRead, format.FormatLabel(capability.Format)))
					}
				}
			}
		}

		cPath, err := storage.BookPath(template, assetBookPathData(a))
		if err != nil {
			return nil, err
		}
		if a.StoragePath != cPath {
			report.staleLayouts = append(report.staleLayouts, fmt.Sprintf("%d: %s -> %s", a.ID, a.StoragePath, cPath))
		}
		canonicalPaths = append(canonicalPaths, storage.BookPathCandidate{AssetID: a.ID, Path: cPath})
	}

	for _, collision := range storage.DetectBookPathCollisions(canonicalPaths) {
		report.pathCollisions = append(report.pathCollisions, fmt.Sprintf("%s: assets %v", collision.Path, collision.AssetIDs))
	}
	return referencedPaths, nil
}

func (report *checkReport) checkCoverOriginals(ctx context.Context, dataRoot storage.Root, bookCovers []db.BookCoverRow) (map[string]bool, error) {
	referencedCoverPaths := make(map[string]bool)
	for _, w := range bookCovers {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		rel := covers.OriginalPath(w.ID)
		absPath, err := dataRoot.Resolve(rel)
		if err != nil {
			report.invalidStoragePaths = append(report.invalidStoragePaths, fmt.Sprintf("cover %d (%s): %v", w.ID, rel, err))
			continue
		}
		if w.CoverVersion > 0 {
			referencedCoverPaths[absPath] = true
			info, err := os.Stat(absPath)
			if os.IsNotExist(err) {
				report.missingCoverOriginals = append(report.missingCoverOriginals, fmt.Sprintf("%d (%s)", w.ID, rel))
			} else if err != nil {
				report.ioErrors = append(report.ioErrors, fmt.Sprintf("stat cover %s: %v", rel, err))
			} else if info.IsDir() {
				report.ioErrors = append(report.ioErrors, fmt.Sprintf("stat cover %s: is a directory", rel))
			}
		}
	}
	return referencedCoverPaths, nil
}

func (report *checkReport) checkBookFiles(ctx context.Context, root storage.Root, referencedPaths, pendingWritebackTemps map[string]bool) error {
	booksDir := root.BooksDir()
	err := storage.WalkBooks(root, func(path string, info os.FileInfo, err error) error {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			report.ioErrors = append(report.ioErrors, fmt.Sprintf("walk books %s: %v", relToRoot(root, path), err))
			return nil
		}
		if info.IsDir() {
			if path != booksDir {
				f, err := os.Open(path)
				if err == nil {
					_, err = f.Readdirnames(1)
					if err == io.EOF {
						report.emptyDirs = append(report.emptyDirs, relToRoot(root, path))
					}
					f.Close()
				} else {
					report.ioErrors = append(report.ioErrors, fmt.Sprintf("read dir %s: %v", relToRoot(root, path), err))
				}
			}
		} else {
			if storage.IsWritebackTempFileName(info.Name()) {
				if !pendingWritebackTemps[path] {
					report.orphanWritebackTemps = append(report.orphanWritebackTemps, relToRoot(root, path))
				}
				return nil
			}
			if !referencedPaths[path] {
				report.orphanFiles = append(report.orphanFiles, relToRoot(root, path))
			}
		}
		return nil
	})
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if err != nil && !os.IsNotExist(err) {
		report.ioErrors = append(report.ioErrors, fmt.Sprintf("walk books: %v", err))
	}
	return nil
}

func (report *checkReport) checkStagingFiles(ctx context.Context, root, dataRoot storage.Root) error {
	// Book assets stage under the books root; covers stage under the data root.
	// Walk both (skipping a duplicate when they coincide) so leftover staged
	// files from an interrupted import surface wherever they landed.
	walkedStaging := map[string]bool{}
	for _, stagingRoot := range []storage.Root{root, dataRoot} {
		stagingDir := stagingRoot.StagingDir()
		if walkedStaging[stagingDir] {
			continue
		}
		walkedStaging[stagingDir] = true
		err := filepath.Walk(stagingDir, func(path string, info os.FileInfo, err error) error {
			if cause := context.Cause(ctx); cause != nil {
				return cause
			}
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				report.ioErrors = append(report.ioErrors, fmt.Sprintf("walk staging %s: %v", relToRoot(stagingRoot, path), err))
				return nil
			}
			if !info.IsDir() {
				report.stagedFiles = append(report.stagedFiles, relToRoot(stagingRoot, path))
			}
			return nil
		})
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		if err != nil && !os.IsNotExist(err) {
			report.ioErrors = append(report.ioErrors, fmt.Sprintf("walk staging: %v", err))
		}
	}
	return nil
}

func (report *checkReport) checkOrphanCoverOriginals(ctx context.Context, dataRoot storage.Root, referencedCoverPaths map[string]bool) error {
	coversOriginalsDir := dataRoot.Abs("covers")
	err := filepath.Walk(coversOriginalsDir, func(path string, info os.FileInfo, err error) error {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			report.ioErrors = append(report.ioErrors, fmt.Sprintf("walk cover originals %s: %v", relToRoot(dataRoot, path), err))
			return nil
		}
		if !info.IsDir() && !referencedCoverPaths[path] {
			report.orphanCoverOriginals = append(report.orphanCoverOriginals, relToRoot(dataRoot, path))
		}
		return nil
	})
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if err != nil && !os.IsNotExist(err) {
		report.ioErrors = append(report.ioErrors, fmt.Sprintf("walk cover originals: %v", err))
	}
	return nil
}

func (report *checkReport) print() error {
	hasErrors := false
	for _, section := range []checkReportSection{
		{"Invalid storage paths", report.invalidStoragePaths},
		{"Missing files", report.missingFiles},
		{"Missing cover originals", report.missingCoverOriginals},
		{"Stale layout / drift", report.staleLayouts},
		{"Storage path collisions", report.pathCollisions},
		{"Missing current sizes", report.missingCurrentSizes},
		{"Current size mismatches", report.sizeMismatches},
		{"Current hash mismatches", report.hashMismatches},
		{"Format mismatches", report.formatMismatches},
		{"Reader capability mismatches", report.readerCapabilityMismatches},
		{"I/O errors", report.ioErrors},
		{"Orphan files", report.orphanFiles},
		{"Orphan cover originals", report.orphanCoverOriginals},
		{"Pending metadata write-back attempts", report.pendingWritebackAttempts},
		{"Orphan metadata write-back temps", report.orphanWritebackTemps},
		{"Staged files", report.stagedFiles},
		{"Empty directories", report.emptyDirs},
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
