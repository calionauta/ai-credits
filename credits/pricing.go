package credits

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
)

// microUnits per 1M tokens (micro-dollars). $0.15/1M → 150000.
type modelPrices struct {
	InputPerMtok       int64 `json:"input_per_mtok"`
	OutputPerMtok      int64 `json:"output_per_mtok"`
	CachedInputPerMtok int64 `json:"cached_input_per_mtok"`
	ReasoningPerMtok   int64 `json:"reasoning_per_mtok"`
}

// pricingFile is the external JSON shape (see §5.1).
type pricingFile struct {
	Version             string                 `json:"version"`
	MicrounitsPerCredit int64                  `json:"microunits_per_credit"`
	Models              map[string]modelPrices `json:"models"`
}

// testMiniModel is a model id used in defaults + tests.
const testMiniModel = "gpt-4o-mini"

var defaultModels = map[string]modelPrices{
	testMiniModel:       {InputPerMtok: 150000, OutputPerMtok: 600000, CachedInputPerMtok: 150000},
	"gpt-4o":            {InputPerMtok: 2500000, OutputPerMtok: 10000000, CachedInputPerMtok: 1250000},
	"claude-3-5-haiku":  {InputPerMtok: 800000, OutputPerMtok: 4000000, CachedInputPerMtok: 80000},
	"claude-3-5-sonnet": {InputPerMtok: 3000000, OutputPerMtok: 15000000, CachedInputPerMtok: 300000},
}

const defaultMicrounitsPerCredit = 1000 // 1 credit = $0.001

// microUnitsPerMTok is 1_000_000: model prices are "per 1M tokens", token
// counts are raw; dividing by this converts to micro-units.
const microUnitsPerMTok int64 = 1_000_000

// reserveMargin inflates EstimateMax over the nominal max cost (see §5.2).
// Expressed as an integer fraction (6/5 = 1.2) to keep EstimateMax integer
// arithmetic exact.
const (
	reserveMarginNum = 6
	reserveMarginDen = 5
)

// pricerEngine is the pricing engine: a model table + microunits per credit.
type pricerEngine struct {
	version             string
	microunitsPerCredit int64
	models              map[string]modelPrices
}

// newPricer builds the engine from a JSON file (or built-in defaults if nil),
// with the given pricing version.
func newPricer(r io.Reader, version string) (*pricerEngine, error) {
	p := &pricerEngine{
		version:             version,
		microunitsPerCredit: defaultMicrounitsPerCredit,
		models:              defaultModels,
	}
	if r == nil {
		return p, nil
	}
	var f pricingFile
	if err := json.NewDecoder(r).Decode(&f); err != nil {
		return nil, err
	}
	if f.Version != "" {
		p.version = f.Version
	}
	if f.MicrounitsPerCredit > 0 {
		p.microunitsPerCredit = f.MicrounitsPerCredit
	}
	if f.Models != nil {
		p.models = f.Models
	}
	return p, nil
}

// cost computes cost in micro-units for the given token counts.
// Integer arithmetic: cost = sum(token / 1e6 * perMtok). To avoid float
// drift at the credit boundary we multiply tokens by perMtok first (exact
// int64), then round up the division by 1e6.
func (p *pricerEngine) cost(model string, input, output, cached, reasoning int64) (int64, error) {
	m, ok := p.models[model]
	if !ok {
		return 0, ErrUnknownModel
	}
	values := [][2]int64{{input, m.InputPerMtok}, {output, m.OutputPerMtok}, {cached, m.CachedInputPerMtok}, {reasoning, m.ReasoningPerMtok}}
	var numerator int64
	for _, value := range values {
		if value[0] < 0 || value[1] < 0 || (value[0] != 0 && value[1] > math.MaxInt64/value[0]) {
			return 0, errors.New("credits: pricing overflow or negative token count")
		}
		product := value[0] * value[1]
		if numerator > math.MaxInt64-product {
			return 0, errors.New("credits: pricing overflow")
		}
		numerator += product
	}
	return intDivCeil(numerator, microUnitsPerMTok), nil
}

func pCost(m modelPrices, input, output int64) (int64, error) {
	p := &pricerEngine{models: map[string]modelPrices{"estimate": m}}
	return p.cost("estimate", input, output, 0, 0)
}

// intDivCeil returns ceil(a/b) for b > 0 using integer arithmetic (no float).
func intDivCeil(a, b int64) int64 {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// Cost returns the estimated cost (in micro-units) of a Usage.
//
//nolint:revive // ctx kept for API consistency across Service methods.
func (s *Service) Cost(ctx context.Context, u Usage) (int64, error) {
	return s.pricer.cost(u.Model, int64(u.InputTokens), int64(u.OutputTokens),
		int64(u.CachedTokens), int64(u.ReasoningTokens))
}

// credits converts micro-units to credits, rounding up (integer, no float).
func creditsFromMicrounits(mu, perCredit int64) int64 {
	if perCredit <= 0 {
		perCredit = defaultMicrounitsPerCredit
	}
	if mu <= 0 {
		return 0
	}
	return (mu + perCredit - 1) / perCredit
}

// Credits converts a Usage into whole credits.
func (s *Service) Credits(ctx context.Context, u Usage) (int64, error) {
	mu, err := s.Cost(ctx, u)
	if err != nil {
		return 0, err
	}
	return creditsFromMicrounits(mu, s.pricer.microunitsPerCredit), nil
}

// EstimateMax returns conservative credits for a call with unknown output:
// input + max output (ignoring cache) scaled by RESERVE_MARGIN. Used to size
// a reservation.
func (s *Service) EstimateMax(ctx context.Context, model string,
	inputTokens, maxOutputTokens int,
) (int64, error) {
	_ = ctx // ctx kept for API consistency (PLAN §3).
	if inputTokens < 0 || maxOutputTokens < 0 {
		return 0, errors.New("credits: token counts must be non-negative")
	}
	m, ok := s.pricer.models[model]
	if !ok {
		return 0, ErrUnknownModel
	}
	mu, err := pCost(m, int64(inputTokens), int64(maxOutputTokens))
	if err != nil {
		return 0, err
	}
	// Scale by the reserve margin, rounding up; integer-only.
	if mu > math.MaxInt64/reserveMarginNum {
		return 0, errors.New("credits: reserve estimate overflow")
	}
	mu = intDivCeil(mu*reserveMarginNum, reserveMarginDen)
	credits := creditsFromMicrounits(mu, s.pricer.microunitsPerCredit)
	return max(credits, int64(1)), nil
}
