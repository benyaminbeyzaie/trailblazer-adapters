package avalon

import (
	"context"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/taikoxyz/trailblazer-adapters/adapters"
	"github.com/taikoxyz/trailblazer-adapters/adapters/contracts/avalon_claim"
)

const (
	// https://taikoscan.io/address/0x631da08b6258EfAAe5aAc7Bc69e6a8fF2C79cFd9
	ClaimAddress string = "0x631da08b6258EfAAe5aAc7Bc69e6a8fF2C79cFd9"
	// L1: https://etherscan.io/token/0x5c8d0c48810fd37a0a824d074ee290e64f7a8fa2
	// L2: https://taikoscan.io/token/0xE9cA67e5051e1806546d0a06ee465221c5877feE
	AvlTokenAddress string = "0xE9cA67e5051e1806546d0a06ee465221c5877feE"
	AvlTokenDecimal uint8  = 18

	logClaimedSignature string = "Claimed(address,uint256,uint256)"
)

type ClaimIndexer struct {
	client    *ethclient.Client
	addresses []common.Address
}

func NewClaimIndexer(client *ethclient.Client, addresses []common.Address) *ClaimIndexer {
	return &ClaimIndexer{
		client:    client,
		addresses: addresses,
	}
}

var _ adapters.LogIndexer[adapters.Position] = &ClaimIndexer{}

func (indexer *ClaimIndexer) Addresses() []common.Address {
	return indexer.addresses
}

func (indexer *ClaimIndexer) Index(ctx context.Context, logs ...types.Log) ([]adapters.Position, error) {
	var claimedEvent struct {
		AvlAmount  *big.Int
		UsdaAmount *big.Int
	}

	var claims []adapters.Position

	for _, l := range logs {
		if !indexer.isClaimed(l) {
			continue
		}

		user := common.BytesToAddress(l.Topics[1].Bytes()[12:])

		AvalonClaimABI, err := abi.JSON(strings.NewReader(avalon_claim.ABI))
		if err != nil {
			return nil, err
		}

		err = AvalonClaimABI.UnpackIntoInterface(&claimedEvent, "Claimed", l.Data)
		if err != nil {
			return nil, err
		}

		block, err := indexer.client.BlockByNumber(ctx, big.NewInt(int64(l.BlockNumber)))
		if err != nil {
			return nil, err
		}

		claim := &adapters.Position{
			Metadata: adapters.Metadata{
				BlockTime:   block.Time(),
				BlockNumber: block.NumberU64(),
				TxHash:      l.TxHash,
			},
			User:          user,
			TokenAmount:   claimedEvent.AvlAmount,
			TokenDecimals: AvlTokenDecimal,
			Token:         common.HexToAddress(AvlTokenAddress),
		}

		claims = append(claims, *claim)
	}

	return claims, nil
}

func (indexer *ClaimIndexer) isClaimed(l types.Log) bool {
	return l.Topics[0].Hex() == crypto.Keccak256Hash([]byte(logClaimedSignature)).Hex()
}
