package redact

import "fmt"

// PIIClass selects a family of deterministic value or key-name rules.
type PIIClass uint16

const (
	PIIEmail PIIClass = 1 << iota
	PIIPhone
	PIIIP
	PIICard
	PIIIBAN
	PIINationalID
	PIINaniteID
	PIIDateOfBirth   // key names only; bare dates are never inferred as birth dates
	PIIPersonalName  // key names only; no recognition of names in prose
	PIIPostalAddress // key names only; no recognition of addresses in prose
	// PIIAll selects all supported value and key-name classes.
	PIIAll PIIClass = PIIEmail | PIIPhone | PIIIP | PIICard | PIIIBAN | PIINationalID | PIINaniteID | PIIDateOfBirth | PIIPersonalName | PIIPostalAddress
)

// PIIOption configures [WithPII].
type PIIOption func(*PIIClass) error

// PIIClasses selects exactly these classes. No arguments selects none.
// Class constants may be combined with |. Unknown bits are rejected by New.
func PIIClasses(classes ...PIIClass) PIIOption {
	var selected PIIClass
	for _, class := range classes {
		selected |= class
	}
	return func(c *PIIClass) error {
		if selected & ^PIIAll != 0 {
			return fmt.Errorf("redact: unknown PII class bits: %x", selected & ^PIIAll)
		}
		*c = selected
		return nil
	}
}

// WithPII enables deterministic PII rules everywhere the Redactor is used.
// With no options all classes are enabled; PIIClasses selects a subset. Each
// WithPII replaces the previous selection. Default remains credential-only.
// NewSanitizer always enables all classes, regardless of these options.
func WithPII(opts ...PIIOption) Option {
	return func(c *config) error {
		selected := PIIClass(PIIAll)
		for _, opt := range opts {
			if err := opt(&selected); err != nil {
				return err
			}
		}
		if selected & ^PIIAll != 0 {
			return fmt.Errorf("redact: unknown PII class bits: %x", selected & ^PIIAll)
		}
		c.pii = selected
		return nil
	}
}

func piiRuleClass(name string) PIIClass {
	switch name {
	case RuleEmail:
		return PIIEmail
	case RulePhone:
		return PIIPhone
	case RuleIPv4, RuleIPv6:
		return PIIIP
	case RuleCard:
		return PIICard
	case RuleIBAN:
		return PIIIBAN
	case RuleSSN, RuleNINO:
		return PIINationalID
	case RuleNaniteID:
		return PIINaniteID
	case RulePIIName:
		return PIIAll
	}
	return 0
}

// PIIName reports whether a key names personal data. It uses the same Unicode
// normalization, word boundaries and benign metadata suffixes as SensitiveName.
// It does not change SensitiveName's credential-only contract.
func PIIName(name string) bool { return piiNameClass(name) != 0 }

func piiNameClass(name string) PIIClass {
	words := segments(name)
	if len(words) == 0 {
		return 0
	}
	// Match the longest suffix first so first_name is sensitive, while
	// first_name_count and email_name are descriptive metadata.
	for i := max(0, len(words)-3); i < len(words); i++ {
		if class := piiKeyClass(joinWords(words[i:])); class != 0 {
			return class
		}
	}
	if benignLast[words[len(words)-1]] {
		return 0
	}
	for i := range words {
		for n := 1; n <= 3 && i+n <= len(words); n++ {
			if class := piiKeyClass(joinWords(words[i : i+n])); class != 0 {
				return class
			}
		}
	}
	return 0
}

func piiKeyClass(joined string) PIIClass {
	switch joined {
	case "email", "emailaddress":
		return PIIEmail
	case "phone", "phonenumber", "mobile", "mobilenumber", "telephone":
		return PIIPhone
	case "ip", "ipaddress", "ipv4", "ipv6":
		return PIIIP
	case "card", "cardnumber", "creditcard", "creditcardnumber":
		return PIICard
	case "iban":
		return PIIIBAN
	case "ssn", "nino", "nationalid", "socialsecuritynumber", "nationalinsurancenumber":
		return PIINationalID
	case "dob", "birthdate", "dateofbirth":
		return PIIDateOfBirth
	case "firstname", "lastname", "fullname", "givenname", "surname":
		return PIIPersonalName
	case "address", "postaladdress", "streetaddress", "mailingaddress":
		return PIIPostalAddress
	}
	return 0
}

func joinWords(words []string) string {
	var s string
	for _, w := range words {
		s += w
	}
	return s
}

func (r *Redactor) piiName(name string) bool {
	return r.pii != 0 && piiNameClass(name)&r.pii != 0
}
func (r *Redactor) sensitiveName(name string) bool {
	return r.policy.sensitive(name) || r.piiName(name)
}

// Reuse the assignment and flag parsers with a PII-only key policy. Credential
// rules keep their original policy and classification. Explicit benign credential
// keys do not suppress opted-in PII protection.
func findPIIName(r *Redactor, s *scanText, add func(int, int)) {
	copy := *r
	copy.policy = &keyPolicy{piiOnly: r.pii}
	findAssignment(&copy, s, add)
	findFlag(&copy, s, add)
}
