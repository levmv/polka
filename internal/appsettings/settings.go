package appsettings

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func Get(q Queryer, key string) (string, bool, error) {
	var value string
	err := q.QueryRow("SELECT value FROM app_settings WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func Set(exec Execer, key, value string) error {
	_, err := exec.Exec(`
		INSERT INTO app_settings (key, value, updated_at)
		VALUES (?, ?, unixepoch())
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = unixepoch()
	`, key, value)
	return err
}

func GetBool(q Queryer, key string, defaultValue bool) (bool, error) {
	raw, ok, err := Get(q, key)
	if err != nil {
		return false, fmt.Errorf("load setting %s: %w", key, err)
	}
	if !ok {
		return defaultValue, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid %s value %q", key, raw)
	}
}

func SetBool(exec Execer, key string, enabled bool) error {
	value := "0"
	if enabled {
		value = "1"
	}
	if err := Set(exec, key, value); err != nil {
		return fmt.Errorf("save setting %s: %w", key, err)
	}
	return nil
}

func GetInt(q Queryer, key string, defaultValue int) (int, error) {
	raw, ok, err := Get(q, key)
	if err != nil {
		return 0, fmt.Errorf("load setting %s: %w", key, err)
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("invalid %s value %q", key, raw)
	}
	return value, nil
}

func SetInt(exec Execer, key string, value int) error {
	if err := Set(exec, key, strconv.Itoa(value)); err != nil {
		return fmt.Errorf("save setting %s: %w", key, err)
	}
	return nil
}
