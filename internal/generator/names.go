package main

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// initialisms is golint's list plus the product terms the API uses. Changing it renames exported
// identifiers, which breaks users.
var initialisms = map[string]bool{
	"ACL": true, "API": true, "ASCII": true, "CPU": true, "CSS": true, "DNS": true, "EOF": true,
	"GUID": true, "HTML": true, "HTTP": true, "HTTPS": true, "ID": true, "IP": true, "JSON": true,
	"LHS": true, "QPS": true, "RAM": true, "RHS": true, "RPC": true, "SLA": true, "SMTP": true,
	"SQL": true, "SSH": true, "TCP": true, "TLS": true, "TTL": true, "UDP": true, "UI": true,
	"UID": true, "UUID": true, "URI": true, "URL": true, "UTF8": true, "VM": true, "XML": true,
	"XMPP": true, "XSRF": true, "XSS": true,
	"AI": true, "CRM": true, "SMS": true, "SDR": true, "UTM": true, "PDF": true, "CSV": true,
	"OTP": true, "FAQ": true, "CTA": true, "KPI": true, "NPS": true, "LTV": true, "VSL": true,
	"CPF": true, "CNPJ": true, "SEO": true, "KYC": true, "QR": true, "IMAP": true, "RSVP": true,
	"MIME": true, "AB": true, "WABA": true, "USP": true,
	// File formats.
	"GIF": true, "PNG": true, "JPG": true, "JPEG": true, "MP4": true,
}

// currencyEnum reports whether an enum holds currency codes, judged by its name (a Currency field,
// or an element of a Currency list) so that adding a value never renames the other constants.
func currencyEnum(typeName string) bool {
	return strings.HasSuffix(strings.TrimSuffix(typeName, "Item"), "Currency")
}

// currencyCode keeps the upper case of currency codes (BRL, not Brl). A one-word upper-case value
// stays as written (USDC). In longer values, 3-letter words (the ISO 4217 length) and initialisms
// stay upper case and other words use Pascal case (WHOP_USD -> WhopUSD).
func currencyCode(v string) string {
	ws := words(v)
	if len(ws) == 1 && strings.ToUpper(v) == v {
		return v
	}
	var b strings.Builder
	for _, w := range ws {
		if strings.ToUpper(w) == w && (len(w) == 3 || initialisms[w]) {
			b.WriteString(w)
		} else {
			b.WriteString(pascal(w))
		}
	}
	return b.String()
}

// constantNames names the enum constants whose default name (type + value) is already a type,
// keyed by enum type and value. A new collision needs a person to pick the name: the generator
// never renames on its own because renaming an exported constant breaks users. Entries apply
// even after the type they collided with is gone, so a published name never changes.
var constantNames = map[[2]string]string{
	{"FlowsListInputOrderBy", "mode"}: "FlowsListInputOrderByModeValue",
}

// pascal turns a property name, path segment, operationId or enum value into an exported Go
// identifier part: `ownerId` -> `OwnerID`, `ai-agents` -> `AIAgents`, `QUICK_REPLY` -> `QuickReply`.
// Plurals of initialisms keep the initialism (`leadIds` -> `LeadIDs`).
func pascal(s string) string {
	var b strings.Builder
	for _, w := range words(s) {
		upper := strings.ToUpper(w)
		switch {
		case initialisms[upper]:
			b.WriteString(upper)
		case len(w) > 2 && strings.HasSuffix(w, "s") && initialisms[upper[:len(upper)-1]]:
			b.WriteString(upper[:len(upper)-1] + "s")
		default:
			r, size := utf8.DecodeRuneInString(w)
			b.WriteRune(unicode.ToUpper(r))
			b.WriteString(strings.ToLower(w[size:]))
		}
	}
	return b.String()
}

// words splits on every non-alphanumeric rune and on camelCase boundaries (`HTMLParser` ->
// `HTML`, `Parser`). Digits stay attached to the word before them.
func words(s string) []string {
	var out []string
	runes := []rune(s)
	start := -1
	flush := func(end int) {
		if start >= 0 && end > start {
			out = append(out, string(runes[start:end]))
		}
		start = -1
	}
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush(i)
			continue
		}
		if start < 0 {
			start = i
			continue
		}
		if unicode.IsUpper(r) {
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				flush(i)
				start = i
			}
		}
	}
	flush(len(runes))
	return out
}

// exported reports whether s is an exported Go identifier.
func exported(s string) bool {
	for i, r := range s {
		if i == 0 && !unicode.IsUpper(r) {
			return false
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return s != ""
}
