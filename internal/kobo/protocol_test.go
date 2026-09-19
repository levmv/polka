package kobo

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"testing"
)

func TestBuildSyncItemPinsNewEntitlementShape(t *testing.T) {
	seriesIndex := 2.5
	item := BuildSyncItems(Change{
		AssetID:       1,
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
		FirstRevision: 1,
		Present:       true,
		ChangedAt:     200,
	}, 0, "https://books.test/kobo/secret")[0]

	if item.NewEntitlement == nil || item.ChangedEntitlement != nil {
		t.Fatalf("item = %+v", item)
	}
	payload := item.NewEntitlement
	if payload.BookEntitlement.ID != "1" || payload.BookEntitlement.IsRemoved {
		t.Fatalf("entitlement = %+v", payload.BookEntitlement)
	}
	metadata := payload.BookMetadata
	if metadata == nil || metadata.Title != "A Book" || metadata.WorkID != "1" {
		t.Fatalf("metadata = %+v", metadata)
	}
	if len(metadata.DownloadURLs) != 1 || metadata.DownloadURLs[0].URL != "https://books.test/kobo/secret/download/1/kepub" {
		t.Fatalf("downloads = %+v", metadata.DownloadURLs)
	}
	if metadata.PublicationDate != "2024-03-02T00:00:00Z" || metadata.Series == nil || metadata.Series.Number != 2.5 {
		t.Fatalf("publication/series = %q %+v", metadata.PublicationDate, metadata.Series)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]struct {
		BookEntitlement map[string]jsontext.Value
		BookMetadata    map[string]jsontext.Value
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	publication := wire["NewEntitlement"]
	if len(wire) != 1 || string(publication.BookEntitlement["Id"]) != `"1"` ||
		string(publication.BookMetadata["WorkId"]) != `"1"` || publication.BookMetadata["DownloadUrls"] == nil || publication.BookMetadata["CoverImageId"] == nil {
		t.Fatalf("invalid new entitlement shape: %s", encoded)
	}
}

func TestBuildSyncItemMakesRemovalAChangedEntitlementWithoutMetadata(t *testing.T) {
	item := BuildSyncItems(Change{
		AssetID: 1, AddedAt: 100,
		FirstRevision: 1,
		Present:       false,
		ChangedAt:     300,
	}, 2, "https://books.test/kobo/secret")[0]

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
		AssetID:       1,
		AddedAt:       100,
		ModifiedAt:    200,
		ChangedAt:     300,
		FirstRevision: 2,
		Present:       true,
		ReadingState:  &ReadingState{EntitlementID: "1"},
	}
	initial := BuildSyncItems(change, 0, "https://books.test")
	if len(initial) != 1 || initial[0].NewEntitlement == nil || initial[0].NewEntitlement.ReadingState == nil || initial[0].NewEntitlement.ReadingState.EntitlementID != "1" {
		t.Fatalf("fresh client items = %+v; want NewEntitlement with reading state", initial)
	}
	updated := BuildSyncItems(change, 2, "https://books.test")
	if len(updated) != 2 || updated[0].ChangedProductMetadata == nil || updated[1].ChangedReadingState == nil {
		t.Fatalf("acknowledged client items = %+v; want metadata and reading events", updated)
	}
	if got := updated[0].ChangedProductMetadata.BookEntitlement.LastModified; got != "1970-01-01T00:03:20Z" {
		t.Fatalf("reading changed entitlement time to %q", got)
	}
	encoded, err := json.Marshal(updated)
	if err != nil {
		t.Fatal(err)
	}
	var wire []map[string]map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	metadata := wire[0]["ChangedProductMetadata"]
	if metadata["BookEntitlement"] == nil || metadata["BookMetadata"] == nil || metadata["ReadingState"] != nil {
		t.Fatalf("metadata event must wrap entitlement and metadata: %s", encoded)
	}
}

func TestBuildMetadataBoundsLongUTF8Description(t *testing.T) {
	description := strings.Repeat("к", maxDescriptionBytes)
	metadata := BuildMetadata(Publication{AssetID: 1, Description: description}, "https://books.test")
	if len(metadata.Description) > maxDescriptionBytes || !strings.HasSuffix(metadata.Description, "…") {
		t.Fatalf("bounded description is %d bytes and ends %q", len(metadata.Description), metadata.Description[len(metadata.Description)-3:])
	}
}
