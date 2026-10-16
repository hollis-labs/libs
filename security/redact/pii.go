package redact

import (
	"net/netip"
	"regexp"
	"strings"
	"unicode/utf8"
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

func piiRules() []rule {
	rules := []rule{
		{RuleEmail, findEmail},
		{RulePhone, findPhone},
		{RuleIPv4, findIPv4},
		{RuleIPv6, findIPv6},
		{RuleCard, findCard},
		{RuleNaniteID, findNaniteID},
		{RuleIBAN, findIBAN},
		{RuleSSN, findSSN},
		{RuleNINO, findNINO},
		{RulePIIName, findPIIName},
	}
	for i := range rules {
		rules[i].find = percentPII(rules[i].find)
	}
	return rules
}

func isPIIRule(name string) bool { return piiRuleClass(name) != 0 }

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
		for a > 0 {
			r, n := utf8.DecodeLastRuneInString(s.t[:a])
			if !emailLocalRune(r) {
				break
			}
			a -= n
		}
		for b < len(s.t) {
			r, n := utf8.DecodeRuneInString(s.t[b:])
			if !emailDomainRune(r) {
				break
			}
			b += n
		}
		end := b
		if end > at+1 && s.t[end-1] == '.' {
			end--
		}
		if validEmail(s.t[a:end]) {
			add(a, end)
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
			for b < len(t) && (asciiWord(t[b]) || t[b] == '-' || t[b] == '.' || t[b] == ':') {
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

func phoneBounded(t string, a, b int) bool {
	// Opening punctuation is itself a word boundary, even after a label.
	if a < b && (t[a] == '(' || t[a] == '+') {
		a++
	}
	return bounded(t, a, b)
}

func findPhone(_ *Redactor, s *scanText, add func(int, int)) {
	t := s.t
	for a := 0; a < len(t); {
		if !digit(t[a]) && t[a] != '+' && t[a] != '(' {
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
		if end > a && phoneBounded(t, a, end) {
			v := t[a:end]
			// Inspect boundary-aligned prefixes too. Otherwise removing one
			// adjacent number can expose a new 10/11-digit group on a second
			// pass (for example groups of six, five and ten digits).
			for start := a; start < end; start++ {
				if start != a && t[start-1] != ' ' && t[start] != '(' {
					continue
				}
				if !digit(t[start]) && t[start] != '(' && t[start] != '+' {
					continue
				}
				digits, dots := 0, 0
				minimum, maximum := 10, 11
				if t[start] == '+' {
					minimum, maximum = 7, 15
				}
				for stop := start; stop < end; stop++ {
					if stop > start && t[stop] == '(' {
						break
					}
					if digit(t[stop]) {
						digits++
					}
					if t[stop] == '.' {
						dots++
					}
					if digits > maximum {
						break
					}
					if (digits >= minimum) && digit(t[stop]) && dots != 3 && phoneBounded(t, start, stop+1) {
						add(start, stop+1)
					}
				}
			}
			for _, m := range nationalPhoneRE.FindAllStringIndex(v, -1) {
				if phoneBounded(t, a+m[0], a+m[1]) {
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
	// Consider boundary-aligned digit groups so a neighboring identifier does
	// not hide a card. Never accept a prefix of an overlong contiguous number.
	t := s.t
	for a := 0; a < len(t); a++ {
		if !digit(t[a]) || (a > 0 && asciiWord(t[a-1])) {
			continue
		}
		digits := 0
		for b := a; b < len(t) && (digit(t[b]) || t[b] == ' ' || t[b] == '-'); b++ {
			if !digit(t[b]) {
				continue
			}
			digits++
			if digits > 19 {
				break
			}
			if digits >= 13 && bounded(t, a, b+1) && luhn(t[a:b+1]) {
				add(a, b+1)
			}
		}
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
		nv := piiView(v.text)
		find(r, newScanText(nv.text), func(a, b int) { add(v.at(nv.at(a)), v.at(nv.at(b))) })
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

// scanPII is separate only to leave the credential-only scan's allocation and
// normalization path intact. Both consumers use the same catalog and finders.
func (r *Redactor) scanPII(s string, raw []Finding, observe func(string), skip string) []Finding {
	v := piiView(s)
	st := newScanText(v.text)
	name := ""
	add := func(a, b int) {
		if b > a {
			raw = append(raw, Finding{name, v.at(a), v.at(b)})
			if observe != nil {
				observe(name)
			}
		}
	}
	for _, rl := range r.piiRules {
		if rl.name == skip {
			continue
		}
		name = rl.name
		rl.find(r, st, add)
	}
	return raw
}
