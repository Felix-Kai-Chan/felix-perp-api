module github.com/chantrumps/felix-perp-api

go 1.25.7

require (
	github.com/chantrumps/felix-perp-core/felix-perp-orderbook v0.0.0-00010101000000-000000000000
	github.com/ethereum/go-ethereum v1.17.10
	github.com/joho/godotenv v1.5.1
)

require (
	github.com/holiman/uint256 v1.3.2 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/chantrumps/felix-perp-core/felix-perp-orderbook => ../felix-perp-orderbook
