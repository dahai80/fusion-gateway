// Package catalog holds the built-in static product catalog (#488): the
// "what exists" dimension of the three-source /v1/catalog merge view.
// The catalog is in-repo JSON — adding a product is a one-PR operation —
// and is joined at request time with the live manifest registry (what is
// registered/running) and supervisor health (installed/stopped state).
package catalog

import (
    "encoding/json"
    "fmt"
    _ "embed"
)

//go:embed catalog.json
var raw []byte

// Entry is one product line item. ID matches the supervisor service
// registry key (and, where applicable, the shell manifest id) — that is
// the join key for the merge view.
type Entry struct {
    ID            string   `json:"id"`
    TitleEn       string   `json:"title_en"`
    TitleCn       string   `json:"title_cn"`
    Category      string   `json:"category"`
    Repo          string   `json:"repo"`
    DescriptionEn string   `json:"description_en"`
    DescriptionCn string   `json:"description_cn"`
    // Footprint is the approximate install size (human-readable).
    Footprint string `json:"footprint"`
    // ModelClass is the model requirement class (none / chat / embedding /
    // local-inference / speech / vision / any); SuggestedModel is the
    // advisory domain suggestion, aligned with the #486 manifest field.
    ModelClass     string   `json:"model_class"`
    SuggestedModel string   `json:"suggested_model,omitempty"`
    Dependencies   []string `json:"dependencies,omitempty"`
}

// All parses the embedded catalog. It never fails at runtime in practice
// (the file is in-repo and CI-validated); a parse error surfaces to the
// caller as a degraded catalog source, not a panic.
func All() ([]Entry, error) {
    var entries []Entry
    if err := json.Unmarshal(raw, &entries); err != nil {
        return nil, fmt.Errorf("catalog parse: %w", err)
    }
    return entries, nil
}
