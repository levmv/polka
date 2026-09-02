package cli

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/importer"
	"github.com/levmv/polka/internal/pdfcover"
	"github.com/levmv/polka/internal/storage"
)

var errImportItemsFailed = errors.New("some files failed to import or clean up")

type importCommandOptions struct {
	jsonOutput    bool
	dryRun        bool
	deleteSources bool
	displayRoot   string
}

type importReport struct {
	Source        string          `json:"source"`
	DryRun        bool            `json:"dry_run"`
	DeleteSources bool            `json:"delete_sources,omitzero"`
	Summary       importSummary   `json:"summary"`
	Items         []importOutcome `json:"items"`
}

type importSummary struct {
	Imported       int `json:"imported"`
	WouldImport    int `json:"would_import"`
	Duplicates     int `json:"duplicates"`
	Trashed        int `json:"trashed"`
	Restored       int `json:"restored"`
	Skipped        int `json:"skipped"`
	Warnings       int `json:"warnings"`
	Errors         int `json:"errors"`
	DeletedSources int `json:"deleted_sources,omitzero"`
}

// importOutcome is the single reporting model for one source. JSON encoding,
// the human line, and aggregate counters are all derived from it so a new
// status cannot be added to one channel while being forgotten by another.
// formatLabel, storagePath, and sourceDeleted are command-local details used by
// the human renderer and summary; they are deliberately not additional JSON
// contracts.
type importOutcome struct {
	Source   string          `json:"source"`
	Status   string          `json:"status"`
	WorkID   string          `json:"work_id,omitempty"`
	InTrash  bool            `json:"in_trash,omitzero"`
	Restored bool            `json:"restored,omitzero"`
	AssetID  string          `json:"asset_id,omitempty"`
	Format   string          `json:"format,omitempty"`
	Title    string          `json:"title,omitempty"`
	Authors  []string        `json:"authors,omitempty"`
	Warnings []string        `json:"warnings,omitempty"`
	Error    string          `json:"error,omitempty"`
	Assets   []importOutcome `json:"assets,omitempty"`

	formatLabel   string
	storagePath   string
	sourceDeleted bool
}

func runImport(ctx context.Context, dataDir string, args []string) error {
	fs := commandFlagSet("import", "polka import [--json] [--dry-run] [--delete-sources] <path>")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	dryRun := fs.Bool("dry-run", false, "scan and report without writing SQLite or managed storage")
	deleteSources := fs.Bool("delete-sources", false, "delete source files/directories after successful or duplicate import")
	if help, err := parseCommandFlags(fs, args); help || err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return reportedErrorf("usage: polka import [--json] [--dry-run] [--delete-sources] <path>")
	}
	return runImportPath(ctx, dataDir, fs.Arg(0), importCommandOptions{
		jsonOutput:    *jsonOutput,
		dryRun:        *dryRun,
		deleteSources: *deleteSources,
	})
}

func runImportPath(ctx context.Context, dataDir, srcPath string, opts importCommandOptions) error {
	info, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	report := importReport{
		Source:        srcPath,
		DryRun:        opts.dryRun,
		DeleteSources: opts.deleteSources,
	}
	if !info.IsDir() && !importer.IsSupportedBook(srcPath) {
		err := fmt.Errorf("unsupported book file extension: %s", filepath.Base(srcPath))
		recordImportOutcome(&report, opts, importOutcome{
			Source: srcPath,
			Status: "error",
			Error:  err.Error(),
		}, true)
		if opts.jsonOutput {
			if err := writeImportJSON(report); err != nil {
				return err
			}
		}
		return errors.Join(errImportItemsFailed, err)
	}
	if info.IsDir() {
		srcPath, err = filepath.EvalSymlinks(srcPath)
		if err != nil {
			return fmt.Errorf("resolve import folder symlinks: %w", err)
		}
	}

	// Import intentionally remains usable while serve is running. SQLite
	// serializes catalog writes; rare filesystem races are recoverable and not
	// worth blocking normal use.
	var database *db.DB
	if opts.dryRun {
		database, err = openDatabase(dataDir)
	} else {
		database, err = ensureLibraryInitialized(dataDir)
	}
	if err != nil {
		return err
	}
	defer database.Close()

	var runErr error
	if info.IsDir() {
		opts.displayRoot = srcPath
		runErr = importFolderPath(ctx, database, dataDir, srcPath, opts, &report)
	} else {
		runErr = importSinglePath(ctx, database, dataDir, srcPath, opts, &report)
	}

	if opts.jsonOutput {
		if err := writeImportJSON(report); err != nil && runErr == nil {
			runErr = err
		}
	}
	if runErr != nil && report.Summary.Errors > 0 {
		if errors.Is(runErr, errImportItemsFailed) {
			return runErr
		}
		return errors.Join(errImportItemsFailed, runErr)
	}
	return runErr
}

func importSinglePath(ctx context.Context, database *db.DB, dataDir, srcPath string, opts importCommandOptions, report *importReport) error {
	if opts.dryRun {
		item, err := probeImportOutcome(ctx, database, srcPath)
		recordImportOutcome(report, opts, item, true)
		return err
	}

	root, coverRoot, template, err := openImportStorage(database, dataDir)
	if err != nil {
		return err
	}
	if err := rejectManagedImportSource(srcPath, root); err != nil {
		return err
	}
	renderer := pdfcover.NewRenderer()
	defer renderer.Close()

	res, err := importer.ImportFile(ctx, database, root, srcPath, renderer, importer.Options{PathTemplate: template, CoverRoot: coverRoot})
	item := importOutcome{Source: srcPath}
	if err != nil {
		item.Status = "error"
		item.Error = err.Error()
		recordImportOutcome(report, opts, item, true)
		return err
	}

	item = importOutcomeFromResult(srcPath, res)
	if opts.deleteSources {
		deleteImportedSource(srcPath, false, &item)
	}
	recordImportOutcome(report, opts, item, true)
	return itemError(item)
}

func importFolderPath(ctx context.Context, database *db.DB, dataDir, rootPath string, opts importCommandOptions, report *importReport) error {
	var root storage.Root
	var coverRoot storage.Root
	var template string
	var renderer *pdfcover.Renderer
	if !opts.dryRun {
		var err error
		root, coverRoot, template, err = openImportStorage(database, dataDir)
		if err != nil {
			return err
		}
		if err := rejectManagedImportSource(rootPath, root); err != nil {
			return err
		}
		renderer = pdfcover.NewRenderer()
		defer renderer.Close()
	}

	err := filepath.WalkDir(rootPath, func(path string, d os.DirEntry, walkErr error) error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if walkErr != nil {
			recordImportError(report, opts, path, fmt.Errorf("walk error %w", walkErr))
			return nil
		}

		if d.IsDir() {
			sources, ok, detectErr := importer.CalibreBookSources(path)
			if detectErr != nil {
				recordImportError(report, opts, path, detectErr)
				return filepath.SkipDir
			}
			if ok {
				var item importOutcome
				if opts.dryRun {
					var probeErr error
					item, probeErr = probeImportGroup(ctx, database, path, sources)
					if probeErr != nil {
						return probeErr
					}
				} else {
					item = importGroup(ctx, database, root, coverRoot, template, renderer, path, sources)
					if item.Error != "" {
						if cause := context.Cause(ctx); cause != nil {
							return cause
						}
					}
					if opts.deleteSources && item.Error == "" {
						deleteImportedSource(path, true, &item)
					}
				}
				recordImportOutcome(report, opts, item, false)
				return filepath.SkipDir
			}
			return nil
		}

		if !d.Type().IsRegular() || !importer.IsSupportedBook(d.Name()) {
			recordImportOutcome(report, opts, importOutcome{Source: path, Status: "skipped"}, false)
			return nil
		}

		if opts.dryRun {
			item, probeErr := probeImportOutcome(ctx, database, path)
			if probeErr != nil {
				if cause := context.Cause(ctx); cause != nil {
					return cause
				}
			}
			recordImportOutcome(report, opts, item, false)
			return nil
		}
		res, importErr := importer.ImportFile(ctx, database, root, path, renderer, importer.Options{PathTemplate: template, CoverRoot: coverRoot})
		item := importOutcome{Source: path}
		if importErr != nil {
			if cause := context.Cause(ctx); cause != nil {
				return cause
			}
			item.Status = "error"
			item.Error = importErr.Error()
			recordImportOutcome(report, opts, item, false)
			return nil
		}
		item = importOutcomeFromResult(path, res)
		if opts.deleteSources {
			deleteImportedSource(path, false, &item)
		}
		recordImportOutcome(report, opts, item, false)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk dir: %w", err)
	}

	if !opts.jsonOutput {
		printImportSummary(report.Summary, opts.dryRun, opts.deleteSources)
	}
	if report.Summary.Errors > 0 {
		return errImportItemsFailed
	}
	return nil
}

// openImportStorage resolves the books root, the cover root (app data dir, where
// cover files live), and the path template for an import run. It applies the
// same write guard as upload/ingest/relayout: the books folder must be present
// and must not look empty when the catalog already has books (the classic
// dropped-mount shadow write).
func openImportStorage(database db.Queryer, dataDir string) (storage.Root, storage.Root, string, error) {
	root, err := storage.OpenRoot(database, dataDir)
	if err != nil {
		return storage.Root{}, storage.Root{}, "", err
	}
	root, err = requireStorageLayout(dataDir, root)
	if err != nil {
		return storage.Root{}, storage.Root{}, "", err
	}
	catalogHasBooks, err := db.HasAnyAsset(database)
	if err != nil {
		return storage.Root{}, storage.Root{}, "", err
	}
	if err := storage.RequireWritableRoot(root, catalogHasBooks); err != nil {
		return storage.Root{}, storage.Root{}, "", err
	}
	template, err := storage.OpenBookPathTemplate(database)
	if err != nil {
		return storage.Root{}, storage.Root{}, "", err
	}
	return root, storage.NewRoot(dataDir), template, nil
}

func rejectManagedImportSource(source string, root storage.Root) error {
	sourcePath, err := filepath.Abs(source)
	if err != nil {
		return fmt.Errorf("resolve import source: %w", err)
	}
	sourcePath, err = filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return fmt.Errorf("resolve import source symlinks: %w", err)
	}
	rootPath, err := filepath.Abs(root.Path)
	if err != nil {
		return fmt.Errorf("resolve managed books folder: %w", err)
	}
	rootPath, err = filepath.EvalSymlinks(rootPath)
	if err != nil {
		return fmt.Errorf("resolve managed books folder symlinks: %w", err)
	}
	_, sourceIsManaged := pathRelativeTo(rootPath, sourcePath)
	_, rootIsUnderSource := pathRelativeTo(sourcePath, rootPath)
	if sourceIsManaged || rootIsUnderSource {
		return fmt.Errorf("import source %s overlaps the managed books folder %s", source, root.Path)
	}
	return nil
}

func probeImportOutcome(ctx context.Context, database db.Queryer, path string) (importOutcome, error) {
	probe, err := importer.ProbeSource(ctx, database, importer.Source{Path: path})
	if err != nil {
		return importOutcome{Source: path, Status: "error", Error: err.Error()}, err
	}
	if probe.Duplicate {
		return importOutcomeFromResult(path, probe.Existing), nil
	}
	return importOutcome{
		Source:      path,
		Status:      "would_import",
		Format:      importFormatKey(probe.Format),
		formatLabel: probe.Format,
	}, nil
}

func probeImportGroup(ctx context.Context, database db.Queryer, path string, sources []importer.Source) (importOutcome, error) {
	item := importOutcome{Source: path}
	groupErrors := 0
	groupNew := 0
	for _, source := range sources {
		assetItem, err := probeImportOutcome(ctx, database, source.Path)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return importOutcome{}, cause
			}
		}
		item.Assets = append(item.Assets, assetItem)
		switch assetItem.Status {
		case "error":
			groupErrors++
		case "would_import":
			groupNew++
		}
	}
	if groupErrors > 0 {
		item.Status = "error"
		item.Error = formatCount(groupErrors, "asset error", "asset errors")
		return item, nil
	}
	if groupNew > 0 {
		item.Status = "would_import"
		return item, nil
	}
	item.Status = "duplicate"
	return item, nil
}

func importGroup(ctx context.Context, database *db.DB, root, coverRoot storage.Root, template string, renderer *pdfcover.Renderer, path string, sources []importer.Source) importOutcome {
	group, err := importer.ImportGroup(ctx, database, root, sources, renderer, importer.Options{PathTemplate: template, CoverRoot: coverRoot})
	item := importOutcome{Source: path}
	if err != nil {
		item.Status = "error"
		item.Error = err.Error()
		return item
	}

	item.WorkID = group.WorkID
	item.Title = group.Title
	item.Authors = group.Authors
	item.Restored = group.Restored
	item.Warnings = importWarnings(group.Warnings)
	groupImported := 0
	for i, res := range group.Results {
		sourcePath := ""
		if i < len(sources) {
			sourcePath = sources[i].Path
		}
		assetItem := importOutcomeFromResult(sourcePath, res)
		item.Assets = append(item.Assets, assetItem)
		if res.Status != importer.StatusDuplicate {
			groupImported++
		}
	}
	if groupImported > 0 {
		item.Status = "imported"
	} else {
		item.Status = "duplicate"
	}
	return item
}

func recordImportError(report *importReport, opts importCommandOptions, path string, err error) {
	recordImportOutcome(report, opts, importOutcome{
		Source: path,
		Status: "error",
		Error:  err.Error(),
	}, false)
}

func importOutcomeFromResult(source string, res importer.Result) importOutcome {
	return importOutcome{
		Source:      source,
		Status:      string(res.Status),
		WorkID:      res.WorkID,
		InTrash:     res.WorkTrashed,
		AssetID:     res.AssetID,
		Format:      importFormatKey(res.Format),
		Title:       res.Title,
		Authors:     res.Authors,
		Warnings:    importWarnings(res.Warnings),
		formatLabel: res.Format,
		storagePath: res.StoragePath,
	}
}

func importFormatKey(label string) string {
	return strings.ToLower(strings.TrimSpace(label))
}

func importWarnings(warnings []error) []string {
	if len(warnings) == 0 {
		return nil
	}
	out := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		out = append(out, warning.Error())
	}
	return out
}

func deleteImportedSource(path string, directory bool, item *importOutcome) {
	var err error
	if directory {
		err = os.RemoveAll(path)
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		item.Error = "delete source: " + err.Error()
		return
	}
	item.sourceDeleted = true
}

func recordImportOutcome(report *importReport, opts importCommandOptions, item importOutcome, detailed bool) {
	addImportOutcomeSummary(&report.Summary, item)
	if opts.jsonOutput {
		report.Items = append(report.Items, item)
		return
	}
	printImportOutcome(item, detailed, opts.dryRun, opts.displayRoot)
}

func addImportOutcomeSummary(summary *importSummary, item importOutcome) {
	errorsBefore := summary.Errors
	if len(item.Assets) == 0 {
		switch item.Status {
		case "imported":
			summary.Imported++
		case "would_import":
			summary.WouldImport++
		case "duplicate":
			summary.Duplicates++
			if item.InTrash {
				summary.Trashed++
			}
		case "skipped":
			summary.Skipped++
		case "error":
			summary.Errors++
		}
	} else {
		for _, asset := range item.Assets {
			addImportOutcomeSummary(summary, asset)
		}
		// A group-level error normally summarizes failed children. Count it only
		// when no child carried the actual error.
		if item.Status == "error" && summary.Errors == errorsBefore {
			summary.Errors++
		}
	}

	summary.Warnings += len(item.Warnings)
	if item.Restored {
		summary.Restored++
	}
	if item.sourceDeleted {
		summary.DeletedSources++
	}
	// Source cleanup happens after a successful import/duplicate outcome, so
	// its error is additional to the status rather than represented by it.
	if item.Error != "" && item.Status != "error" {
		summary.Errors++
	}
}

func itemError(item importOutcome) error {
	if item.Error != "" {
		return errImportItemsFailed
	}
	return nil
}

func printImportOutcome(item importOutcome, detailed, dryRun bool, displayRoot string) {
	printImportOutcomeWarnings(item)

	source := displayImportSource(item.Source, displayRoot)
	counts := importSummary{}
	addImportOutcomeSummary(&counts, item)
	switch item.Status {
	case "error":
		fmt.Printf("ERR   %s: %s\n", source, item.Error)
		for _, asset := range item.Assets {
			if asset.Error != "" {
				fmt.Printf("      %s: %s\n", displayImportSource(asset.Source, displayRoot), asset.Error)
			}
		}
	case "skipped":
		// Folder walks count unrelated files but keep normal output quiet.
	case "would_import":
		if len(item.Assets) == 0 {
			fmt.Printf("DRY   %s (%s)\n", source, outcomeFormatLabel(item))
			break
		}
		fmt.Printf("DRY   %s", source)
		var details []string
		if len(item.Assets) > 1 {
			details = append(details, fmt.Sprintf("assets %d", len(item.Assets)))
		}
		if counts.Duplicates > 0 {
			details = append(details, fmt.Sprintf("duplicates %d", counts.Duplicates))
		}
		if counts.Trashed > 0 {
			details = append(details, fmt.Sprintf("currently in Trash %d", counts.Trashed))
		}
		finishImportLine(details)
	case "duplicate":
		if len(item.Assets) > 0 {
			fmt.Printf("DUP   %s", source)
			var details []string
			if len(item.Assets) > 1 {
				details = append(details, fmt.Sprintf("assets %d", len(item.Assets)))
			}
			if counts.Trashed > 0 {
				details = append(details, "book in Trash")
			}
			finishImportLine(details)
		} else if detailed && !dryRun {
			fmt.Printf("Duplicate, skipped%s\n", inTrashSuffix(item.InTrash))
		} else {
			fmt.Printf("DUP   %s", source)
			var details []string
			if item.InTrash {
				details = append(details, "book in Trash")
			}
			finishImportLine(details)
		}
	case "imported":
		if len(item.Assets) > 0 {
			fmt.Printf("OK    %s", source)
			var details []string
			if len(item.Assets) > 1 {
				details = append(details, fmt.Sprintf("assets %d", len(item.Assets)))
			}
			if counts.Duplicates > 0 {
				details = append(details, fmt.Sprintf("duplicates %d", counts.Duplicates))
			}
			if item.Restored {
				details = append(details, "restored from Trash")
			}
			finishImportLine(details)
		} else if detailed {
			fmt.Printf("Imported: %s\n", item.Title)
			fmt.Printf("Authors:  %s\n", strings.Join(item.Authors, ", "))
			fmt.Printf("Format:   %s\n", outcomeFormatLabel(item))
			fmt.Printf("Path:     %s\n", item.storagePath)
		} else {
			fmt.Printf("OK    %s\n", source)
		}
	}

	if item.Error != "" && item.Status != "error" {
		fmt.Printf("ERR   %s: %s\n", source, item.Error)
	}
}

func finishImportLine(details []string) {
	if len(details) > 0 {
		fmt.Printf(" (%s)", strings.Join(details, ", "))
	}
	fmt.Println()
}

func displayImportSource(source, root string) string {
	if root == "" {
		return source
	}
	rel, ok := pathRelativeTo(root, source)
	if !ok {
		return source
	}
	if rel == "." {
		return filepath.Base(filepath.Clean(source))
	}
	return rel
}

func pathRelativeTo(root, target string) (string, bool) {
	rel, err := filepath.Rel(root, target)
	return rel, err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func printImportOutcomeWarnings(item importOutcome) {
	for _, warning := range item.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
	}
	for _, asset := range item.Assets {
		printImportOutcomeWarnings(asset)
	}
}

func outcomeFormatLabel(item importOutcome) string {
	if item.formatLabel != "" {
		return item.formatLabel
	}
	return item.Format
}

func inTrashSuffix(inTrash bool) string {
	if inTrash {
		return ", book in Trash"
	}
	return ""
}

func printImportSummary(summary importSummary, dryRun, deleteSources bool) {
	if dryRun {
		fmt.Printf("\n--- Dry Run Summary ---\n")
		fmt.Printf("Would import: %d\n", summary.WouldImport)
		fmt.Printf("Duplicates:   %d\n", summary.Duplicates)
		fmt.Printf("In Trash:    %d\n", summary.Trashed)
		fmt.Printf("Skipped:      %d\n", summary.Skipped)
		fmt.Printf("Errors:       %d\n", summary.Errors)
		return
	}
	fmt.Printf("\n--- Import Summary ---\n")
	fmt.Printf("Imported:   %d\n", summary.Imported)
	fmt.Printf("Duplicates: %d\n", summary.Duplicates)
	fmt.Printf("In Trash:   %d\n", summary.Trashed)
	fmt.Printf("Restored:   %d\n", summary.Restored)
	fmt.Printf("Skipped:    %d\n", summary.Skipped)
	fmt.Printf("Warnings:   %d\n", summary.Warnings)
	if deleteSources {
		fmt.Printf("Deleted sources: %d\n", summary.DeletedSources)
	}
	fmt.Printf("Errors:     %d\n", summary.Errors)
}

func writeImportJSON(report importReport) error {
	if err := json.MarshalWrite(os.Stdout, report, jsontext.WithIndent("  ")); err != nil {
		return err
	}
	_, err := fmt.Fprintln(os.Stdout)
	return err
}
