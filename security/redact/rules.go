package redact

import (
	"regexp"
	"strings"
)

// Names of the built-in rules, for [WithoutRules] and [Finding].
const (
	RulePrivateKey   = "private-key"
	RuleProviderKey  = "provider-key"
	RuleWebhook      = "webhook-url"
	RuleAuthz        = "authorization"
	RuleBearer       = "bearer"
	RuleCookie       = "cookie"
	RuleUserinfo     = "url-userinfo"
	RuleDSN          = "dsn-password"
	RuleQuery        = "url-query"
	RuleAssignment   = "assignment"
	RuleFlag         = "cli-flag"
	RuleTools        = "cli-tools"
	RuleAWSSecretKey = "aws-secret-shape" // heuristic, off unless [WithHeuristics]
)

// scanText is the text a rule reads plus a lower-cased copy of identical byte layout
// (ASCII lowering only), so a rule can test for its trigger words cheaply before it
// runs a pattern.
type scanText struct {
	t     string
	lower string
}

func newScanText(t string) *scanText {
	b := []byte(t)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return &scanText{t: t, lower: string(b)}
}

func (s *scanText) has(lowerWord string) bool { return strings.Contains(s.lower, lowerWord) }

func (s *scanText) hasAny(lowerWords ...string) bool {
	for _, w := range lowerWords {
		if strings.Contains(s.lower, w) {
			return true
		}
	}
	return false
}

type finder func(r *Redactor, s *scanText, add func(a, b int))

type rule struct {
	name string
	find finder
}

func defaultRules() []rule {
	return []rule{
		{RulePrivateKey, regexRule(privateKeyRe, 0, "private key")},
		{RuleProviderKey, findProviderKey},
		{RuleWebhook, regexRule(webhookRe, 0, "hooks.slack.com", "/api/webhooks/")},
		{RuleAuthz, findAuthorization},
		{RuleBearer, findBearer},
		{RuleCookie, findCookie},
		{RuleUserinfo, findUserinfo},
		{RuleDSN, regexRule(dsnRe, 2, "@tcp(", "@unix(")},
		{RuleQuery, findQuery},
		{RuleAssignment, findAssignment},
		{RuleFlag, findFlag},
		{RuleTools, findTools},
	}
}

// regexRule redacts capture group g of every match. When prefilter words are given, the
// pattern only runs on text that contains at least one of them (lower-case).
func regexRule(re *regexp.Regexp, g int, prefilter ...string) finder {
	return func(_ *Redactor, s *scanText, add func(a, b int)) {
		if len(prefilter) > 0 && !s.hasAny(prefilter...) {
			return
		}
		t := s.t
		for _, m := range re.FindAllStringSubmatchIndex(t, -1) {
			if 2*g+1 < len(m) && m[2*g] >= 0 && m[2*g+1] > m[2*g] {
				add(m[2*g], m[2*g+1])
			}
		}
	}
}

var (
	privateKeyRe = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----|\z)`)

	// A provider key is only a key at a word boundary: "risk-assessment-..." contains
	// "sk-" and is not one. RE2 has no lookbehind, so the boundary is matched and
	// only group 1 (the token) is redacted.
	providerKeyRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9])(` + strings.Join([]string{
		`sk-(?:ant-|proj-|svcacct-|admin-)?[A-Za-z0-9_-]{20,}`,
		`(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{16,}`,
		`whsec_[A-Za-z0-9]{16,}`,
		`gh[pousr]_[A-Za-z0-9]{20,}`,
		`github_pat_[A-Za-z0-9_]{20,}`,
		`xox[abprs]-[A-Za-z0-9-]{10,}`,
		`xapp-[0-9]+-[A-Za-z0-9-]{10,}`,
		`(?:AKIA|ASIA|AGPA|AIDA|AROA|ANPA|ANVA)[A-Z0-9]{16}`,
		`glpat-[A-Za-z0-9_-]{20,}`,
		`AIza[A-Za-z0-9_-]{30,}`,
		`npm_[A-Za-z0-9]{30,}`,
		`SG\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}`,
		`ya29\.[A-Za-z0-9_-]{20,}`,
		`hf_[A-Za-z0-9]{30,}`,
		`dop_v1_[a-f0-9]{40,}`,
		`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`,
	}, "|") + `)`)

	// the URL is itself the credential
	webhookRe = regexp.MustCompile(`https://hooks\.slack\.com/services/[A-Za-z0-9/_-]+|https://(?:discord|discordapp)\.com/api/webhooks/[0-9]+/[A-Za-z0-9_-]+`)

	authKeyRe = regexp.MustCompile(`(?i)\b(?:proxy-)?authorization\b(?:\\?["'])?[ \t]*[:=][ \t]*`)
	schemeRe  = regexp.MustCompile(`(?i)^(bearer|basic|token|digest|negotiate|apikey|hawk|oauth|aws4-hmac-sha256)[ \t]+`)
	bearerRe  = regexp.MustCompile(`(?i)\bbearer[ \t]+([A-Za-z0-9._~+/=-]{4,})`)

	cookieKeyRe = regexp.MustCompile(`(?i)\b(set-cookie|cookie)\b(?:\\?["'])?[ \t]*[:=][ \t]*`)

	userinfoRe = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://([^/\s:@?#"'<>\[\]]*)(?::([^/\s@"'<>]+))?@`)
	dsnRe      = regexp.MustCompile(`\b([A-Za-z0-9._-]+):([^\s@:/]+)@(?:tcp|unix)\(`)
	queryRe    = regexp.MustCompile(`[?&;]([A-Za-z0-9_.%-]+)=([^&;\s#"'<>]*)`)

	sshpassRe   = regexp.MustCompile(`\bsshpass\s+(?:[^\n]*?\s)?-p\s*(\S+)`)
	mysqlRe     = regexp.MustCompile(`\bmysql(?:dump|admin|import|check)?\b[^\n]*?\s-p(\S+)`)
	configSetRe = regexp.MustCompile(`(?i)\b(?:configure|config)\b[^\n]*?\bset\s+([A-Za-z0-9_.-]+)\s+`)
	curlUserRe  = regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^\n]*?\s(?:-u|--user|--proxy-user)(?:[ \t]+|=)["']?([^\s:"']+):([^\s"']+)`)

	awsSecretRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9/+=])([A-Za-z0-9/+]{40})(?:$|[^A-Za-z0-9/+=])`)
)

// providerPrefixes are the literal starts of the provider key shapes; the pattern only
// runs on text that contains one.
var providerPrefixes = []string{"sk-", "sk_", "rk_", "whsec_", "ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "xox", "xapp-",
	"AKIA", "ASIA", "AGPA", "AIDA", "AROA", "ANPA", "ANVA", "glpat-", "AIza", "npm_", "SG.", "ya29.", "hf_", "dop_v1_", "eyJ"}

func findProviderKey(_ *Redactor, s *scanText, add func(a, b int)) {
	found := false
	for _, p := range providerPrefixes {
		if strings.Contains(s.t, p) {
			found = true
			break
		}
	}
	if !found {
		return
	}
	for _, m := range providerKeyRe.FindAllStringSubmatchIndex(s.t, -1) {
		add(m[2], m[3])
	}
}

// --- Authorization -----------------------------------------------------------

// findAuthorization redacts the credential of an Authorization header, whether
// written as a header, a JSON field or inside a quoted curl argument, and keeps
// the scheme word: "Authorization: Bearer [REDACTED]", never "[REDACTED] <token>".
func findAuthorization(r *Redactor, s *scanText, add func(a, b int)) {
	if !s.has("authorization") {
		return
	}
	t := s.t
	until := 0 // a key inside a value already read is part of that value
	for _, m := range authKeyRe.FindAllStringIndex(t, -1) {
		if m[0] < until {
			continue
		}
		a, b, ok := authCredential(t, m[0], m[1])
		if !ok {
			continue
		}
		until = b
		if cred := strings.TrimRight(t[a:b], " \t,;.)]"); cred != "" && !r.isPlaceholder(cred) {
			add(a, a+len(cred))
		}
	}
}

// authCredential finds the credential after "Authorization:" (key at keyStart, value
// at vs), without its scheme word. It reads only the words it needs: one for a bare
// credential, two for "Bearer x", and the rest of the header for Digest. Reading to the
// end of the line for every match would be quadratic on text with no line breaks.
func authCredential(t string, keyStart, vs int) (a, b int, ok bool) {
	if vs >= len(t) {
		return 0, 0, false
	}
	// the whole header may sit inside quotes: a JSON value, or curl -H "..."
	if t[vs] == '"' || t[vs] == '\'' || strings.HasPrefix(t[vs:], `\"`) {
		qa, qb, ok := parseValue(t, vs)
		if !ok {
			return 0, 0, false
		}
		return credInRegion(t, qa, qb)
	}
	var stop byte // the quote that opened the header, when the key itself sits inside quotes
	if keyStart > 0 && (t[keyStart-1] == '"' || t[keyStart-1] == '\'') {
		stop = t[keyStart-1]
	}
	word := func(i int) int {
		for i < len(t) && !strings.ContainsRune(" \t\n\r\"'\\", rune(t[i])) {
			i++
		}
		return i
	}
	w1 := word(vs)
	if !isSchemeWord(t[vs:w1]) {
		return vs, w1, w1 > vs
	}
	a = w1
	for a < len(t) && (t[a] == ' ' || t[a] == '\t') {
		a++
	}
	if strings.EqualFold(t[vs:w1], "digest") {
		b = a
		for b < len(t) && t[b] != '\n' && t[b] != '\r' && (stop == 0 || t[b] != stop) {
			b++
		}
		return a, b, b > a
	}
	b = word(a)
	return a, b, b > a
}

// credInRegion skips a leading scheme word inside a region that is already bounded
// (a quoted value). A scheme's credential is one word, except Digest, whose parameters
// take the rest of the region; with no scheme the credential is the first word.
func credInRegion(t string, a, b int) (int, int, bool) {
	w := a
	for w < b && t[w] != ' ' && t[w] != '\t' {
		w++
	}
	if isSchemeWord(t[a:w]) {
		digest := strings.EqualFold(t[a:w], "digest")
		a = w
		for a < b && (t[a] == ' ' || t[a] == '\t') {
			a++
		}
		w = a
		if digest {
			w = b
		} else {
			for w < b && t[w] != ' ' && t[w] != '\t' {
				w++
			}
		}
	}
	return a, w, w > a
}

func isSchemeWord(w string) bool {
	switch strings.ToLower(w) {
	case "bearer", "basic", "token", "digest", "negotiate", "apikey", "hawk", "oauth", "aws4-hmac-sha256":
		return true
	}
	return false
}

func findBearer(r *Redactor, s *scanText, add func(a, b int)) {
	if !s.has("bearer") {
		return
	}
	t := s.t
	for _, m := range bearerRe.FindAllStringSubmatchIndex(t, -1) {
		v := strings.TrimRight(t[m[2]:m[3]], ".")
		if looksLikeToken(v) && !r.isPlaceholder(v) {
			add(m[2], m[2]+len(v))
		}
	}
}

// --- Cookies -------------------------------------------------------------------

var cookieAttrs = set("path", "domain", "expires", "max-age", "samesite", "priority", "version", "comment", "secure", "httponly", "partitioned")

// findCookie redacts the value of every cookie in a Cookie or Set-Cookie header,
// keeping names and the attributes of Set-Cookie.
func findCookie(r *Redactor, s *scanText, add func(a, b int)) {
	if !s.has("cookie") {
		return
	}
	t := s.t
	until := 0
	for _, m := range cookieKeyRe.FindAllStringSubmatchIndex(t, -1) {
		if m[0] < until {
			continue // a "cookie" inside a cookie value is part of that value
		}
		isSet := strings.EqualFold(t[m[2]:m[3]], "set-cookie")
		vs := m[1]
		var a, b int
		if vs < len(t) && (t[vs] == '"' || t[vs] == '\'' || strings.HasPrefix(t[vs:], `\"`)) {
			var ok bool
			if a, b, ok = parseValue(t, vs); !ok {
				continue
			}
		} else {
			a, b = vs, vs
			for b < len(t) && t[b] != '\n' && t[b] != '\r' {
				b++
			}
		}
		until = b
		pos := a
		for _, part := range strings.Split(t[a:b], ";") {
			if eq := strings.IndexByte(part, '='); eq >= 0 {
				name := strings.ToLower(strings.TrimSpace(part[:eq]))
				if !(isSet && cookieAttrs[name]) {
					vstart, vend := pos+eq+1, pos+len(part)
					if v := strings.TrimSpace(t[vstart:vend]); v != "" && !r.isPlaceholder(v) {
						add(vstart, vend)
					}
				}
			}
			pos += len(part) + 1
		}
	}
}

// --- URLs ----------------------------------------------------------------------

func findUserinfo(r *Redactor, s *scanText, add func(a, b int)) {
	if !s.has("://") || !s.has("@") {
		return
	}
	t := s.t
	for _, m := range userinfoRe.FindAllStringSubmatchIndex(t, -1) {
		if m[4] >= 0 { // user:password@
			if pw := t[m[4]:m[5]]; !r.isPlaceholder(pw) {
				add(m[4], m[5])
			}
			continue
		}
		// a token used as the whole userinfo (https://TOKEN@host), but not a user name
		if u := t[m[2]:m[3]]; len(u) >= 16 && hasDigit(u) && !r.isPlaceholder(u) {
			add(m[2], m[3])
		}
	}
}

func hasDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			return true
		}
	}
	return false
}

func findQuery(r *Redactor, s *scanText, add func(a, b int)) {
	if !s.has("=") || !s.hasAny("?", "&", ";") {
		return
	}
	t := s.t
	for _, m := range queryRe.FindAllStringSubmatchIndex(t, -1) {
		name, val := t[m[2]:m[3]], t[m[4]:m[5]]
		if val == "" || r.isPlaceholder(val) {
			continue
		}
		if r.policy.sensitive(name) || querySensitive(name) {
			add(m[4], m[5])
		}
	}
}

// querySensitive covers parameters that are secrets in URLs but too generic to
// treat as secret everywhere: signatures.
func querySensitive(name string) bool {
	for _, w := range segments(name) {
		if w == "sig" || w == "signature" || w == "sas" {
			return true
		}
	}
	return false
}

// --- key=value -----------------------------------------------------------------

// keyFragments are cheap substrings a sensitive key name must contain; they skip the
// word-splitting for the great majority of keys.
var keyFragments = []string{"pass", "pwd", "secret", "token", "cred", "cookie", "auth", "key", "bearer", "sig"}

func maybeSensitive(lowerKey string, p *keyPolicy) bool {
	if p != nil && len(p.extraSensitive) > 0 {
		return true
	}
	for _, f := range keyFragments {
		if strings.Contains(lowerKey, f) {
			return true
		}
	}
	return false
}

func isKeyChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}

// findAssignment redacts the value of every "key<sep>value" whose key names a secret:
// env style (KEY=v), JSON ("key":"v", also \"key\":\"v\" inside a JSON string), YAML
// (key: v), logfmt, Go maps, percent-encoded (key%3Dv). It is a linear scan for
// separators, not a pattern: the work done per character is constant.
func findAssignment(r *Redactor, s *scanText, add func(a, b int)) {
	t := s.t
	for i := 0; i < len(t); i++ {
		var sepEnd int
		switch c := t[i]; {
		case c == '=':
			sepEnd = i + 1
			if i+1 < len(t) && (t[i+1] == '=' || t[i+1] == '>') {
				sepEnd++
			}
		case c == ':':
			if i+1 < len(t) && t[i+1] == ':' {
				i++ // "::" is a scope or an address, not a separator
				continue
			}
			sepEnd = i + 1
		case c == '%' && i+2 < len(t) && t[i+1] == '3' && strings.IndexByte("DdAa", t[i+2]) >= 0:
			sepEnd = i + 3
		default:
			continue
		}
		j := i
		for j > 0 && (t[j-1] == ' ' || t[j-1] == '\t') {
			j--
		}
		if j > 0 && (t[j-1] == '"' || t[j-1] == '\'') {
			j--
			if j > 0 && t[j-1] == '\\' {
				j--
			}
		}
		end := j
		for j > 0 && isKeyChar(t[j-1]) {
			j--
		}
		for j < end && (t[j] == '.' || t[j] == '-') {
			j++ // a key starts with a letter, digit or underscore
		}
		if end-j < 2 || end-j > 128 || !maybeSensitive(s.lower[j:end], r.policy) {
			continue
		}
		key := t[j:end]
		if !r.policy.sensitive(key) {
			continue
		}
		if j >= 3 && t[j-3:j] == "://" {
			continue // userinfo of a URL: the url-userinfo rule reads where it ends
		}
		segs := segments(key)
		if last := segs[len(segs)-1]; last == "authorization" || last == "cookie" {
			continue // the header rules read these, with their schemes and attributes
		}
		k := sepEnd
		for k < len(t) && (t[k] == ' ' || t[k] == '\t') {
			k++
		}
		a, b, ok := parseValue(t, k)
		if !ok {
			continue
		}
		v := t[a:b]
		if r.isPlaceholder(v) {
			continue
		}
		if t[i] == ':' && isTypeName(key) && messageLike(v) {
			continue // "AzureCLICredential: ERROR: ...": a type name and an error, not a key
		}
		add(a, b)
	}
}

// --- command lines -------------------------------------------------------------

func isFlagBoundary(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '"', '\'', '`', '(', '[', '{':
		return true
	}
	return false
}

func isFlagChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// findFlag redacts the value of a secret-named flag written with a space,
// "--token abc". The "--token=abc" form is an assignment. A flag that describes a
// secret without holding one (--password-stdin, --password-file, --max-tokens) is left.
func findFlag(r *Redactor, s *scanText, add func(a, b int)) {
	t := s.t
	for i := 0; i < len(t); i++ {
		if t[i] != '-' || (i > 0 && !isFlagBoundary(t[i-1])) {
			continue
		}
		j := i
		for j < len(t) && t[j] == '-' {
			j++
		}
		if j-i > 2 || j >= len(t) || !(t[j] >= 'a' && t[j] <= 'z' || t[j] >= 'A' && t[j] <= 'Z') {
			continue
		}
		k := j
		for k < len(t) && isFlagChar(t[k]) {
			k++
		}
		m := k
		for m < len(t) && (t[m] == ' ' || t[m] == '\t') {
			m++
		}
		i = k - 1
		if m == k || m >= len(t) || t[m] == '-' {
			continue // no value, or the next word is another flag
		}
		if !maybeSensitive(s.lower[j:k], r.policy) || !r.policy.sensitive(t[j:k]) {
			continue
		}
		if a, b, ok := parseValue(t, m); ok && !r.isPlaceholder(t[a:b]) {
			add(a, b)
		}
	}
}

func findTools(r *Redactor, s *scanText, add func(a, b int)) {
	if !s.hasAny("sshpass", "mysql", "curl", "wget", "config") {
		return
	}
	t := s.t
	for _, re := range []*regexp.Regexp{sshpassRe, mysqlRe} {
		for _, m := range re.FindAllStringSubmatchIndex(t, -1) {
			if !r.isPlaceholder(t[m[2]:m[3]]) {
				add(m[2], m[3])
			}
		}
	}
	// aws configure set aws_secret_access_key VALUE; npm config set _authToken VALUE
	for _, m := range configSetRe.FindAllStringSubmatchIndex(t, -1) {
		if !r.policy.sensitive(t[m[2]:m[3]]) {
			continue
		}
		if a, b, ok := parseValue(t, m[1]); ok && !r.isPlaceholder(t[a:b]) {
			add(a, b)
		}
	}
	for _, m := range curlUserRe.FindAllStringSubmatchIndex(t, -1) {
		if !r.isPlaceholder(t[m[4]:m[5]]) {
			add(m[4], m[5])
		}
	}
}

// --- heuristics ----------------------------------------------------------------

// findAWSSecret finds an unlabeled AWS secret access key: 40 base64 characters
// with upper case, lower case and a digit, not hexadecimal. It is a heuristic
// (a 40-character mixed-case identifier would match), so it is opt-in.
func findAWSSecret(_ *Redactor, s *scanText, add func(a, b int)) {
	t := s.t
	for _, m := range awsSecretRe.FindAllStringSubmatchIndex(t, -1) {
		s := t[m[2]:m[3]]
		var up, lo, dg, other bool
		for i := 0; i < len(s); i++ {
			switch c := s[i]; {
			case c >= 'A' && c <= 'Z':
				up = true
			case c >= 'a' && c <= 'z':
				lo = true
			case c >= '0' && c <= '9':
				dg = true
			}
			if c := s[i]; c >= 'g' && c <= 'z' || c >= 'G' && c <= 'Z' {
				other = true // a non-hex letter
			}
		}
		if up && lo && dg && other {
			add(m[2], m[3])
		}
	}
}
