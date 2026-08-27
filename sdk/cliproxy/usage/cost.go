package usage

type CostQuality string

const (
	CostQualityComplete    CostQuality = "complete"
	CostQualityPartial     CostQuality = "partial"
	CostQualityUnavailable CostQuality = "unavailable"
)

// CostBreakdown is the request cost calculated from canonical token accounting.
type CostBreakdown struct {
	Currency            string      `json:"currency,omitempty"`
	Quality             CostQuality `json:"quality"`
	EstimatedTotal      float64     `json:"estimated_total"`
	Input               float64     `json:"input"`
	CacheRead           float64     `json:"cache_read"`
	Output              float64     `json:"output"`
	PricingSource       string      `json:"pricing_source,omitempty"`
	PricingSourceDigest string      `json:"pricing_source_digest,omitempty"`
}

// CalculateCost prices a valid token breakdown using USD rates per million tokens.
func CalculateCost(tokens TokenBreakdown, inputPerMillion, outputPerMillion float64, cacheReadPerMillion *float64, source, digest string) CostBreakdown {
	if !tokens.Valid() {
		return CostBreakdown{Quality: CostQualityUnavailable}
	}
	quality := CostQualityComplete
	if tokens.Quality != TokenAccountingQualityComplete || tokens.Input.CacheWriteTokens > 0 || tokens.UnclassifiedTokens > 0 {
		quality = CostQualityPartial
	}
	cacheRate := 0.0
	if cacheReadPerMillion != nil {
		cacheRate = *cacheReadPerMillion
	} else if tokens.Input.CacheReadTokens > 0 {
		quality = CostQualityPartial
	}
	const million = 1_000_000
	result := CostBreakdown{
		Currency:            "USD",
		Quality:             quality,
		Input:               float64(tokens.Input.UncachedTokens) * inputPerMillion / million,
		CacheRead:           float64(tokens.Input.CacheReadTokens) * cacheRate / million,
		Output:              float64(tokens.Output.TotalTokens) * outputPerMillion / million,
		PricingSource:       source,
		PricingSourceDigest: digest,
	}
	result.EstimatedTotal = result.Input + result.CacheRead + result.Output
	return result
}
