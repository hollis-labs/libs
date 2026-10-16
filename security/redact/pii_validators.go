package redact

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	RuleIBAN    = "pii-iban"
	RuleSSN     = "pii-ssn"
	RuleNINO    = "pii-nino"
	RulePIIName = "pii-key-name"
)

var ssnRE = regexp.MustCompile(`[0-9]{3}-[0-9]{2}-[0-9]{4}`)

func unicodeBounded(t string, a, b int) bool {
	word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || r == '_' }
	if a > 0 {
		r, _ := utf8.DecodeLastRuneInString(t[:a])
		if word(r) {
			return false
		}
	}
	if b < len(t) {
		r, _ := utf8.DecodeRuneInString(t[b:])
		if word(r) {
			return false
		}
	}
	return true
}

func findSSN(_ *Redactor, s *scanText, add func(int, int)) {
	if !s.has("-") {
		return
	}
	for _, m := range ssnRE.FindAllStringIndex(s.t, -1) {
		if unicodeBounded(s.t, m[0], m[1]) && validSSN(s.t[m[0]:m[1]]) {
			add(m[0], m[1])
		}
	}
}

// SSA invalid-number rules, not issuance or identity verification. Require the
// hyphenated form: an unlabeled nine-digit order number is not enough evidence.
func validSSN(s string) bool {
	return s[:3] != "000" && s[:3] != "666" && s[0] != '9' && s[4:6] != "00" && s[7:] != "0000"
}

func findNINO(_ *Redactor, s *scanText, add func(int, int)) {
	t := s.lower
	letter := func(c byte) bool { return c >= 'a' && c <= 'z' }
	for a := 0; a+8 < len(t); a++ {
		if !letter(t[a]) || !letter(t[a+1]) {
			continue
		}
		b := a + 2
		for group := 0; group < 3; group++ {
			if b < len(t) && t[b] == ' ' {
				b++
			}
			if b+1 >= len(t) || !digit(t[b]) || !digit(t[b+1]) {
				break
			}
			b += 2
			if group != 2 {
				continue
			}
			if b < len(t) && t[b] == ' ' {
				b++
			}
			if b < len(t) && letter(t[b]) && unicodeBounded(t, a, b+1) && validNINO(t[a:b+1]) {
				add(a, b+1)
				a = b
			}
		}
	}
}

// HMRC NIM39110 prefix exclusions and suffix range; not allocation verification.
func validNINO(s string) bool {
	s = strings.ToUpper(strings.ReplaceAll(s, " ", ""))
	if strings.ContainsRune("DFIQUV", rune(s[0])) || strings.ContainsRune("DFIQUVO", rune(s[1])) {
		return false
	}
	switch s[:2] {
	case "BG", "GB", "KN", "NK", "NT", "TN", "ZZ":
		return false
	}
	return s[8] >= 'A' && s[8] <= 'D'
}

// validIBAN validates the registered national shape and ISO 7064 MOD-97-10.
// It never verifies account existence, bank routing or ownership.
func validIBAN(s string) bool {
	if len(s) < 4 {
		return false
	}
	shape, ok := ibanShapes[s[:2]]
	if !ok || len(s) != 4+len(shape) || !digit(s[2]) || !digit(s[3]) {
		return false
	}
	if s[2:4] < "02" || s[2:4] > "98" {
		return false
	}
	for i, kind := range shape {
		c := s[i+4]
		if kind == 'n' && !digit(c) || kind == 'a' && !(c >= 'A' && c <= 'Z') || kind == 'c' && !asciiAlphaNum(c) {
			return false
		}
	}
	remainder := 0
	for i := 0; i < len(s); i++ {
		c := s[(i+4)%len(s)]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		if digit(c) {
			remainder = (remainder*10 + int(c-'0')) % 97
		} else {
			remainder = (remainder*100 + int(c-'A') + 10) % 97
		}
	}
	return remainder == 1
}

func findIBAN(_ *Redactor, s *scanText, add func(int, int)) {
	t := s.t
	for a := 0; a+4 < len(t); a++ {
		if t[a] < 'A' || t[a] > 'Z' || t[a+1] < 'A' || t[a+1] > 'Z' || !digit(t[a+2]) || !digit(t[a+3]) {
			continue
		}
		shape, ok := ibanShapes[t[a:a+2]]
		if !ok {
			continue
		}
		n := 4 + len(shape)
		var token [34]byte
		b, k := a, 0
		// Accept electronic form or standard groups of four, separated by one space.
		spaced := a+4 < len(t) && t[a+4] == ' '
		for k < n && b < len(t) {
			if spaced && k > 0 && k%4 == 0 {
				if t[b] != ' ' {
					break
				}
				b++
				if b == len(t) {
					break
				}
			}
			if !asciiAlphaNum(t[b]) {
				break
			}
			token[k] = t[b]
			k++
			b++
		}
		if k == n && unicodeBounded(t, a, b) && validIBAN(string(token[:k])) {
			add(a, b)
			a = b - 1
		}
	}
}

// A deliberately bounded SMTPUTF8 subset: Unicode letters/numbers/marks plus
// ASCII dot-atom punctuation locally, and Unicode DNS-style labels in domains.
// Quoted local parts, address literals and mailbox existence are out of scope.
func validEmail(s string) bool {
	local, domain, ok := strings.Cut(s, "@")
	if !ok || len(local) == 0 || len(local) > 64 || len(s) > 254 || local[0] == '.' || local[len(local)-1] == '.' || strings.Contains(local, "..") {
		return false
	}
	for _, r := range local {
		if !emailLocalRune(r) {
			return false
		}
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		first, _ := utf8.DecodeRuneInString(label)
		if unicode.IsMark(first) {
			return false
		}
		for _, r := range label {
			if !(unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || r == '-') {
				return false
			}
		}
	}
	return true
}
func emailLocalRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || strings.ContainsRune(".!#$%&'*+/=?^_`{|}~-", r)
}
func emailDomainRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || r == '-' || r == '.'
}
