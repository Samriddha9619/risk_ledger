package ledger

import (
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/Samriddha9619/risk_ledger/data"
)

// generateTestCSV creates a temporary CSV with numTxns synthetic transactions.
func generateTestCSV(t *testing.T, numTxns int) string {
	t.Helper()
	cfg := data.GeneratorConfig{
		NumMerchants:  200,
		DurationHours: 72,
		FraudRate:     0.08,
		Seed:          42,
		OutputDir:     t.TempDir(),
	}
	total, _, err := data.Generate(cfg)
	if err != nil {
		t.Fatalf("generating test data: %v", err)
	}
	if total < numTxns {
		t.Logf("warning: generated %d txns, wanted %d", total, numTxns)
	}
	return cfg.OutputDir + "/transactions.csv"
}

// TestGoDBBufferPoolStress loads 50K+ transactions through a buffer pool with
// only 64 frames (vs. ~1700 pages needed). This forces heavy page eviction
// and verifies data integrity under memory pressure.
func TestGoDBBufferPoolStress(t *testing.T) {
	csvPath := generateTestCSV(t, 50000)

	poolSizes := []int{64, 128, 2000}
	for _, poolSize := range poolSizes {
		t.Run(fmt.Sprintf("pool_%d", poolSize), func(t *testing.T) {
			dbDir := t.TempDir()

			// Create store with constrained buffer pool
			store, err := NewTransactionStoreWithPoolSize(dbDir, true, poolSize)
			if err != nil {
				t.Fatalf("creating store with pool=%d: %v", poolSize, err)
			}

			// Load transactions
			start := time.Now()
			count, err := store.LoadFromCSV(csvPath)
			if err != nil {
				t.Fatalf("loading with pool=%d: %v", poolSize, err)
			}
			loadTime := time.Since(start)

			// Flush all dirty pages to disk
			flushStart := time.Now()
			if err := store.Flush(); err != nil {
				t.Fatalf("flushing with pool=%d: %v", poolSize, err)
			}
			flushTime := time.Since(flushStart)

			// Scan back and verify count matches
			scanStart := time.Now()
			txns, err := store.ScanAll()
			if err != nil {
				t.Fatalf("scanning with pool=%d: %v", poolSize, err)
			}
			scanTime := time.Since(scanStart)

			if len(txns) != count {
				t.Errorf("pool=%d: loaded %d but scanned back %d", poolSize, count, len(txns))
			}

			// Estimate pages needed: ~30 txns per page
			pagesNeeded := count / 30
			evictions := 0
			if pagesNeeded > poolSize {
				evictions = pagesNeeded - poolSize
			}

			t.Logf("Pool=%d frames | Loaded %d txns (~%d pages) | Est. evictions: ~%d",
				poolSize, count, pagesNeeded, evictions)
			t.Logf("  Load: %v | Flush: %v | Scan: %v | Total: %v",
				loadTime, flushTime, scanTime, loadTime+flushTime+scanTime)
		})
	}
}

// TestGoDBSmallPoolFlush verifies that after loading with a tiny pool,
// flushing, and re-opening, all data survives the round-trip.
func TestGoDBSmallPoolFlush(t *testing.T) {
	csvPath := generateTestCSV(t, 50000)
	dbDir := t.TempDir()

	// Phase 1: Load with a tiny pool and flush
	store, err := NewTransactionStoreWithPoolSize(dbDir, true, 64)
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}

	loadCount, err := store.LoadFromCSV(csvPath)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flushing: %v", err)
	}

	// Scan to verify before re-open
	txns1, err := store.ScanAll()
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if len(txns1) != loadCount {
		t.Errorf("first scan: got %d, loaded %d", len(txns1), loadCount)
	}

	// Phase 2: Re-open with a different pool size and verify data persists
	store2, err := NewTransactionStoreWithPoolSize(dbDir, false, 128)
	if err != nil {
		t.Fatalf("re-opening store: %v", err)
	}

	txns2, err := store2.ScanAll()
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if len(txns2) != loadCount {
		t.Errorf("second scan: got %d, loaded %d", len(txns2), loadCount)
	}

	// Spot-check a few transactions for field integrity
	if len(txns2) > 0 {
		rng := rand.New(rand.NewSource(42))
		for i := 0; i < 10 && i < len(txns2); i++ {
			idx := rng.Intn(len(txns2))
			txn := txns2[idx]
			if txn.TxnID == 0 {
				t.Errorf("txn[%d] has zero TxnID", idx)
			}
			if txn.MerchantID == "" {
				t.Errorf("txn[%d] has empty MerchantID", idx)
			}
			if txn.AmountPaise <= 0 {
				t.Errorf("txn[%d] has non-positive amount: %d", idx, txn.AmountPaise)
			}
		}
	}
}

// TestGoDBConcurrentReads verifies that multiple goroutines can scan
// the store concurrently without data races (run with -race).
func TestGoDBConcurrentReads(t *testing.T) {
	// Generate a small dataset for fast concurrent test
	cfg := data.GeneratorConfig{
		NumMerchants:  20,
		DurationHours: 12,
		FraudRate:     0.1,
		Seed:          42,
		OutputDir:     t.TempDir(),
	}
	_, _, err := data.Generate(cfg)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	dbDir := t.TempDir()
	store, err := NewTransactionStoreWithPoolSize(dbDir, true, 128)
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}

	count, err := store.LoadFromCSV(cfg.OutputDir + "/transactions.csv")
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flushing: %v", err)
	}

	// Concurrent scans
	const numReaders = 4
	errCh := make(chan error, numReaders)

	for g := 0; g < numReaders; g++ {
		go func() {
			txns, err := store.ScanAll()
			if err != nil {
				errCh <- fmt.Errorf("scan error: %w", err)
				return
			}
			if len(txns) != count {
				errCh <- fmt.Errorf("expected %d txns, got %d", count, len(txns))
				return
			}
			errCh <- nil
		}()
	}

	for i := 0; i < numReaders; i++ {
		if err := <-errCh; err != nil {
			t.Error(err)
		}
	}
}

// cleanupDir removes a directory if it exists (for use in deferred cleanup).
func cleanupDir(path string) {
	_ = os.RemoveAll(path)
}
