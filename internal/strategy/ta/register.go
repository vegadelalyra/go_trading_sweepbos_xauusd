package ta

import "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"

var (
	EMACrossInstance          = &EMACross{Cfg: DefaultEMACrossConfig()}
	BollingerReversalInstance = &BollingerReversal{Cfg: DefaultBollingerReversalConfig()}
	DailyBreakoutInstance     = &DailyBreakout{Cfg: DefaultDailyBreakoutConfig()}
)

// init registers the TA-family strategies after the SMC family — same
// priority order the legacy main-package slice used.
func init() {
	strategy.Register(EMACrossInstance)
	strategy.Register(BollingerReversalInstance)
	strategy.Register(DailyBreakoutInstance)
}
