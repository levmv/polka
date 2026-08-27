package storage

import (
	"fmt"
	"strings"

	"github.com/levmv/polka/internal/appsettings"
)

const templateSettingKey = "book_path_template"

func OpenBookPathTemplate(q appsettings.Queryer) (string, error) {
	template, ok, err := appsettings.Get(q, templateSettingKey)
	if err != nil {
		return "", fmt.Errorf("load book path template: %w", err)
	}
	if !ok {
		return DefaultBookPathTemplate, nil
	}
	template = strings.TrimSpace(template)
	if template == "" {
		return DefaultBookPathTemplate, nil
	}
	return template, nil
}

func SaveBookPathTemplate(exec appsettings.Execer, template string) (string, error) {
	template = strings.TrimSpace(template)
	if template == "" {
		template = DefaultBookPathTemplate
	}
	if err := ValidateBookPathTemplate(template); err != nil {
		return "", err
	}
	if err := appsettings.Set(exec, templateSettingKey, template); err != nil {
		return "", fmt.Errorf("save book path template: %w", err)
	}
	return template, nil
}

func ValidateBookPathTemplate(template string) error {
	_, err := RenderBookPathTemplate(template, BookPathData{
		Title:            "Example Book",
		SortTitle:        "Example Book",
		Author:           "Example Author",
		AuthorSort:       "Author, Example",
		Series:           "Example Series",
		SeriesIndex:      "01",
		AssetID:          "a_example",
		WorkID:           "w_example",
		Ext:              "epub",
		OriginalFilename: "example.epub",
	})
	if err != nil {
		return fmt.Errorf("invalid book path template: %w", err)
	}
	return nil
}
