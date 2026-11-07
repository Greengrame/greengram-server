// Package wallet is the built-in crypto wallet: per-user balances in
// on-instance units, internal transfers, swaps at market prices and buying
// Stars with crypto. Nothing touches a blockchain; deposits are admin
// adjustments, like greengram-web's crypto-*.js.
package wallet

import (
	"math/big"
	"regexp"
	"strings"
)

type Asset struct {
	Code     string
	Name     string
	Decimals int
	Networks []string
	MarketID string // CoinGecko id
	Category string // "" for coins, "stock" and "etf" for tokenized ones: Trade sorts by it
}

// Assets is crypto-assets.js; the order is the order clients list them in.
var Assets = []Asset{
	{"TON", "Gram", 9, []string{"ton"}, "the-open-network", ""},
	{"USDT", "USD Tether", 6, []string{"ton", "ethereum", "tron"}, "tether", ""},
	{"BTC", "Bitcoin", 8, []string{"bitcoin"}, "bitcoin", ""},
	{"XAUT", "Gold", 6, []string{"ethereum"}, "tether-gold", ""},
	{"ETH", "Ethereum", 18, []string{"ethereum"}, "ethereum", ""},
	{"XRP", "XRP", 6, []string{"xrp"}, "ripple", ""},
	{"TRX", "TRON", 6, []string{"tron"}, "tron", ""},
	{"BNB", "BNB", 18, []string{"bsc"}, "binancecoin", ""},
	{"SOL", "Solana", 9, []string{"solana"}, "solana", ""},
	{"LTC", "Litecoin", 8, []string{"litecoin"}, "litecoin", ""},
	{"SUI", "Sui", 9, []string{"sui"}, "sui", ""},
	{"DOGE", "Dogecoin", 8, []string{"dogecoin"}, "dogecoin", ""},
	{"DASH", "Dash", 8, []string{"dash"}, "dash", ""},
	{"PI", "Pi Network", 7, []string{"pi"}, "pi-network", ""},
	{"AAVE", "Aave", 18, []string{"ethereum"}, "aave", ""},
	{"HYPE", "Hyperliquid", 8, []string{"hyperliquid"}, "hyperliquid", ""},
	{"LINK", "Chainlink", 18, []string{"ethereum"}, "chainlink", ""},
	{"UNI", "Uniswap", 18, []string{"ethereum"}, "uniswap", ""},

	{"ADA", "Cardano", 6, []string{"cardano"}, "cardano", ""},
	{"AVAX", "Avalanche", 18, []string{"avalanche"}, "avalanche-2", ""},
	{"DOT", "Polkadot", 10, []string{"polkadot"}, "polkadot", ""},
	{"POL", "Polygon", 18, []string{"polygon"}, "polygon-ecosystem-token", ""},
	{"NEAR", "NEAR Protocol", 18, []string{"near"}, "near", ""},
	{"ATOM", "Cosmos", 6, []string{"cosmos"}, "cosmos", ""},
	{"APT", "Aptos", 8, []string{"aptos"}, "aptos", ""},
	{"ARB", "Arbitrum", 18, []string{"arbitrum"}, "arbitrum", ""},
	{"OP", "Optimism", 18, []string{"optimism"}, "optimism", ""},
	{"SHIB", "Shiba Inu", 18, []string{"ethereum"}, "shiba-inu", ""},
	{"PEPE", "Pepe", 18, []string{"ethereum"}, "pepe", ""},
	{"DAI", "Dai", 18, []string{"ethereum"}, "dai", ""},
	{"BCH", "Bitcoin Cash", 8, []string{"bitcoin-cash"}, "bitcoin-cash", ""},
	{"ETC", "Ethereum Classic", 18, []string{"ethereum-classic"}, "ethereum-classic", ""},
	{"XLM", "Stellar", 7, []string{"stellar"}, "stellar", ""},
	{"TAO", "Bittensor", 9, []string{"bittensor"}, "bittensor", ""},
	{"WLD", "Worldcoin", 18, []string{"ethereum"}, "worldcoin-wld", ""},
	{"NOT", "Notcoin", 9, []string{"ton"}, "notcoin", ""},
	{"DOGS", "DOGS", 9, []string{"ton"}, "dogs-2", ""},
	{"ENA", "Ethena", 18, []string{"ethereum"}, "ethena", ""},
	{"ONDO", "Ondo", 18, []string{"ethereum"}, "ondo-finance", ""},
	{"JUP", "Jupiter", 6, []string{"solana"}, "jupiter-exchange-solana", ""},

	// Backed's xStocks, under the codes the wallet app knows them by. CoinGecko
	// prices the token, in the same call as every coin.
	{"AAPLx", "Apple", 8, []string{"solana"}, "apple-xstock", "stock"},
	{"TSLAx", "Tesla", 8, []string{"solana"}, "tesla-xstock", "stock"},
	{"NVDAx", "NVIDIA", 8, []string{"solana"}, "nvidia-xstock", "stock"},
	{"MSFTx", "Microsoft", 8, []string{"solana"}, "microsoft-xstock", "stock"},
	{"GOOGLx", "Alphabet", 8, []string{"solana"}, "alphabet-xstock", "stock"},
	{"AMZNx", "Amazon", 8, []string{"solana"}, "amazon-xstock", "stock"},
	{"METAx", "Meta", 8, []string{"solana"}, "meta-xstock", "stock"},
	{"NFLXx", "Netflix", 8, []string{"solana"}, "netflix-xstock", "stock"},
	{"AMDx", "AMD", 8, []string{"solana"}, "amd-xstock", "stock"},
	{"COINx", "Coinbase", 8, []string{"solana"}, "coinbase-xstock", "stock"},
	{"MSTRx", "Strategy", 8, []string{"solana"}, "microstrategy-xstock", "stock"},
	{"HOODx", "Robinhood", 8, []string{"solana"}, "robinhood-xstock", "stock"},
	{"PLTRx", "Palantir", 8, []string{"solana"}, "palantir-xstock", "stock"},
	{"ORCLx", "Oracle", 8, []string{"solana"}, "oracle-xstock", "stock"},
	{"INTCx", "Intel", 8, []string{"solana"}, "intel-xstock", "stock"},
	{"JPMx", "JPMorgan Chase", 8, []string{"solana"}, "jpmorgan-chase-xstock", "stock"},
	{"CRCLx", "Circle", 8, []string{"solana"}, "circle-xstock", "stock"},
	{"SPYx", "S&P 500", 8, []string{"solana"}, "sp500-xstock", "etf"},
	{"QQQx", "Nasdaq 100", 8, []string{"solana"}, "nasdaq-xstock", "etf"},
	{"TQQQx", "Nasdaq 100 x3", 8, []string{"solana"}, "tqqq-xstock", "etf"},
	{"GLDx", "Gold Trust", 8, []string{"solana"}, "gold-xstock", "etf"},
}

// clientCategory is the client's word for the asset's Trade tab. The page's
// Crypto tab takes "onchain" and "trading", and "trading" also puts up a
// banner saying the coin can't leave the wallet.
func clientCategory(a Asset) string {
	if a.Category == "" {
		return "onchain"
	}
	return a.Category
}

// Canonical maps the client's name for an asset to ours: the wallet UI
// calls TON "GRAM".
func Canonical(code string) string {
	if code == "GRAM" {
		return "TON"
	}
	return code
}

func Lookup(code string) (Asset, bool) {
	code = Canonical(code)
	for _, a := range Assets {
		if a.Code == code {
			return a, true
		}
	}
	return Asset{}, false
}

var amountRe = regexp.MustCompile(`^\d+(?:\.\d+)?$`)

// ParseAmount reads "1.5" as base units of the asset. ok is false for
// anything parseCryptoAmount refused: unknown asset, not a plain decimal,
// over 60 characters or more decimals than the asset has.
func ParseAmount(value, code string) (*big.Int, bool) {
	a, found := Lookup(code)
	if !found || len(value) > 60 || !amountRe.MatchString(value) {
		return nil, false
	}
	whole, frac, _ := strings.Cut(value, ".")
	if len(frac) > a.Decimals {
		return nil, false
	}
	n, _ := new(big.Int).SetString(whole+frac+strings.Repeat("0", a.Decimals-len(frac)), 10)
	return n, true
}

// FormatAmount is the inverse, trailing zeros dropped: 1500000000 TON is "1.5".
func FormatAmount(units *big.Int, code string) string {
	a, found := Lookup(code)
	if !found || units == nil {
		return ""
	}
	neg := units.Sign() < 0
	digits := new(big.Int).Abs(units).String()
	if len(digits) <= a.Decimals {
		digits = strings.Repeat("0", a.Decimals-len(digits)+1) + digits
	}
	whole, frac := digits[:len(digits)-a.Decimals], strings.TrimRight(digits[len(digits)-a.Decimals:], "0")
	out := whole
	if frac != "" {
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}
