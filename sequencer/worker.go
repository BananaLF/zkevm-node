package sequencer

import (
	"context"
	"math/big"
	"runtime"
	"sync"

	"github.com/0xPolygonHermez/zkevm-node/state"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Worker represents the worker component of the sequencer
type Worker struct {
	pool                 map[string]addrQueue // This should have (almost) all txs from the pool
	efficiencyList       *efficiencyList
	workerMutex          sync.Mutex
	dbManager            dbManagerInterface
	state                stateInterface
	batchConstraints     batchConstraints
	batchResourceWeights batchResourceWeights
}

// NewWorker creates an init a worker
func NewWorker(cfg Config, state stateInterface, constraints batchConstraints, weights batchResourceWeights) *Worker {
	w := Worker{
		pool:                 make(map[string]addrQueue),
		efficiencyList:       newEfficiencyList(),
		state:                state,
		batchConstraints:     constraints,
		batchResourceWeights: weights,
	}

	const defaultCostWeigth = float64(1.0 / 9.0)

	return &w
}

// NewTx creates and init a TxTracker
// TODO: Rename to NewTxTracker?
func (w *Worker) NewTx(tx types.Transaction, counters state.ZKCounters) (*TxTracker, error) {
	return newTxTracker(tx, counters, w.batchConstraints, w.batchResourceWeights)
}

// AddTx adds a new Tx to the Worker
// TODO: Rename to AddTxTracker?
func (w *Worker) AddTx(ctx context.Context, tx *TxTracker) {
	// TODO: Review if additional mutex is needed to lock GetBestFittingTx
	w.workerMutex.Lock()
	defer w.workerMutex.Unlock()

	addr, found := w.pool[tx.FromStr]

	if !found {
		// Unlock the worker to let execute other worker functions while creating the new AddrQueue
		w.workerMutex.Unlock()

		root, error := w.state.GetLastStateRoot(ctx)
		if error != nil {
			// TODO: How to manage this
			return
		}
		nonce, error := w.state.GetNonce(ctx, tx.From, root)
		if error != nil {
			// TODO: How to manage this
			return
		}
		balance, error := w.state.GetBalance(ctx, tx.From, root)
		if error != nil {
			// TODO: How to manage this
			return
		}

		addr = newAddrQueue(tx.From, nonce.Uint64(), balance)

		// Lock again the worker
		w.workerMutex.Lock()

		w.pool[tx.FromStr] = addr
	}

	// Add the txTracker to Addr and get the newReadyTx and prevReadyTx
	newReadyTx, prevReadyTx := addr.addTx(tx)

	// Update the EfficiencyList (if needed)
	if prevReadyTx != nil {
		w.efficiencyList.delete(prevReadyTx)
	}
	if newReadyTx != nil {
		w.efficiencyList.add(newReadyTx)
	}
}

func (w *Worker) applyAddressUpdate(from common.Address, fromNonce *uint64, fromBalance *big.Int) (*TxTracker, *TxTracker) {
	addrQueue, found := w.pool[from.String()]

	if found {
		newReadyTx, prevReadyTx := addrQueue.updateCurrentNonceBalance(fromNonce, fromBalance)

		// Update the EfficiencyList (if needed)
		if prevReadyTx != nil {
			w.efficiencyList.delete(prevReadyTx)
		}
		if newReadyTx != nil {
			w.efficiencyList.add(newReadyTx)
		}

		return newReadyTx, prevReadyTx
	}

	return nil, nil
}

// UpdateAfterSingleSuccessfulTxExecution updates the touched addresses after execute on Executor a successfully tx
func (w *Worker) UpdateAfterSingleSuccessfulTxExecution(from common.Address, touchedAddresses map[common.Address]*state.TouchedAddress) {
	w.workerMutex.Lock()
	defer w.workerMutex.Unlock()

	// TODO: Check if from exists in toucedAddresses, if not warning
	fromNonce, fromBalance := touchedAddresses[from].Nonce, touchedAddresses[from].Balance
	w.applyAddressUpdate(from, fromNonce, fromBalance)

	for addr, addressInfo := range touchedAddresses {
		w.applyAddressUpdate(addr, nil, addressInfo.Balance)
	}
}

// MoveTxToNotReady move a tx to not ready after it fails to execute
func (w *Worker) MoveTxToNotReady(txHash common.Hash, from common.Address, actualNonce *uint64, actualBalance *big.Int) {
	w.applyAddressUpdate(from, actualNonce, actualBalance)
	// TODO: Errorf in case readyTx.Hash == txHash
}

// DeleteTx delete the tx after it fails to execute
func (w *Worker) DeleteTx(txHash common.Hash, addr common.Address, actualFromNonce *uint64, actualFromBalance *big.Int) {
	w.workerMutex.Lock()
	defer w.workerMutex.Unlock()

	addrQueue, found := w.pool[addr.String()]

	// TODO: What happens if not found?
	if found {
		deletedReadyTx := addrQueue.deleteTx(txHash)
		if deletedReadyTx != nil {
			w.efficiencyList.delete(deletedReadyTx)
		}

		addrQueue.updateCurrentNonceBalance(actualFromNonce, actualFromBalance)
	}
}

// UpdateTx updates the ZKCounter of a tx and resort the tx in the efficiency list if needed
func (w *Worker) UpdateTx(txHash common.Hash, addr common.Address, counters state.ZKCounters) {
	w.workerMutex.Lock()
	defer w.workerMutex.Unlock()

	addrQueue, found := w.pool[addr.String()]

	// TODO: What happens if not found? log Errorf
	if found {
		readyTxUpdated := addrQueue.UpdateTxZKCounters(txHash, counters, w.batchConstraints, w.batchResourceWeights)

		// Resort updatedReadyTx in efficiencyList
		if readyTxUpdated != nil {
			w.efficiencyList.delete(readyTxUpdated)
			w.efficiencyList.add(readyTxUpdated)
		}
	}
}

// GetBestFittingTx gets the most efficient tx that fits in the available batch resources
func (w *Worker) GetBestFittingTx(resources batchResources) *TxTracker {
	w.workerMutex.Lock()
	defer w.workerMutex.Unlock()

	var (
		tx         *TxTracker
		foundMutex sync.RWMutex
	)

	nGoRoutines := runtime.NumCPU()
	foundAt := -1

	wg := sync.WaitGroup{}
	wg.Add(nGoRoutines)

	// Each go routine looks for a fitting tx
	for i := 0; i < nGoRoutines; i++ {
		go func(n int) {
			defer wg.Done()
			for i := n; i < w.efficiencyList.len(); i += nGoRoutines {
				foundMutex.RLock()
				if foundAt != -1 && i > foundAt {
					foundMutex.RUnlock()
					return
				}
				foundMutex.RUnlock()

				txCandidate := w.efficiencyList.getByIndex(i)
				error := resources.sub(*&txCandidate.BatchResources)
				if error != nil {
					// We don't add this Tx
					continue
				}

				foundMutex.Lock()
				if foundAt == -1 || foundAt > i {
					foundAt = i
					tx = txCandidate
				}
				foundMutex.Unlock()

				return
			}
		}(i)
	}
	wg.Wait()

	return tx
}

// HandleL2Reorg handles the L2 reorg signal
func (w *Worker) HandleL2Reorg(txHashes []common.Hash) {
	// 1. Delete related txs from w.efficiencyList
	// 2. Mark the affected addresses as "reorged" in w.Pool
	// 3. Update these addresses (go to MT, update nonce and balance into w.Pool)
}