package context

// Compile-time interface assertions.
var (
	_ Summarizer     = (*ProviderSummarizer)(nil)
	_ TokenEstimator = DefaultEstimator{}
)
