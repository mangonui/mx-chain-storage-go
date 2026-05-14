package txcache

import (
	"bytes"
	"math"
	"math/big"

	"github.com/multiversx/mx-chain-core-go/data"
)

// bunchOfTransactions is a slice of WrappedTransaction pointers
type bunchOfTransactions []*WrappedTransaction

// WrappedTransaction contains a transaction, its hash and extra information
type WrappedTransaction struct {
	Tx              data.TransactionHandler
	TxHash          []byte
	SenderShardID   uint32
	ReceiverShardID uint32
	Size            int64

	// These fields are only set within "precomputeFields".
	// We don't need to protect them with a mutex, since "precomputeFields" is called only once for each transaction.
	// Additional note: "WrappedTransaction" objects are created by the Node, in dataRetriever/txpool/shardedTxPool.go.
	Fee              *big.Int
	PricePerUnit     uint64
	TransferredValue *big.Int
	FeePayer         []byte
}

// precomputeFields computes (and caches) the (average) price per gas unit.
//
// ISSUE-057: previously this computed `wrappedTx.Fee.Uint64() / gasLimit`
// directly. `big.Int.Uint64()` silently truncates to the low 64 bits if
// the value exceeds `math.MaxUint64`, so a fee above 2^64 wei (chain-
// dependent but not impossible on chains with very high precision or
// adversarially-crafted fee fields) would wrap around — producing a
// PricePerUnit of 0 (or some tiny modular remainder), giving the tx the
// LOWEST priority in the mempool instead of the highest. That's
// mempool-priority manipulation by overflow.
//
// Now the division happens in big.Int math, and the result is clamped
// at MaxUint64 before narrowing. Overflowing fees end up with the
// highest possible priority (the intent of high-fee txs) rather than
// wrapping to zero.
func (wrappedTx *WrappedTransaction) precomputeFields(host MempoolHost) {
	wrappedTx.Fee = host.ComputeTxFee(wrappedTx.Tx)

	gasLimit := wrappedTx.Tx.GetGasLimit()
	if gasLimit != 0 {
		gasLimitBig := new(big.Int).SetUint64(gasLimit)
		pricePerUnit := new(big.Int).Quo(wrappedTx.Fee, gasLimitBig)
		if pricePerUnit.IsUint64() {
			wrappedTx.PricePerUnit = pricePerUnit.Uint64()
		} else {
			// Fee per unit exceeds MaxUint64 — clamp to the maximum
			// so the tx wins priority comparisons (the intent of a
			// very-high-fee tx) rather than wrapping to a small value.
			wrappedTx.PricePerUnit = math.MaxUint64
		}
	}

	wrappedTx.TransferredValue = host.GetTransferredValue(wrappedTx.Tx)
	wrappedTx.FeePayer = wrappedTx.decideFeePayer()
}

func (wrappedTx *WrappedTransaction) decideFeePayer() []byte {
	asRelayed, ok := wrappedTx.Tx.(data.RelayedTransactionHandler)
	if ok && len(asRelayed.GetRelayerAddr()) > 0 {
		return asRelayed.GetRelayerAddr()
	}

	return wrappedTx.Tx.GetSndAddr()
}

// Equality is out of scope (not possible in our case).
func (wrappedTx *WrappedTransaction) isTransactionMoreValuableForNetwork(otherTransaction *WrappedTransaction) bool {
	// First, compare by PPU (higher PPU is better).
	if wrappedTx.PricePerUnit != otherTransaction.PricePerUnit {
		return wrappedTx.PricePerUnit > otherTransaction.PricePerUnit
	}

	// If PPU is the same, compare by gas limit (higher gas limit is better, promoting less "execution fragmentation").
	gasLimit := wrappedTx.Tx.GetGasLimit()
	gasLimitOther := otherTransaction.Tx.GetGasLimit()

	if gasLimit != gasLimitOther {
		return gasLimit > gasLimitOther
	}

	// In the end, compare by transaction hash
	return bytes.Compare(wrappedTx.TxHash, otherTransaction.TxHash) < 0
}
