package drips

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
	"github.com/taikoxyz/trailblazer-adapters/adapters/contracts/drips"
)

const (
	// https://taikoscan.io/address/0x46f0a2e45bee8e9ebfdb278ce06caa6af294c349
	LockAddress string = "0x46f0a2e45bee8e9ebfdb278ce06caa6af294c349"

	logDepositWithDurationSignature string = "DepositWithDuration(address,uint256,uint256,uint256)"
)

type LockIndexer struct {
	client    *ethclient.Client
	addresses []common.Address
}

func NewLockIndexer(client *ethclient.Client, addresses []common.Address) *LockIndexer {
	return &LockIndexer{
		client:    client,
		addresses: addresses,
	}
}

var _ adapters.LogIndexer[adapters.Lock] = &LockIndexer{}

func (indexer *LockIndexer) Addresses() []common.Address {
	return indexer.addresses
}

func (indexer *LockIndexer) Index(ctx context.Context, logs ...types.Log) ([]adapters.Lock, error) {
	var locks []adapters.Lock

	for _, l := range logs {
		if !indexer.isDepositWithDuration(l) {
			continue
		}

		var depositWithDurationEvent struct {
			LockStart *big.Int
			Amount    *big.Int
			Duration  *big.Int
		}

		user := common.BytesToAddress(l.Topics[1].Bytes()[12:])

		dripsABI, err := abi.JSON(strings.NewReader(drips.DripsABI))
		if err != nil {
			return nil, err
		}

		err = dripsABI.UnpackIntoInterface(&depositWithDurationEvent, "DepositWithDuration", l.Data)
		if err != nil {
			return nil, err
		}

		block, err := indexer.client.BlockByNumber(ctx, big.NewInt(int64(l.BlockNumber)))
		if err != nil {
			return nil, err
		}

		lock := &adapters.Lock{
			Metadata: adapters.Metadata{
				BlockTime:   block.Time(),
				BlockNumber: block.NumberU64(),
				TxHash:      l.TxHash,
			},
			User:          user,
			TokenAmount:   depositWithDurationEvent.Amount,
			TokenDecimals: adapters.TaikoTokenDecimals,
			Token:         common.HexToAddress(adapters.TaikoTokenAddress),
			Duration:      depositWithDurationEvent.Duration.Uint64(),
		}

		locks = append(locks, *lock)
	}

	return locks, nil
}

func (indexer *LockIndexer) isDepositWithDuration(l types.Log) bool {
	return l.Topics[0].Hex() == crypto.Keccak256Hash([]byte(logDepositWithDurationSignature)).Hex()
}
