package data

import (
	"fmt"

	"mit.edu/dsg/godb/catalog"
	"mit.edu/dsg/godb/common"
	"mit.edu/dsg/godb/storage"
)

// Transaction represents a single payment transaction.
type Transaction struct {
	TxnID       int64
	MerchantID  string // ≤32 chars (GoDB StringType limit)
	AmountPaise int64  // ₹100 = 10000 paise
	Timestamp   int64  // unix seconds
	Method      string // "upi", "card", "netbanking"
	Category    string // "grocery", "electronics", "food"
}

// FraudLabel marks whether a transaction is fraudulent and what type.
type FraudLabel struct {
	TxnID     int64
	IsFraud   bool
	FraudType string // "card_testing", "amount_spike", "velocity_spike", "gradual_creep", ""
}

// Column indices in the GoDB table — must match TableColumns() order.
const (
	ColTxnID       = 0
	ColMerchantID  = 1
	ColAmountPaise = 2
	ColTimestamp    = 3
	ColMethod      = 4
	ColCategory    = 5
	NumColumns     = 6
)

// TableName is the GoDB table name for transactions.
const TableName = "transactions"

// TableColumns returns the catalog column definitions.
func TableColumns() []catalog.Column {
	return []catalog.Column{
		{Name: "txn_id", Type: common.IntType},
		{Name: "merchant_id", Type: common.StringType},
		{Name: "amount_paise", Type: common.IntType},
		{Name: "timestamp", Type: common.IntType},
		{Name: "method", Type: common.StringType},
		{Name: "category", Type: common.StringType},
	}
}

// FieldTypes returns the GoDB types for RawTupleDesc construction.
func FieldTypes() []common.Type {
	return []common.Type{
		common.IntType,    // txn_id
		common.StringType, // merchant_id
		common.IntType,    // amount_paise
		common.IntType,    // timestamp
		common.StringType, // method
		common.StringType, // category
	}
}

// Serialize writes a Transaction into a pre-allocated RawTuple buffer.
func (t *Transaction) Serialize(desc *storage.RawTupleDesc, buf storage.RawTuple) {
	desc.SetValue(buf, ColTxnID, common.NewIntValue(t.TxnID))
	desc.SetValue(buf, ColMerchantID, common.NewStringValue(t.MerchantID))
	desc.SetValue(buf, ColAmountPaise, common.NewIntValue(t.AmountPaise))
	desc.SetValue(buf, ColTimestamp, common.NewIntValue(t.Timestamp))
	desc.SetValue(buf, ColMethod, common.NewStringValue(t.Method))
	desc.SetValue(buf, ColCategory, common.NewStringValue(t.Category))
}

// DeserializeTxn reads a Transaction from a RawTuple.
// The caller must hold a read lock on the underlying page frame.
func DeserializeTxn(desc *storage.RawTupleDesc, raw storage.RawTuple) Transaction {
	return Transaction{
		TxnID:       desc.GetValue(raw, ColTxnID).IntValue(),
		MerchantID:  desc.GetValue(raw, ColMerchantID).StringValue(),
		AmountPaise: desc.GetValue(raw, ColAmountPaise).IntValue(),
		Timestamp:   desc.GetValue(raw, ColTimestamp).IntValue(),
		Method:      desc.GetValue(raw, ColMethod).StringValue(),
		Category:    desc.GetValue(raw, ColCategory).StringValue(),
	}
}

// Each tuple: 3 ints (8 bytes each) + 3 strings (32 bytes each) = 24 + 96 = 120 bytes
// Page size: 4096 bytes. After header (16) + 2 bitmaps, ~30 tuples per page.
// 50K tuples → ~1700 pages → ~6.6 MB on disk.
func init() {
	// Sanity check: ensure our column count matches.
	if len(FieldTypes()) != NumColumns {
		panic(fmt.Sprintf("schema mismatch: expected %d columns, got %d", NumColumns, len(FieldTypes())))
	}
}
