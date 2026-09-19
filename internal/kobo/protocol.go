// Package kobo contains Polka's small, independent Kobo wire adapter. It has no
// database or HTTP dependencies: callers map domain rows in at one boundary and
// receive DTOs that can be encoded directly as JSON.
package kobo

import (
	"crypto/sha1"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"
)

const (
	importedCategoryID  = "00000000-0000-0000-0000-000000000001"
	maxDescriptionBytes = 64 << 10
)

var seriesUUIDNamespace = uuid.MustParse("e528a6d9-824d-4d47-a7e4-cbf4c58b2159")

type Publication struct {
	AssetID       int64
	Size          int64
	Title         string
	Description   string
	Publisher     string
	PublishedDate string
	Language      string
	Series        string
	SeriesIndex   *float64
	Authors       []string
	AddedAt       int64
	ModifiedAt    int64
	CoverVersion  int
}

type Change struct {
	Publication
	FirstRevision int64
	Present       bool
	ChangedAt     int64
	ReadingState  *ReadingState
}

type ActivePeriod struct {
	From string `json:"From"`
}

type Entitlement struct {
	Accessibility       string       `json:"Accessibility"`
	ActivePeriod        ActivePeriod `json:"ActivePeriod"`
	Created             string       `json:"Created"`
	CrossRevisionID     string       `json:"CrossRevisionId"`
	ID                  string       `json:"Id"`
	IsRemoved           bool         `json:"IsRemoved"`
	IsHiddenFromArchive bool         `json:"IsHiddenFromArchive"`
	IsLocked            bool         `json:"IsLocked"`
	LastModified        string       `json:"LastModified"`
	OriginCategory      string       `json:"OriginCategory"`
	RevisionID          string       `json:"RevisionId"`
	Status              string       `json:"Status"`
}

type Money struct {
	CurrencyCode string  `json:"CurrencyCode,omitempty"`
	TotalAmount  float64 `json:"TotalAmount"`
}

type DownloadURL struct {
	Format   string `json:"Format"`
	Size     int64  `json:"Size"`
	URL      string `json:"Url"`
	Platform string `json:"Platform"`
}

type Publisher struct {
	Imprint string `json:"Imprint"`
	Name    string `json:"Name"`
}

type ContributorRole struct {
	Name string `json:"Name"`
}

type Series struct {
	ID          string  `json:"Id"`
	Name        string  `json:"Name"`
	Number      float64 `json:"Number"`
	NumberFloat float64 `json:"NumberFloat"`
}

type Metadata struct {
	Categories              []string          `json:"Categories"`
	CoverImageID            string            `json:"CoverImageId"`
	CrossRevisionID         string            `json:"CrossRevisionId"`
	CurrentDisplayPrice     Money             `json:"CurrentDisplayPrice"`
	CurrentLoveDisplayPrice Money             `json:"CurrentLoveDisplayPrice"`
	Description             string            `json:"Description"`
	DownloadURLs            []DownloadURL     `json:"DownloadUrls"`
	EntitlementID           string            `json:"EntitlementId"`
	ExternalIDs             []string          `json:"ExternalIds"`
	Genre                   string            `json:"Genre"`
	IsEligibleForKoboLove   bool              `json:"IsEligibleForKoboLove"`
	IsInternetArchive       bool              `json:"IsInternetArchive"`
	IsPreOrder              bool              `json:"IsPreOrder"`
	IsSocialEnabled         bool              `json:"IsSocialEnabled"`
	Language                string            `json:"Language"`
	PhoneticPronunciations  map[string]string `json:"PhoneticPronunciations"`
	PublicationDate         string            `json:"PublicationDate"`
	Publisher               Publisher         `json:"Publisher"`
	RevisionID              string            `json:"RevisionId"`
	Title                   string            `json:"Title"`
	WorkID                  string            `json:"WorkId"` // Kobo's field name; identifies the asset.
	Contributors            []string          `json:"Contributors"`
	ContributorRoles        []ContributorRole `json:"ContributorRoles"`
	Series                  *Series           `json:"Series,omitzero"`
}

type EntitlementPayload struct {
	BookEntitlement Entitlement   `json:"BookEntitlement"`
	BookMetadata    *Metadata     `json:"BookMetadata,omitzero"`
	ReadingState    *ReadingState `json:"ReadingState,omitzero"`
}

type Location struct {
	Source string `json:"Source"`
	Type   string `json:"Type"`
	Value  string `json:"Value"`
}

type Bookmark struct {
	LastModified                 time.Time `json:"LastModified"`
	ProgressPercent              *float64  `json:"ProgressPercent,omitzero"`
	ContentSourceProgressPercent *float64  `json:"ContentSourceProgressPercent,omitzero"`
	Location                     *Location `json:"Location,omitzero"`
}

type StatusInfo struct {
	LastModified time.Time `json:"LastModified"`
	Status       string    `json:"Status"`
}

type ReadingState struct {
	EntitlementID     string      `json:"EntitlementId"`
	Created           time.Time   `json:"Created,omitzero"`
	LastModified      time.Time   `json:"LastModified"`
	PriorityTimestamp time.Time   `json:"PriorityTimestamp,omitzero"`
	CurrentBookmark   *Bookmark   `json:"CurrentBookmark,omitzero"`
	StatusInfo        *StatusInfo `json:"StatusInfo,omitzero"`
}

type ReadingStatePayload struct {
	ReadingState *ReadingState `json:"ReadingState"`
}

type ReadingStateUpdate struct {
	ReadingStates []ReadingState `json:"ReadingStates"`
}

type Result struct {
	Result string `json:"Result"`
}

type ReadingUpdateResult struct {
	EntitlementID         string `json:"EntitlementId"`
	CurrentBookmarkResult Result `json:"CurrentBookmarkResult"`
	StatusInfoResult      Result `json:"StatusInfoResult"`
	StatisticsResult      Result `json:"StatisticsResult"`
}

type ReadingUpdateResponse struct {
	RequestResult string                `json:"RequestResult"`
	UpdateResults []ReadingUpdateResult `json:"UpdateResults"`
}

type SyncItem struct {
	NewEntitlement         *EntitlementPayload  `json:"NewEntitlement,omitzero"`
	ChangedEntitlement     *EntitlementPayload  `json:"ChangedEntitlement,omitzero"`
	ChangedProductMetadata *EntitlementPayload  `json:"ChangedProductMetadata,omitzero"`
	ChangedReadingState    *ReadingStatePayload `json:"ChangedReadingState,omitzero"`
}

func BuildSyncItems(change Change, afterRevision int64, baseURL string) []SyncItem {
	// New/changed is a property of what this client has acknowledged, not of
	// the compacted row's latest revision. A fresh or reset device must receive
	// NewEntitlement even when metadata changed before its first sync.
	payload := &EntitlementPayload{BookEntitlement: BuildEntitlement(change)}
	if !change.Present {
		return []SyncItem{{ChangedEntitlement: payload}}
	}
	metadata := BuildMetadata(change.Publication, baseURL)
	payload.BookMetadata = &metadata
	if afterRevision < change.FirstRevision {
		payload.ReadingState = change.ReadingState
		return []SyncItem{{NewEntitlement: payload}}
	}
	// ChangedEntitlement can make Kobo discard a downloaded copy. Updating
	// metadata or reading progress must not announce a replacement book.
	items := []SyncItem{{ChangedProductMetadata: payload}}
	// Kobo consumes reading changes as separate events, including when the
	// same publication also has a metadata change in this page.
	if change.ReadingState != nil {
		items = append(items, SyncItem{ChangedReadingState: &ReadingStatePayload{change.ReadingState}})
	}
	return items
}

func BuildEntitlement(change Change) Entitlement {
	modifiedAt := change.ModifiedAt
	if !change.Present {
		modifiedAt = change.ChangedAt
	}
	assetID := strconv.FormatInt(change.AssetID, 10)
	return Entitlement{
		Accessibility:       "Full",
		ActivePeriod:        ActivePeriod{From: timestamp(change.AddedAt)},
		Created:             timestamp(change.AddedAt),
		CrossRevisionID:     assetID,
		ID:                  assetID,
		IsRemoved:           !change.Present,
		IsHiddenFromArchive: false,
		IsLocked:            false,
		LastModified:        timestamp(modifiedAt),
		OriginCategory:      "Imported",
		RevisionID:          assetID,
		Status:              "Active",
	}
}

func BuildMetadata(publication Publication, baseURL string) Metadata {
	assetID := strconv.FormatInt(publication.AssetID, 10)
	coverID := assetID + "-" + strconv.Itoa(publication.CoverVersion)
	if publication.CoverVersion == 0 {
		// Fallback covers change with the book's title and author.
		sum := sha1.Sum([]byte(publication.Title + "\x00" + strings.Join(publication.Authors, "\x00")))
		coverID += "-" + hex.EncodeToString(sum[:])
	}
	language := strings.TrimSpace(publication.Language)
	if language == "" {
		language = "en"
	}
	contributors := slices.Clone(publication.Authors)
	roles := make([]ContributorRole, 0, len(contributors))
	for _, author := range contributors {
		roles = append(roles, ContributorRole{Name: author})
	}
	metadata := Metadata{
		Categories:              []string{importedCategoryID},
		CoverImageID:            coverID,
		CrossRevisionID:         assetID,
		CurrentDisplayPrice:     Money{CurrencyCode: "USD", TotalAmount: 0},
		CurrentLoveDisplayPrice: Money{TotalAmount: 0},
		Description:             boundedDescription(publication.Description),
		DownloadURLs: []DownloadURL{{
			Format:   "KEPUB",
			Size:     publication.Size,
			URL:      strings.TrimRight(baseURL, "/") + "/download/" + assetID + "/kepub",
			Platform: "Generic",
		}},
		EntitlementID:          assetID,
		ExternalIDs:            []string{},
		Genre:                  importedCategoryID,
		IsEligibleForKoboLove:  false,
		IsInternetArchive:      false,
		IsPreOrder:             false,
		IsSocialEnabled:        true,
		Language:               language,
		PhoneticPronunciations: map[string]string{},
		PublicationDate:        publicationTimestamp(publication.PublishedDate, publication.AddedAt),
		Publisher:              Publisher{Imprint: "", Name: publication.Publisher},
		RevisionID:             assetID,
		Title:                  publication.Title,
		WorkID:                 assetID,
		Contributors:           contributors,
		ContributorRoles:       roles,
	}
	if publication.Series != "" {
		number := 1.0
		if publication.SeriesIndex != nil {
			number = *publication.SeriesIndex
		}
		metadata.Series = &Series{
			ID:          stableSeriesID(publication.Series),
			Name:        publication.Series,
			Number:      number,
			NumberFloat: number,
		}
	}
	return metadata
}

func boundedDescription(value string) string {
	if len(value) <= maxDescriptionBytes {
		return value
	}
	end := maxDescriptionBytes - len("…")
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + "…"
}

func timestamp(unix int64) string {
	if unix <= 0 {
		return "1970-01-01T00:00:00Z"
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func publicationTimestamp(value string, fallback int64) string {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339, "2006-01-02", "2006-01", "2006"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC().Format(time.RFC3339)
		}
	}
	return timestamp(fallback)
}

// stableSeriesID is a Polka-namespaced UUIDv5-shaped value.
// Kobo treats it as an opaque stable identifier; the namespace is Polka-local.
func stableSeriesID(name string) string {
	h := sha1.New()
	h.Write(seriesUUIDNamespace[:])
	h.Write([]byte(name))
	var id uuid.UUID
	copy(id[:], h.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}
