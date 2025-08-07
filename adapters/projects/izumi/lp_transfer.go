package izumi

import (
	"context"
	"fmt"
	"math"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/taikoxyz/trailblazer-adapters/adapters"
	"github.com/taikoxyz/trailblazer-adapters/adapters/contracts/erc20"
	"github.com/taikoxyz/trailblazer-adapters/adapters/contracts/izipool"
	"github.com/taikoxyz/trailblazer-adapters/adapters/contracts/izumi"
)

const (
	// https://taikoscan.io/address/0x33531bDBFE34fa6Fd5963D0423f7699775AacaaF
	LPAddress string = "0x33531bDBFE34fa6Fd5963D0423f7699775AacaaF"

	logTransferSignature string = "Transfer(address,address,uint256)"
	logDepositSignature  string = "Deposit(address,uint256,uint256)"
)

func Whitelist() []string {
	return []string{
		"0xE2380f4Cc37027B4bF23bBb3b6c092470dB4975f", // https://taikoscan.io/address/0xE2380f4Cc37027B4bF23bBb3b6c092470dB4975f
		"0x5264F77F8af8550cDa8e81Fee0360c0De6b52432", // https://taikoscan.io/address/0x5264F77F8af8550cDa8e81Fee0360c0De6b52432
	}
}

type LPTransferIndexer struct {
	client    *ethclient.Client
	addresses []common.Address
	Whitelist map[string]struct{}
}

func NewLPTransferIndexer(client *ethclient.Client, addresses []common.Address, whitelist []string) *LPTransferIndexer {
	indexer := &LPTransferIndexer{
		client:    client,
		addresses: addresses,
		Whitelist: map[string]struct{}{},
	}

	for _, addr := range whitelist {
		indexer.Whitelist[addr] = struct{}{}
	}

	return indexer
}

var _ adapters.LogIndexer[adapters.LPTransfer] = &LPTransferIndexer{}

func (indexer *LPTransferIndexer) Addresses() []common.Address {
	return indexer.addresses
}

func (indexer *LPTransferIndexer) Index(ctx context.Context, logs ...types.Log) ([]adapters.LPTransfer, error) {
	var lpTransfers []adapters.LPTransfer

	for _, l := range logs {
		if !indexer.isERC721Transfer(l) {
			continue
		}

		// Extract "from" and "to" addresses from the log
		to := common.BytesToAddress(l.Topics[2].Bytes()[12:])
		_, exists := indexer.Whitelist[to.Hex()]
		if !exists {
			return nil, nil
		}
		txReceipt, err := indexer.client.TransactionReceipt(ctx, l.TxHash)
		if err != nil {
			return nil, err
		}
		from := adapters.ZeroAddress()
		for _, log := range txReceipt.Logs {
			if log.Topics[0].Hex() == crypto.Keccak256Hash([]byte(logDepositSignature)).Hex() {
				from = common.BytesToAddress(log.Topics[1].Bytes()[12:])
			}
		}
		tokenID := l.Topics[3].Big()

		// Fetch the block details
		block, err := indexer.client.BlockByNumber(ctx, big.NewInt(int64(l.BlockNumber)))
		if err != nil {
			return nil, err
		}

		// Initialize the LiquidityManager contract caller
		liquidityManager, err := izumi.NewIzumiCaller(l.Address, indexer.client)
		if err != nil {
			return nil, err
		}
		// Fetch liquidity details using the Token ID (NFT)
		liquidity, err := liquidityManager.Liquidities(&bind.CallOpts{
			BlockNumber: block.Number(),
		}, tokenID)
		if err != nil {
			return nil, err
		}

		// Fetch pool metadata using the pool ID
		poolMeta, err := liquidityManager.PoolMetas(&bind.CallOpts{
			BlockNumber: block.Number(),
		}, liquidity.PoolId)
		if err != nil {
			return nil, err
		}

		// Fetch token addresses and decimals for the pool
		token0Address := poolMeta.TokenX
		token1Address := poolMeta.TokenY

		_, token0Decimals, err := fetchTokenDetails(token0Address, indexer.client)
		if err != nil {
			return nil, err
		}

		_, token1Decimals, err := fetchTokenDetails(token1Address, indexer.client)
		if err != nil {
			return nil, err
		}

		// Calculate the amount of tokens in the liquidity position
		token0Amount, token1Amount, err := calculateLiquidityAmounts(liquidityManager, liquidity, poolMeta, block, indexer.client)
		if err != nil {
			return nil, err
		}

		// Return the LPTransfer struct with calculated values
		lpTransfer := &adapters.LPTransfer{
			Metadata: adapters.Metadata{
				BlockTime:   block.Time(),
				BlockNumber: block.NumberU64(),
				TxHash:      l.TxHash,
			},
			From:           from,
			To:             to,
			Token0Amount:   token0Amount,
			Token0Decimals: token0Decimals,
			Token0:         token0Address,
			Token1Amount:   token1Amount,
			Token1Decimals: token1Decimals,
			Token1:         token1Address,
		}

		lpTransfers = append(lpTransfers, *lpTransfer)
	}

	return lpTransfers, nil
}

func (indexer *LPTransferIndexer) isERC721Transfer(l types.Log) bool {
	return len(l.Topics) == 4 && l.Topics[0].Hex() == crypto.Keccak256Hash([]byte(logTransferSignature)).Hex()
}

// Helper function to fetch token details (caller and decimals)
func fetchTokenDetails(tokenAddress common.Address, client *ethclient.Client) (token *erc20.Erc20Caller, decimals uint8, err error) {
	token, err = erc20.NewErc20Caller(tokenAddress, client)
	if err != nil {
		return
	}

	decimals, err = token.Decimals(nil)
	return
}

// Helper function to calculate the amount of tokens in the liquidity position
func calculateLiquidityAmounts(caller *izumi.IzumiCaller, liquidity struct {
	LeftPt           *big.Int
	RightPt          *big.Int
	Liquidity        *big.Int
	LastFeeScaleX128 *big.Int
	LastFeeScaleY128 *big.Int
	RemainTokenX     *big.Int
	RemainTokenY     *big.Int
	PoolId           *big.Int
}, poolMeta struct {
	TokenX common.Address
	TokenY common.Address
	Fee    *big.Int
}, block *types.Block, client *ethclient.Client) (token0Amount, token1Amount *big.Int, err error) {
	poolAddress, err := caller.Pool(&bind.CallOpts{
		BlockNumber: block.Number(),
	}, poolMeta.TokenX, poolMeta.TokenY, poolMeta.Fee)

	if poolAddress == adapters.ZeroAddress() || err != nil {
		return nil, nil, fmt.Errorf("pool not found")
	}

	pool, err := izipool.NewIziPoolCaller(poolAddress, client)
	if err != nil {
		return nil, nil, err
	}
	// Get current pool price and point
	state, err := getPoolPrice(pool, block)
	if err != nil {
		return nil, nil, err
	}

	amountX, amountY := getLiquidityValue(liquidity, *state)

	return amountX, amountY, nil
}

// Helper function to get the current price and point for the pool
func getPoolPrice(pool *izipool.IziPoolCaller, block *types.Block) (*struct {
	SqrtPrice96             *big.Int
	CurrentPoint            *big.Int
	ObservationCurrentIndex uint16
	ObservationQueueLen     uint16
	ObservationNextQueueLen uint16
	Locked                  bool
	Liquidity               *big.Int
	LiquidityX              *big.Int
}, error) {
	state, err := pool.State(&bind.CallOpts{
		BlockNumber: block.Number(),
	})
	if err != nil {
		return nil, err
	}

	return &state, nil
}

func pow(base *big.Float, exp *big.Int) *big.Float {
	result := new(big.Float).SetPrec(200).SetFloat64(1.0) // Initialize result as 1.0
	baseCopy := new(big.Float).SetPrec(200).Copy(base)    // Copy base to avoid modifying original

	expCopy := new(big.Int).Set(exp) // Copy exp to avoid modifying original
	zero := big.NewInt(0)

	for expCopy.Cmp(zero) > 0 { // While exp > 0
		result.Mul(result, baseCopy)
		expCopy.Sub(expCopy, big.NewInt(1))
	}

	return result
}

// Function to calculate sqrt(1.0001^point) using big.Float
func point2PoolPriceUndecimalSqrt(point *big.Int) *big.Float {
	constant := new(big.Float).SetPrec(200).SetFloat64(1.0001)

	// Determine if the point is negative
	isNegative := point.Sign() < 0

	// Take the absolute value of the point
	absPoint := new(big.Int).Abs(point)

	// Compute 1.0001^abs(point)
	expResult := pow(constant, absPoint)

	// Take the square root of the result
	sqrtResult := new(big.Float).SetPrec(200).Sqrt(expResult)

	// If the original point was negative, take the reciprocal of the result
	if isNegative {
		one := new(big.Float).SetPrec(200).SetFloat64(1.0)
		sqrtResult.Quo(one, sqrtResult)
	}

	return sqrtResult
}

// Function to calculate amountY using big.Float
func getAmountY(liquidity *big.Int, sqrtPriceL, sqrtPriceR, sqrtRate *big.Float, upper bool) *big.Int {
	numerator := new(big.Float).Sub(sqrtPriceR, sqrtPriceL)
	denominator := new(big.Float).Sub(sqrtRate, big.NewFloat(1.0))

	amount := new(big.Float).SetPrec(200).SetInt(liquidity)
	amount.Mul(amount, numerator)
	amount.Quo(amount, denominator)

	result := new(big.Int)
	if upper {
		amount.Add(amount, big.NewFloat(0.5)) // Round up
	}
	amount.Int(result)

	return result
}

// Function to calculate amountY at a specific point using big.Float
func liquidity2AmountYAtPoint(liquidity *big.Int, sqrtPrice *big.Float, upper bool) *big.Int {
	amountY := new(big.Float).SetPrec(200).SetInt(liquidity)
	amountY.Mul(amountY, sqrtPrice)

	result := new(big.Int)
	if upper {
		amountY.Add(amountY, big.NewFloat(0.5)) // Round up
	}
	amountY.Int(result)

	return result
}

// Function to calculate amountX using big.Float
func getAmountX(liquidity *big.Int, leftPt, rightPt *big.Int, sqrtPriceR, sqrtRate *big.Float, upper bool) *big.Int {
	// Calculate sqrtPricePrPc = sqrtRate^(rightPt-leftPt+1)
	diff := new(big.Int).Sub(rightPt, leftPt)
	diff.Add(diff, big.NewInt(1))
	sqrtPricePrPc := pow(sqrtRate, diff)

	// Calculate sqrtPricePrPd = sqrtRate^(rightPt+1)
	rightPtPlusOne := new(big.Int).Add(rightPt, big.NewInt(1))
	sqrtPricePrPd := pow(sqrtRate, rightPtPlusOne)

	numerator := new(big.Float).Sub(sqrtPricePrPc, sqrtRate)
	denominator := new(big.Float).Sub(sqrtPricePrPd, sqrtPriceR)

	amount := new(big.Float).SetPrec(200).SetInt(liquidity)
	amount.Mul(amount, numerator)
	amount.Quo(amount, denominator)

	result := new(big.Int)
	if upper {
		amount.Add(amount, big.NewFloat(0.5)) // Round up
	}
	amount.Int(result)

	return result
}

// Function to calculate amountX at a specific point using big.Float
func liquidity2AmountXAtPoint(liquidity *big.Int, sqrtPrice *big.Float, upper bool) *big.Int {
	amountX := new(big.Float).SetPrec(200).SetInt(liquidity)
	amountX.Quo(amountX, sqrtPrice)

	result := new(big.Int)
	if upper {
		amountX.Add(amountX, big.NewFloat(0.5)) // Round up
	}
	amountX.Int(result)

	return result
}

// Helper function to calculate the min of two *big.Int values
func minBigInt(a, b *big.Int) *big.Int {
	if a.Cmp(b) < 0 {
		return a
	}
	return b
}

// Helper function to calculate the max of two *big.Int values
func maxBigInt(a, b *big.Int) *big.Int {
	if a.Cmp(b) > 0 {
		return a
	}
	return b
}

// Main function to calculate the liquidity value
func getLiquidityValue(liquidity struct {
	LeftPt           *big.Int
	RightPt          *big.Int
	Liquidity        *big.Int
	LastFeeScaleX128 *big.Int
	LastFeeScaleY128 *big.Int
	RemainTokenX     *big.Int
	RemainTokenY     *big.Int
	PoolId           *big.Int
}, state struct {
	SqrtPrice96             *big.Int
	CurrentPoint            *big.Int
	ObservationCurrentIndex uint16
	ObservationQueueLen     uint16
	ObservationNextQueueLen uint16
	Locked                  bool
	Liquidity               *big.Int
	LiquidityX              *big.Int
}) (amountX, amountY *big.Int) {
	amountX = big.NewInt(0)
	amountY = big.NewInt(0)
	sqrtRate := new(big.Float).SetPrec(200).SetFloat64(math.Sqrt(1.0001))

	// Compute amountY without currentPoint
	if liquidity.LeftPt.Cmp(state.CurrentPoint) < 0 {
		rightPt := minBigInt(state.CurrentPoint, liquidity.RightPt)
		sqrtPriceR := point2PoolPriceUndecimalSqrt(rightPt)
		sqrtPriceL := point2PoolPriceUndecimalSqrt(liquidity.LeftPt)
		amountY = getAmountY(liquidity.Liquidity, sqrtPriceL, sqrtPriceR, sqrtRate, false)
	}

	// Compute amountX without currentPoint
	if liquidity.RightPt.Cmp(new(big.Int).Add(state.CurrentPoint, big.NewInt(1))) > 0 {
		leftPt := maxBigInt(new(big.Int).Add(state.CurrentPoint, big.NewInt(1)), liquidity.LeftPt)
		sqrtPriceR := point2PoolPriceUndecimalSqrt(liquidity.RightPt)
		amountX = getAmountX(liquidity.Liquidity, leftPt, liquidity.RightPt, sqrtPriceR, sqrtRate, false)
	}

	// Compute amountX and amountY on currentPoint
	if liquidity.LeftPt.Cmp(state.CurrentPoint) <= 0 && liquidity.RightPt.Cmp(state.CurrentPoint) > 0 {
		liquidityValue := new(big.Int).Set(liquidity.Liquidity)
		maxLiquidityYAtCurrentPt := new(big.Int).Sub(state.Liquidity, state.LiquidityX)
		liquidityYAtCurrentPt := minBigInt(liquidityValue, maxLiquidityYAtCurrentPt)
		liquidityXAtCurrentPt := new(big.Int).Sub(liquidityValue, liquidityYAtCurrentPt)
		currentSqrtPrice := point2PoolPriceUndecimalSqrt(state.CurrentPoint)
		amountX.Add(amountX, liquidity2AmountXAtPoint(liquidityXAtCurrentPt, currentSqrtPrice, false))
		amountY.Add(amountY, liquidity2AmountYAtPoint(liquidityYAtCurrentPt, currentSqrtPrice, false))
	}

	return amountX, amountY
}
