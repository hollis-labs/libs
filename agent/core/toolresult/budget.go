package toolresult

// BudgetOptions tunes [BudgetForWindow]. A zero field takes its default:
// Floor 4000, Ceiling 32000, BytesPerToken 4, Fraction 0.004.
type BudgetOptions struct {
	// Floor is the smallest budget returned, in bytes.
	Floor int
	// Ceiling is the largest budget returned, in bytes.
	Ceiling int
	// BytesPerToken converts the window from tokens to an approximate byte
	// size.
	BytesPerToken int
	// Fraction is the share of the window in bytes proposed as the budget.
	Fraction float64
}

// BudgetForWindow returns the per-result preview and page budget, in bytes,
// for a model whose context window is windowTokens tokens: Fraction of the
// window converted to bytes, clamped to [Floor, Ceiling]. A window of zero or
// less (unknown model) yields Floor. With the defaults a 200K-token window
// keeps the 4000-byte floor and a 1M-token window gets 16000 bytes.
//
// The caller resolves the window; this package knows no model catalog.
func BudgetForWindow(windowTokens int, o BudgetOptions) int {
	if o.Floor <= 0 {
		o.Floor = 4000
	}
	if o.Ceiling <= 0 {
		o.Ceiling = 32000
	}
	if o.BytesPerToken <= 0 {
		o.BytesPerToken = 4
	}
	if o.Fraction <= 0 {
		o.Fraction = 0.004
	}
	if o.Ceiling < o.Floor {
		o.Ceiling = o.Floor
	}
	if windowTokens <= 0 {
		return o.Floor
	}
	proposed := int(float64(windowTokens*o.BytesPerToken) * o.Fraction)
	return min(o.Ceiling, max(o.Floor, proposed))
}
