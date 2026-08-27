package ingest

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/levmv/polka/internal/appsettings"
)

const (
	pathSettingKey          = "ingest_path"
	enabledSettingKey       = "ingest_enabled"
	deleteSourcesSettingKey = "ingest_delete_sources"
	defaultPath             = "ingest"
)

type Config struct {
	Path          string
	Enabled       bool
	DeleteSources bool
}

// ResolvePath resolves the configured drop-folder path against dataDir.
// Relative paths live under app data by default; the files are incoming source
// material, not book files in the books folder.
func ResolvePath(dataDir, configured string) (string, error) {
	if dataDir == "" {
		return "", errors.New("data directory is required")
	}
	configured = strings.TrimSpace(configured)
	if configured == "" {
		configured = defaultPath
	}

	path := configured
	if !filepath.IsAbs(path) {
		path = filepath.Join(dataDir, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve ingest path: %w", err)
	}
	return filepath.Clean(abs), nil
}

// OpenPath reads the configured ingest path. Missing rows fall back to
// dataDir/ingest, preserving the simple default layout.
func OpenPath(q appsettings.Queryer, dataDir string) (string, error) {
	configured, ok, err := appsettings.Get(q, pathSettingKey)
	if err != nil {
		return "", fmt.Errorf("load ingest path: %w", err)
	}
	if !ok {
		return ResolvePath(dataDir, defaultPath)
	}
	return ResolvePath(dataDir, configured)
}

func OpenEnabled(q appsettings.Queryer) (bool, error) {
	return appsettings.GetBool(q, enabledSettingKey, true)
}

func OpenDeleteSources(q appsettings.Queryer) (bool, error) {
	return appsettings.GetBool(q, deleteSourcesSettingKey, false)
}

func OpenConfig(q appsettings.Queryer, dataDir string) (Config, error) {
	path, err := OpenPath(q, dataDir)
	if err != nil {
		return Config{}, err
	}
	enabled, err := OpenEnabled(q)
	if err != nil {
		return Config{}, err
	}
	deleteSources, err := OpenDeleteSources(q)
	if err != nil {
		return Config{}, err
	}
	return Config{Path: path, Enabled: enabled, DeleteSources: deleteSources}, nil
}

// SavePath stores an ingest path and returns its resolved absolute path.
func SavePath(exec appsettings.Execer, dataDir, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		configured = defaultPath
	}
	path, err := ResolvePath(dataDir, configured)
	if err != nil {
		return "", err
	}
	if err := appsettings.Set(exec, pathSettingKey, filepath.Clean(configured)); err != nil {
		return "", fmt.Errorf("save ingest path: %w", err)
	}
	return path, nil
}

func SaveEnabled(exec appsettings.Execer, enabled bool) error {
	return appsettings.SetBool(exec, enabledSettingKey, enabled)
}

func SaveDeleteSources(exec appsettings.Execer, deleteSources bool) error {
	return appsettings.SetBool(exec, deleteSourcesSettingKey, deleteSources)
}

func SaveConfig(exec appsettings.Execer, dataDir string, cfg Config) (Config, error) {
	path, err := SavePath(exec, dataDir, cfg.Path)
	if err != nil {
		return Config{}, err
	}
	if err := SaveEnabled(exec, cfg.Enabled); err != nil {
		return Config{}, err
	}
	if err := SaveDeleteSources(exec, cfg.DeleteSources); err != nil {
		return Config{}, err
	}
	return Config{Path: path, Enabled: cfg.Enabled, DeleteSources: cfg.DeleteSources}, nil
}
