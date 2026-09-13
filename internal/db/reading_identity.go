package db

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// LibraryIdentity is the stable namespace shared by catalog and export IDs.
func LibraryIdentity(queryer Queryer) (string, error) {
	var id string
	err := queryer.QueryRow("SELECT value FROM app_settings WHERE key = 'library_id'").Scan(&id)
	return id, err
}

func PublicationURI(libraryID string, assetID int64) string {
	return fmt.Sprintf("urn:polka:%s:asset:%d", libraryID, assetID)
}

func BookURI(libraryID string, bookID int64) string {
	return fmt.Sprintf("urn:polka:%s:book:%d", libraryID, bookID)
}

func AnnotationURI(libraryID string, annotationID int64) string {
	return fmt.Sprintf("urn:polka:%s:annotation:%d", libraryID, annotationID)
}

func ResourceURI(publicationURI, href string) string {
	if href == "" {
		return publicationURI
	}
	return publicationURI + ":resource:" + hex.EncodeToString([]byte(strings.TrimPrefix(href, "./")))
}
