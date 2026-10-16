package redact

import (
	"net/netip"
	"regexp"
	"strings"
)

// PII rules are mandatory in Sanitizer and do not change Default's credential-only policy.
const (
	RuleEmail    = "pii-email"
	RulePhone    = "pii-phone"
	RuleIPv4     = "pii-ipv4"
	RuleIPv6     = "pii-ipv6"
	RuleCard     = "pii-card"
	RuleNaniteID = "pii-nanite-id"
)

var (
	emailRE = regexp.MustCompile("[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)+")
)

func piiRules() []rule {
	rules := []rule{
		{RuleEmail, findEmail},
		{RulePhone, findPhone},
		{RuleIPv4, findIPv4},
		{RuleIPv6, findIPv6},
		{RuleCard, findCard},
		{RuleNaniteID, findNaniteID},
	}
	for i := range rules {
		rules[i].find = percentPII(rules[i].find)
	}
	return rules
}

func isPIIRule(name string) bool {
	switch name {
	case RuleEmail, RulePhone, RuleIPv4, RuleIPv6, RuleCard, RuleNaniteID:
		return true
	}
	return false
}

func asciiWord(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}
func bounded(t string, a, b int) bool {
	return (a == 0 || !asciiWord(t[a-1])) && (b == len(t) || !asciiWord(t[b]))
}
func digit(c byte) bool { return c >= '0' && c <= '9' }
func hex(c byte) bool   { return digit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

func findEmail(_ *Redactor, s *scanText, add func(int, int)) {
	for offset := 0; offset < len(s.t); {
		at := strings.IndexByte(s.t[offset:], '@')
		if at < 0 {
			break
		}
		at += offset
		offset = at + 1
		a, b := at, at+1
		for a > 0 && (asciiAlphaNum(s.t[a-1]) || strings.ContainsRune(".!#$%&'*+/=?^_`{|}~-", rune(s.t[a-1]))) {
			a--
		}
		for b < len(s.t) && (asciiAlphaNum(s.t[b]) || s.t[b] == '-' || s.t[b] == '.') {
			b++
		}
		if m := emailRE.FindStringIndex(s.t[a:b]); m != nil {
			add(a+m[0], a+m[1])
		}
	}
}

func findIPv4(_ *Redactor, s *scanText, add func(int, int)) {
	t := s.t
	for a := 0; a < len(t); {
		if !digit(t[a]) {
			a++
			continue
		}
		b := a
		for b < len(t) && (digit(t[b]) || t[b] == '.') {
			b++
		}
		end := b
		if end > a && t[end-1] == '.' {
			end--
		}
		if bounded(t, a, end) && strings.Count(t[a:end], ".") == 3 {
			if ip, err := netip.ParseAddr(t[a:end]); err == nil && ip.Is4() {
				add(a, end)
			}
		}
		a = b
	}
}

func findIPv6(_ *Redactor, s *scanText, add func(int, int)) {
	if !s.has(":") {
		return
	}
	// Lex maximal address-shaped tokens, then use the standard library parser.
	// Zones are part of the address and must be removed with it.
	t := s.t
	for a := 0; a < len(t); {
		if !hex(t[a]) && t[a] != ':' {
			a++
			continue
		}
		b := a
		for b < len(t) && (hex(t[b]) || t[b] == ':' || t[b] == '.') {
			b++
		}
		if b < len(t) && t[b] == '%' {
			b++
			for b < len(t) && (asciiWord(t[b]) || t[b] == '-' || t[b] == '.') {
				b++
			}
		}
		end := b
		for end > a && t[end-1] == '.' {
			end--
		}
		if strings.Count(t[a:end], ":") >= 2 && bounded(t, a, end) {
			if ip, err := netip.ParseAddr(t[a:end]); err == nil && ip.Is6() {
				add(a, end)
			}
		}
		a = b
	}
}

var nationalPhoneRE = regexp.MustCompile(`(?:\+?1[ .-]?)?(?:\([0-9]{3}\)|[0-9]{3})[ .-]?[0-9]{3}[ .-]?[0-9]{4}`)

func findPhone(_ *Redactor, s *scanText, add func(int, int)) {
	t := s.t
	for a := 0; a < len(t); {
		if !digit(t[a]) && t[a] != '+' {
			a++
			continue
		}
		b := a + 1
		for b < len(t) && (digit(t[b]) || strings.ContainsRune("(). -", rune(t[b]))) {
			b++
		}
		end := b
		for end > a && !digit(t[end-1]) {
			end--
		}
		if end > a && bounded(t, a, end) {
			v := t[a:end]
			for _, m := range nationalPhoneRE.FindAllStringIndex(v, -1) {
				if bounded(t, a+m[0], a+m[1]) {
					add(a+m[0], a+m[1])
				}
			}
			n := 0
			for i := range v {
				if digit(v[i]) {
					n++
				}
			}
			if n >= 7 && n <= 15 && v[0] == '+' || n >= 10 && n <= 11 && v[0] != '+' {
				if strings.Count(v, ".") != 3 && !(strings.Count(v, "-") == 2 && len(v) == 10) {
					add(a, end)
				}
			}
		}
		a = b
	}
}

func findCard(_ *Redactor, s *scanText, add func(int, int)) {
	// Maximal runs prevent accepting a valid prefix of an overlong number.
	t := s.t
	for a := 0; a < len(t); {
		if !digit(t[a]) {
			a++
			continue
		}
		b := a
		for b < len(t) && (digit(t[b]) || t[b] == ' ' || t[b] == '-') {
			b++
		}
		end := b
		for end > a && !digit(t[end-1]) {
			end--
		}
		if bounded(t, a, end) && luhn(t[a:end]) {
			add(a, end)
		}
		a = b
	}
}

func luhn(s string) bool {
	sum, n := 0, 0
	for i := len(s) - 1; i >= 0; i-- {
		if !digit(s[i]) {
			continue
		}
		d := int(s[i] - '0')
		if n%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		n++
	}
	return n >= 13 && n <= 19 && sum > 0 && sum%10 == 0
}

func findNaniteID(_ *Redactor, s *scanText, add func(int, int)) {
	for a := 0; a < len(s.t); {
		if !hex(s.t[a]) {
			a++
			continue
		}
		b := a + 1
		for b < len(s.t) && hex(s.t[b]) {
			b++
		}
		if b-a == 32 && bounded(s.t, a, b) {
			add(a, b)
		}
		a = b
	}
}

// PII rules also inspect one percent-decoded view; offsets always map back to
// the original encoded bytes. The raw pass retains IPv6 zone and literal '%' semantics.
func percentPII(find finder) finder {
	return func(r *Redactor, s *scanText, add func(int, int)) {
		find(r, s, add)
		if !strings.Contains(s.t, "%") {
			return
		}
		v := percentView(s.t)
		if v.orig == nil {
			return
		}
		find(r, newScanText(v.text), func(a, b int) { add(v.at(a), v.at(b)) })
	}
}
func percentView(s string) view {
	out := make([]byte, 0, len(s))
	offsets := make([]int, 0, len(s)+1)
	changed := false
	for i := 0; i < len(s); {
		if s[i] == '%' && i+2 < len(s) && hex(s[i+1]) && hex(s[i+2]) {
			out = append(out, hexValue(s[i+1])<<4|hexValue(s[i+2]))
			offsets = append(offsets, i)
			i += 3
			changed = true
		} else {
			out = append(out, s[i])
			offsets = append(offsets, i)
			i++
		}
	}
	if !changed {
		return view{text: s}
	}
	offsets = append(offsets, len(s))
	return view{text: string(out), orig: offsets}
}
func hexValue(c byte) byte {
	if c >= '0' && c <= '9' {
		return c - '0'
	}
	if c >= 'A' && c <= 'F' {
		return c - 'A' + 10
	}
	return c - 'a' + 10
}
