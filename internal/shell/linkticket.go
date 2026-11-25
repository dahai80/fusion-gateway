package shell

import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/base64"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "strings"
    "time"
)

// #717: local service tickets for the fusion-link daemon. The daemon drives
// shell task APIs on behalf of cloud-dispatched tasks with no browser
// session present. Tickets are a SEPARATE token space from shell sessions:
// different claims (Kind marker), different HMAC key (derived from the
// session secret, never interchangeable), short TTL (<=15m), loopback-only
// acceptance (enforced at the HTTP layer), and a route allowlist so a
// leaked ticket cannot open the whole /api/v1/shell plane.

// LinkTicketClaims ride inside the signed ticket payload.
type LinkTicketClaims struct {
    Kind     string `json:"kind"` // constant "link"
    Sub      string `json:"sub"`  // config-bound principal (audit identity)
    Tenant   string `json:"tenant"`
    TaskID   string `json:"task_id"`
    Modules  []string `json:"modules"`
    Exp      int64  `json:"exp"`
}

// linkTicketKind is the claim marker distinguishing a service ticket from a
// shell session payload (sessions carry no "kind" field).
const LinkTicketKind = "link"

// linkTicketKey derives the ticket HMAC key from the session secret: same
// secret store, non-overlapping key space (a session token can never verify
// as a ticket and vice versa — different key AND different payload shape).
func linkTicketKey(secret []byte) []byte {
    mac := hmac.New(sha256.New, secret)
    mac.Write([]byte("fusion-gateway/link-ticket/v1"))
    return mac.Sum(nil)
}

// MintLinkTicket signs a short-lived service ticket.
func MintLinkTicket(secret []byte, claims LinkTicketClaims) (string, error) {
    if claims.Kind != LinkTicketKind {
        return "", fmt.Errorf("link ticket claims must carry kind=link")
    }
    if claims.Exp == 0 {
        return "", fmt.Errorf("link ticket claims must carry exp")
    }
    payload, err := json.Marshal(claims)
    if err != nil {
        return "", fmt.Errorf("marshal link ticket claims: %w", err)
    }
    encoded := base64.RawURLEncoding.EncodeToString(payload)
    mac := hmac.New(sha256.New, linkTicketKey(secret))
    mac.Write([]byte(encoded))
    return encoded + "." + hexEncode(mac.Sum(nil)), nil
}

// ValidateLinkTicket parses + verifies a service ticket: signature, kind
// marker, and expiry. Returns the claims on success.
func ValidateLinkTicket(secret []byte, token string) (LinkTicketClaims, error) {
    var claims LinkTicketClaims
    encoded, sig, found := cutToken(token)
    if !found || encoded == "" || sig == "" {
        return claims, fmt.Errorf("malformed link ticket")
    }
    mac := hmac.New(sha256.New, linkTicketKey(secret))
    mac.Write([]byte(encoded))
    if !hmac.Equal([]byte(sig), []byte(hexEncode(mac.Sum(nil)))) {
        return claims, fmt.Errorf("invalid link ticket signature")
    }
    raw, err := base64.RawURLEncoding.DecodeString(encoded)
    if err != nil {
        return claims, fmt.Errorf("decode link ticket payload: %w", err)
    }
    if err := json.Unmarshal(raw, &claims); err != nil {
        return claims, fmt.Errorf("unmarshal link ticket claims: %w", err)
    }
    if claims.Kind != LinkTicketKind {
        return claims, fmt.Errorf("not a link service ticket")
    }
    if time.Now().Unix() >= claims.Exp {
        return claims, fmt.Errorf("link ticket expired")
    }
    return claims, nil
}

func hexEncode(sum []byte) string {
    return hex.EncodeToString(sum)
}

func cutToken(token string) (string, string, bool) {
    return strings.Cut(token, ".")
}
