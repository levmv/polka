package opds

const ProgressionType = "application/opds-progression+json"
const ProgressionRel = "http://opds-spec.org/progression"

// https://drafts.opds.io/opds-progression-1.0.html
// A pointer distinguishes a missing percentage from the beginning of a book.
type Progression struct {
	Title       string            `json:"title,omitempty"`
	Modified    string            `json:"modified"`
	Device      ProgressionDevice `json:"device"`
	Progression *float64          `json:"progression"`
	References  []string          `json:"references,omitempty"`
}

type ProgressionDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
