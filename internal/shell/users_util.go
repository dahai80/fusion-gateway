package shell

import (
    "bytes"
    "encoding/json"
    "sort"
)

// jsonUnmarshalStrict decodes JSON with unknown-field rejection so a corrupt
// or hand-edited store file fails loudly instead of silently dropping rows.
func jsonUnmarshalStrict(raw []byte, v any) error {
    dec := json.NewDecoder(bytes.NewReader(raw))
    dec.DisallowUnknownFields()
    return dec.Decode(v)
}

func jsonMarshalIndent(v any) ([]byte, error) {
    var buf bytes.Buffer
    enc := json.NewEncoder(&buf)
    enc.SetIndent("", "    ")
    if err := enc.Encode(v); err != nil {
        return nil, err
    }
    return buf.Bytes(), nil
}

// hasRole reports whether the role slice contains the role.
func hasRole(roles []string, role string) bool {
    for _, r := range roles {
        if r == role {
            return true
        }
    }
    return false
}

// sortAccounts orders accounts by username for a stable admin listing.
func sortAccounts(accounts []UserAccount) {
    sort.Slice(accounts, func(i, j int) bool {
        return accounts[i].Username < accounts[j].Username
    })
}
