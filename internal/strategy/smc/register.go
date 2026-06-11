package smc

import "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"

// Instance is the package-level singleton for each strategy, exported so the
// transitional wrappers in package main can call its Detect method without
// re-instantiating with default config every time.
var (
	OrderBlockMitInstance     = &OrderBlockMit{Cfg: DefaultOrderBlockMitConfig()}
	BreakerBlockInstance      = &BreakerBlock{Cfg: DefaultBreakerBlockConfig()}
	LiquiditySweepBoSInstance = &LiquiditySweepBoS{Cfg: DefaultLiquiditySweepBoSConfig()}
	SilverBulletInstance      = &SilverBullet{Cfg: DefaultSilverBulletConfig()}
	TurtleSoupInstance        = &TurtleSoup{Cfg: DefaultTurtleSoupConfig()}
	OTEFibonacciInstance      = &OTEFibonacci{Cfg: DefaultOTEFibonacciConfig()}
	FVGFillInstance           = &FVGFill{Cfg: DefaultFVGFillConfig()}
	PowerOfThreeInstance      = &PowerOfThree{Cfg: DefaultPowerOfThreeConfig()}
	AsianRangeBreakoutInstance = &AsianRangeBreakout{Cfg: DefaultAsianRangeBreakoutConfig()}
	JudasSwingInstance        = &JudasSwing{Cfg: DefaultJudasSwingConfig()}
)

// init registers every SMC-family strategy in the engine's priority order —
// "first match wins" so order matters. SilverBullet leads (most selective,
// highest-quality), JudasSwing trails (broadest, lowest-conviction).
func init() {
	strategy.Register(SilverBulletInstance)
	strategy.Register(TurtleSoupInstance)
	strategy.Register(PowerOfThreeInstance)
	strategy.Register(OrderBlockMitInstance)
	strategy.Register(FVGFillInstance)
	strategy.Register(BreakerBlockInstance)
	strategy.Register(LiquiditySweepBoSInstance)
	strategy.Register(OTEFibonacciInstance)
	strategy.Register(AsianRangeBreakoutInstance)
	strategy.Register(JudasSwingInstance)
}
