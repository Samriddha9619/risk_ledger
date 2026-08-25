package ledger

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"

	"github.com/Samriddha9619/risk_ledger/data"
	"mit.edu/dsg/godb/catalog"
	"mit.edu/dsg/godb/execution"
	"mit.edu/dsg/godb/storage"
	"mit.edu/dsg/godb/transaction"
)

// TransactionStore wraps GoDB's storage layer for transaction data.
type TransactionStore struct {
	catalog     *catalog.Catalog
	bufferPool  *storage.BufferPool
	tableHeap   *execution.TableHeap
	desc        *storage.RawTupleDesc
	lockManager *transaction.LockManager
}

// NewTransactionStore initializes the GoDB storage engine and creates/opens the transactions table.
func NewTransactionStore(dataDir string, truncate bool) (*TransactionStore, error) {
	if truncate {
		_ = os.RemoveAll(dataDir)
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}

	// Set up catalog (in-memory for simplicity)
	cat, err := catalog.NewCatalog(catalog.NullPersistenceProvider{})
	if err != nil {
		return nil, fmt.Errorf("creating catalog: %w", err)
	}

	// Register the transactions table
	table, err := cat.AddTable(data.TableName, data.TableColumns())
	if err != nil {
		return nil, fmt.Errorf("adding table: %w", err)
	}

	// Initialize storage components
	logManager := &storage.NoopLogManager{}
	storageManager := storage.NewDiskStorageManager(dataDir)
	bufferPool := storage.NewBufferPool(2000, storageManager, logManager)
	lockManager := transaction.NewLockManager()

	// Create table heap
	tableHeap, err := execution.NewTableHeap(table, bufferPool, logManager, lockManager)
	if err != nil {
		return nil, fmt.Errorf("creating table heap: %w", err)
	}

	desc := storage.NewRawTupleDesc(data.FieldTypes())

	return &TransactionStore{
		catalog:     cat,
		bufferPool:  bufferPool,
		tableHeap:   tableHeap,
		desc:        desc,
		lockManager: lockManager,
	}, nil
}

// Insert stores a single transaction in GoDB.
func (s *TransactionStore) Insert(txn data.Transaction) error {
	buf := make(storage.RawTuple, s.desc.BytesPerTuple())
	txn.Serialize(s.desc, buf)
	_, err := s.tableHeap.InsertTuple(nil, buf)
	return err
}

// LoadFromCSV reads a transactions CSV and bulk-inserts into GoDB.
func (s *TransactionStore) LoadFromCSV(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	records, err := reader.ReadAll()
	if err != nil {
		return 0, fmt.Errorf("reading CSV: %w", err)
	}

	buf := make(storage.RawTuple, s.desc.BytesPerTuple())
	count := 0

	for _, row := range records {
		if len(row) < data.NumColumns {
			continue
		}
		txnID, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil {
			return count, fmt.Errorf("parsing txn_id %q: %w", row[0], err)
		}
		amountPaise, err := strconv.ParseInt(row[2], 10, 64)
		if err != nil {
			return count, fmt.Errorf("parsing amount %q: %w", row[2], err)
		}
		timestamp, err := strconv.ParseInt(row[3], 10, 64)
		if err != nil {
			return count, fmt.Errorf("parsing timestamp %q: %w", row[3], err)
		}

		txn := data.Transaction{
			TxnID:       txnID,
			MerchantID:  row[1],
			AmountPaise: amountPaise,
			Timestamp:   timestamp,
			Method:      row[4],
			Category:    row[5],
		}

		// Zero the buffer before each use to prevent stale data
		clear(buf)
		txn.Serialize(s.desc, buf)
		if _, err := s.tableHeap.InsertTuple(nil, buf); err != nil {
			return count, fmt.Errorf("inserting txn %d: %w", txnID, err)
		}
		count++
	}

	return count, nil
}

// ScanAll reads all transactions from GoDB into memory.
// This loads everything into a slice — acceptable for 50K transactions.
func (s *TransactionStore) ScanAll() ([]data.Transaction, error) {
	buf := make([]byte, s.desc.BytesPerTuple())
	iter, err := s.tableHeap.Iterator(nil, transaction.LockModeS, buf)
	if err != nil {
		return nil, fmt.Errorf("creating iterator: %w", err)
	}
	defer iter.Close()

	var txns []data.Transaction
	for iter.Next() {
		raw := iter.CurrentTuple()
		// Copy the raw tuple since the underlying page might be reused
		rawCopy := make(storage.RawTuple, len(raw))
		copy(rawCopy, raw)
		txn := data.DeserializeTxn(s.desc, rawCopy)
		txns = append(txns, txn)
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("scanning: %w", err)
	}

	return txns, nil
}

// Flush ensures all dirty pages are written to disk.
func (s *TransactionStore) Flush() error {
	return s.bufferPool.FlushAllPages()
}

// LoadLabelsFromCSV reads the labels CSV into a map of txn_id → FraudLabel.
func LoadLabelsFromCSV(path string) (map[int64]data.FraudLabel, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}

	labels := make(map[int64]data.FraudLabel, len(records))
	for _, row := range records {
		if len(row) < 3 {
			continue
		}
		txnID, _ := strconv.ParseInt(row[0], 10, 64)
		isFraud := row[1] == "1"
		labels[txnID] = data.FraudLabel{
			TxnID:     txnID,
			IsFraud:   isFraud,
			FraudType: row[2],
		}
	}
	return labels, nil
}
