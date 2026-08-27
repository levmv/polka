package kobo

import (
	"encoding/json/v2"
	"strings"
	"testing"
)

func TestBuildSyncItemPinsNewEntitlementShape(t *testing.T) {
	seriesIndex := 2.5
	item := BuildSyncItem(Change{
		AssetID:       "a_book",
		WorkID:        "w_book",
		Size:          123,
		Title:         "A Book",
		Description:   "Description",
		Publisher:     "Press",
		PublishedDate: "2024-03-02",
		Language:      "en",
		Series:        "Sequence",
		SeriesIndex:   &seriesIndex,
		Authors:       []string{"Ada Author"},
		AddedAt:       100,
		ModifiedAt:    200,
		Revision:      1,
		FirstRevision: 1,
		Present:       true,
		ChangedAt:     200,
	}, 0, "https://books.test/kobo/secret")

	if item.NewEntitlement == nil || item.ChangedEntitlement != nil {
		t.Fatalf("item = %+v", item)
	}
	payload := item.NewEntitlement
	if payload.BookEntitlement.ID != "a_book" || payload.BookEntitlement.IsRemoved {
		t.Fatalf("entitlement = %+v", payload.BookEntitlement)
	}
	metadata := payload.BookMetadata
	if metadata == nil || metadata.Title != "A Book" || metadata.WorkID != "a_book" {
		t.Fatalf("metadata = %+v", metadata)
	}
	if len(metadata.DownloadURLs) != 1 || metadata.DownloadURLs[0].URL != "https://books.test/kobo/secret/download/a_book/kepub" {
		t.Fatalf("downloads = %+v", metadata.DownloadURLs)
	}
	if metadata.PublicationDate != "2024-03-02T00:00:00Z" || metadata.Series == nil || metadata.Series.Number != 2.5 {
		t.Fatalf("publication/series = %q %+v", metadata.PublicationDate, metadata.Series)
	}
	if metadata.Series.ID != "58ded003-fe86-5d0e-8a78-ef3135b960ff" {
		t.Fatalf("series id = %q", metadata.Series.ID)
	}

	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"NewEntitlement", "BookEntitlement", "BookMetadata", "DownloadUrls", "CoverImageId"} {
		if !strings.Contains(string(encoded), `"`+key+`"`) {
			t.Errorf("encoded item lacks %q: %s", key, encoded)
		}
	}
}

func TestBuildSyncItemMakesRemovalAChangedEntitlementWithoutMetadata(t *testing.T) {
	item := BuildSyncItem(Change{
		AssetID: "a_book", AddedAt: 100,
		Revision:      3,
		FirstRevision: 1,
		Present:       false,
		ChangedAt:     300,
	}, 2, "https://books.test/kobo/secret")

	if item.NewEntitlement != nil || item.ChangedEntitlement == nil {
		t.Fatalf("item = %+v", item)
	}
	if !item.ChangedEntitlement.BookEntitlement.IsRemoved {
		t.Fatal("removal entitlement is not marked removed")
	}
	if item.ChangedEntitlement.BookMetadata != nil {
		t.Fatalf("removal unexpectedly includes metadata: %+v", item.ChangedEntitlement.BookMetadata)
	}
}

func TestBuildSyncItemClassifiesPresentEntitlementAgainstClientCursor(t *testing.T) {
	change := Change{
		AssetID:       "a_book",
		Revision:      4,
		FirstRevision: 2,
		Present:       true,
	}
	if item := BuildSyncItem(change, 0, "https://books.test"); item.NewEntitlement == nil {
		t.Fatalf("fresh client item = %+v; want NewEntitlement", item)
	}
	if item := BuildSyncItem(change, 2, "https://books.test"); item.ChangedEntitlement == nil {
		t.Fatalf("acknowledged client item = %+v; want ChangedEntitlement", item)
	}
}

func TestBuildMetadataBoundsLongUTF8Description(t *testing.T) {
	description := strings.Repeat("к", maxDescriptionBytes)
	metadata := BuildMetadata(Publication{AssetID: "a", Description: description}, "https://books.test")
	if len(metadata.Description) > maxDescriptionBytes || !strings.HasSuffix(metadata.Description, "…") {
		t.Fatalf("bounded description is %d bytes and ends %q", len(metadata.Description), metadata.Description[len(metadata.Description)-3:])
	}
}
