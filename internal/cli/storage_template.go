package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

func runStorage(ctx context.Context, dataDir string, args []string) error {
	if len(args) == 0 || helpRequested(args) {
		printStorageUsage()
		if len(args) == 0 {
			return reportedErrorf("usage: polka storage <template> [args]")
		}
		return nil
	}

	sub, rest := args[0], args[1:]
	switch sub {
	case "root":
		return runStorageRoot(ctx, dataDir, rest)
	case "template":
		return runStorageTemplate(ctx, dataDir, rest)
	default:
		printStorageUsage()
		return reportedErrorf("unknown storage command: %s", sub)
	}
}

func printStorageUsage() {
	fmt.Fprintf(os.Stderr, `Usage:
  polka storage root <command> [args]
  polka storage template <command> [args]

Commands:
  root       Manage the books folder
  template   Manage the book file path template
`)
}

func runStorageTemplate(ctx context.Context, dataDir string, args []string) error {
	if len(args) == 0 || helpRequested(args) {
		printStorageTemplateUsage()
		if len(args) == 0 {
			return reportedErrorf("missing storage template command")
		}
		return nil
	}
	if len(args) > 1 && isHelpArg(args[1]) {
		printStorageTemplateUsage()
		return nil
	}

	switch args[0] {
	case "default":
		if len(args) != 1 {
			printStorageTemplateUsage()
			return reportedErrorf("usage: polka storage template default")
		}
		fmt.Println(storage.DefaultBookPathTemplate)
		return nil
	case "current":
		if len(args) != 1 {
			printStorageTemplateUsage()
			return reportedErrorf("usage: polka storage template current")
		}
		return runStorageTemplateCurrent(dataDir)
	case "preview":
		if len(args) != 2 {
			printStorageTemplateUsage()
			return reportedErrorf("usage: polka storage template preview <template>")
		}
		return runStorageTemplatePreview(dataDir, args[1])
	case "apply":
		return runStorageTemplateApply(ctx, dataDir, args[1:])
	default:
		printStorageTemplateUsage()
		return reportedErrorf("unknown storage template command: %s", args[0])
	}
}

func printStorageTemplateUsage() {
	fmt.Fprintf(os.Stderr, `Usage:
  polka storage template default
  polka storage template current
  polka storage template preview <template>
  polka storage template apply [--force] <template>

The template is the layout of a book file within the books folder. The folder
is itself the books tree, so rendered paths are relative to it — bucket
directories sit directly under the folder, with no books/ prefix to name.

Fields:
  title sort_title author author_sort author_bucket
  series series_index series_bucket asset_id work_id
  original_filename ext dot_ext

Examples:
  polka storage template preview '%s'
  polka storage template preview '{author_bucket}/{author_sort}/{series}/{series_index|Standalone} - {title} [{asset_id}]{dot_ext}'
`, storage.DefaultBookPathTemplate)
}

func runStorageTemplateCurrent(dataDir string) error {
	database, err := openDatabaseReadOnly(dataDir)
	if err != nil {
		return err
	}
	defer database.Close()

	template, err := storage.OpenBookPathTemplate(database.DB)
	if err != nil {
		return err
	}
	fmt.Println(template)
	return nil
}

func runStorageTemplatePreview(dataDir, template string) error {
	database, err := openDatabaseReadOnly(dataDir)
	if err != nil {
		return err
	}
	defer database.Close()

	plan, err := buildStorageTemplatePlan(database, template)
	if err != nil {
		return err
	}
	printStorageTemplatePlan(plan)
	if len(plan.Collisions) > 0 || len(plan.RenderErrors) > 0 {
		return ErrIssuesFound
	}
	return nil
}

func runStorageTemplateApply(parent context.Context, dataDir string, args []string) (retErr error) {
	fs := commandFlagSet("storage template apply", "polka storage template apply [--force] <template>")
	force := fs.Bool("force", false, "override a fresh writer lease from another polka process")
	if help, err := parseCommandFlags(fs, args); help || err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		fs.Usage()
		return reportedErrorf("usage: polka storage template apply [--force] <template>")
	}
	template := fs.Args()[0]

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
	lease, err := acquireCLIWriterLease(parent, database, "storage-template-apply", *force)
	if err != nil {
		return err
	}
	defer func() { retErr = lease.finish(retErr) }()
	ctx := lease.Context()

	plan, err := buildStorageTemplatePlan(database, template)
	if err != nil {
		return err
	}
	printStorageTemplatePlan(plan)
	if len(plan.Collisions) > 0 || len(plan.RenderErrors) > 0 {
		return ErrIssuesFound
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}

	blockers := findStorageTemplateBlockers(root, plan.Changes)
	if len(blockers) > 0 {
		fmt.Println()
		fmt.Println("Blocked destination paths:")
		for _, b := range blockers {
			fmt.Printf("  - %s\n", b)
		}
		return ErrIssuesFound
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}

	saved, err := storage.SaveBookPathTemplate(database.DB, template)
	if err != nil {
		return err
	}
	warnings, err := applyStorageTemplateMoves(ctx, database, root, plan.Changes)
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Printf("Saved template: %s\n", saved)
	fmt.Printf("Moved files:    %d\n", len(plan.Changes)-len(warnings))
	if len(warnings) > 0 {
		fmt.Println()
		fmt.Println("Warnings:")
		for _, w := range warnings {
			fmt.Printf("  - %s\n", w)
		}
		return ErrIssuesFound
	}
	return nil
}

type storageTemplatePlan struct {
	Assets       int
	Changes      []storageTemplateChange
	Collisions   []storage.BookPathCollision
	RenderErrors []string
}

type storageTemplateChange struct {
	AssetID string
	OldPath string
	NewPath string
}

func buildStorageTemplatePlan(database *db.DB, template string) (storageTemplatePlan, error) {
	if err := storage.ValidateBookPathTemplate(template); err != nil {
		return storageTemplatePlan{}, err
	}
	assets, err := db.AllAssetsWithPrimaryAuthor(database)
	if err != nil {
		return storageTemplatePlan{}, err
	}

	plan := storageTemplatePlan{Assets: len(assets)}
	candidates := make([]storage.BookPathCandidate, 0, len(assets))
	for _, a := range assets {
		rel, err := storage.BookPath(template, assetBookPathData(a))
		if err != nil {
			plan.RenderErrors = append(plan.RenderErrors, fmt.Sprintf("%s: %v", a.ID, err))
			continue
		}
		candidates = append(candidates, storage.BookPathCandidate{AssetID: a.ID, Path: rel})
		if rel != a.StoragePath {
			plan.Changes = append(plan.Changes, storageTemplateChange{
				AssetID: a.ID,
				OldPath: a.StoragePath,
				NewPath: rel,
			})
		}
	}
	plan.Collisions = storage.DetectBookPathCollisions(candidates)
	return plan, nil
}

func printStorageTemplatePlan(plan storageTemplatePlan) {
	fmt.Printf("Assets:     %d\n", plan.Assets)
	fmt.Printf("Would move: %d\n", len(plan.Changes))
	fmt.Printf("Collisions: %d\n", len(plan.Collisions))
	fmt.Printf("Errors:     %d\n", len(plan.RenderErrors))

	if len(plan.Changes) > 0 {
		fmt.Println()
		fmt.Println("Sample changes:")
		for i, c := range plan.Changes {
			if i >= 20 {
				fmt.Printf("  ... %d more\n", len(plan.Changes)-i)
				break
			}
			fmt.Printf("  - %s: %s -> %s\n", c.AssetID, c.OldPath, c.NewPath)
		}
	}

	if len(plan.Collisions) > 0 {
		fmt.Println()
		fmt.Println("Path collisions:")
		for _, c := range plan.Collisions {
			fmt.Printf("  - %s\n", c.Path)
			fmt.Printf("    assets: %s\n", strings.Join(c.AssetIDs, ", "))
		}
	}

	if len(plan.RenderErrors) > 0 {
		fmt.Println()
		fmt.Println("Template errors:")
		for _, e := range plan.RenderErrors {
			fmt.Printf("  - %s\n", e)
		}
	}
}

func assetBookPathData(a db.AssetWithAuthorRow) storage.BookPathData {
	originalFilename := strings.TrimSpace(a.OriginalFilename)
	if originalFilename == "" {
		originalFilename = filepath.Base(a.StoragePath)
	}
	return storage.BookPathData{
		Title:            a.Title,
		SortTitle:        a.SortTitle,
		Author:           a.AuthorName,
		AuthorSort:       a.AuthorSortName,
		Series:           a.Series,
		SeriesIndex:      a.SeriesIndex,
		AssetID:          a.ID,
		WorkID:           a.WorkID,
		Ext:              a.Extension,
		OriginalFilename: originalFilename,
	}
}

func findStorageTemplateBlockers(root storage.Root, changes []storageTemplateChange) []string {
	oldPaths := make(map[string]bool, len(changes))
	for _, c := range changes {
		oldPaths[c.OldPath] = true
	}

	var blockers []string
	for _, c := range changes {
		if oldPaths[c.NewPath] {
			continue
		}
		newPath, err := root.Resolve(c.NewPath)
		if err != nil {
			blockers = append(blockers, fmt.Sprintf("%s: %v", c.NewPath, err))
			continue
		}
		if _, err := os.Stat(newPath); err == nil {
			blockers = append(blockers, c.NewPath)
		} else if err != nil && !os.IsNotExist(err) {
			blockers = append(blockers, fmt.Sprintf("%s: %v", c.NewPath, err))
		}
	}
	return blockers
}

func applyStorageTemplateMoves(ctx context.Context, database *db.DB, root storage.Root, changes []storageTemplateChange) ([]string, error) {
	if len(changes) == 0 {
		return nil, nil
	}

	type stagedChange struct {
		storageTemplateChange
		StagedPath string
	}

	var staged []stagedChange
	var warnings []string
	for _, c := range changes {
		if err := context.Cause(ctx); err != nil {
			return warnings, err
		}
		oldPath, err := root.Resolve(c.OldPath)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: invalid source path %s: %v", c.AssetID, c.OldPath, err))
			continue
		}
		if _, err := os.Stat(oldPath); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: source missing at %s", c.AssetID, c.OldPath))
			continue
		}
		stageRel := storage.StagingRelPath(fmt.Sprintf("[%s]-%s", c.AssetID, filepath.Base(c.OldPath)))
		if err := storage.Move(root, c.OldPath, stageRel); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: stage %s: %v", c.AssetID, c.OldPath, err))
			continue
		}
		staged = append(staged, stagedChange{storageTemplateChange: c, StagedPath: stageRel})
	}

	for _, c := range staged {
		if err := context.Cause(ctx); err != nil {
			return warnings, err
		}
		if err := storage.Move(root, c.StagedPath, c.NewPath); err != nil {
			if backErr := storage.Move(root, c.StagedPath, c.OldPath); backErr != nil {
				warnings = append(warnings, fmt.Sprintf("%s: move to %s failed (%v), rollback to %s also failed (%v); run `polka repair`", c.AssetID, c.NewPath, err, c.OldPath, backErr))
			} else {
				warnings = append(warnings, fmt.Sprintf("%s: move to %s failed: %v", c.AssetID, c.NewPath, err))
			}
			continue
		}
		if _, err := database.Exec("UPDATE assets SET storage_path = ?, filename = ?, updated_at = unixepoch() WHERE id = ?", c.NewPath, filepath.Base(c.NewPath), c.AssetID); err != nil {
			if backErr := storage.Move(root, c.NewPath, c.OldPath); backErr != nil {
				warnings = append(warnings, fmt.Sprintf("%s: update DB failed (%v), rollback to %s also failed (%v); run `polka repair`", c.AssetID, err, c.OldPath, backErr))
			} else {
				warnings = append(warnings, fmt.Sprintf("%s: update DB failed: %v", c.AssetID, err))
			}
		}
	}

	return warnings, nil
}
