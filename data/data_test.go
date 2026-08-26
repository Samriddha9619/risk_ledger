package data

import (
	"math"
	"math/rand"
	"testing"

	"mit.edu/dsg/godb/storage"
)

// TestSchemaRoundTrip verifies serialize → deserialize produces the original transaction.
func TestSchemaRoundTrip(t *testing.T) {
	desc := storage.NewRawTupleDesc(FieldTypes())
	buf := make(storage.RawTuple, desc.BytesPerTuple())

	original := Transaction{
		TxnID:       42,
		MerchantID:  "merchant_007",
		AmountPaise: 123456,
		Timestamp:   1785346200,
		Method:      "upi",
		Category:    "grocery",
	}

	original.Serialize(desc, buf)
	restored := DeserializeTxn(desc, buf)

	if restored.TxnID != original.TxnID {
		t.Errorf("TxnID = %d, want %d", restored.TxnID, original.TxnID)
	}
	if restored.MerchantID != original.MerchantID {
		t.Errorf("MerchantID = %q, want %q", restored.MerchantID, original.MerchantID)
	}
	if restored.AmountPaise != original.AmountPaise {
		t.Errorf("AmountPaise = %d, want %d", restored.AmountPaise, original.AmountPaise)
	}
	if restored.Timestamp != original.Timestamp {
		t.Errorf("Timestamp = %d, want %d", restored.Timestamp, original.Timestamp)
	}
	if restored.Method != original.Method {
		t.Errorf("Method = %q, want %q", restored.Method, original.Method)
	}
	if restored.Category != original.Category {
		t.Errorf("Category = %q, want %q", restored.Category, original.Category)
	}
}

// TestSchemaMultipleTuples verifies that multiple tuples can be serialized into separate buffers
// without cross-contamination.
func TestSchemaMultipleTuples(t *testing.T) {
	desc := storage.NewRawTupleDesc(FieldTypes())

	txns := []Transaction{
		{1, "merchant_001", 10000, 1000000, "upi", "grocery"},
		{2, "merchant_002", 99999, 2000000, "card", "electronics"},
		{3, "merchant_003", 500, 3000000, "netbanking", "food"},
	}

	bufs := make([]storage.RawTuple, len(txns))
	for i, txn := range txns {
		bufs[i] = make(storage.RawTuple, desc.BytesPerTuple())
		txn.Serialize(desc, bufs[i])
	}

	for i, txn := range txns {
		restored := DeserializeTxn(desc, bufs[i])
		if restored.TxnID != txn.TxnID {
			t.Errorf("tuple %d: TxnID = %d, want %d", i, restored.TxnID, txn.TxnID)
		}
		if restored.AmountPaise != txn.AmountPaise {
			t.Errorf("tuple %d: AmountPaise = %d, want %d", i, restored.AmountPaise, txn.AmountPaise)
		}
	}
}

// TestSampleLogNormal verifies the distribution produces positive values with sane mean.
func TestSampleLogNormal(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	mean := 40000.0 // ₹400
	std := 20000.0

	sum := 0.0
	n := 10000
	for i := 0; i < n; i++ {
		val := sampleLogNormal(rng, mean, std)
		if val <= 0 {
			t.Fatalf("sampleLogNormal produced non-positive value: %d", val)
		}
		sum += float64(val)
	}

	sampleMean := sum / float64(n)
	// The sample mean should be within 20% of the target mean
	if math.Abs(sampleMean-mean)/mean > 0.20 {
		t.Errorf("sample mean = %.0f, target mean = %.0f (>20%% deviation)", sampleMean, mean)
	}
}

// TestSampleLogNormalEdgeCases verifies guards against degenerate inputs.
func TestSampleLogNormalEdgeCases(t *testing.T) {
	rng := rand.New(rand.NewSource(42))

	// Zero mean
	val := sampleLogNormal(rng, 0, 1000)
	if val != 100 {
		t.Errorf("sampleLogNormal(0, 1000) = %d, want 100 (minimum)", val)
	}

	// Negative mean
	val = sampleLogNormal(rng, -500, 1000)
	if val != 100 {
		t.Errorf("sampleLogNormal(-500, 1000) = %d, want 100 (minimum)", val)
	}
}

// TestGeneratorDeterministic verifies that the same seed produces the same output.
func TestGeneratorDeterministic(t *testing.T) {
	cfg := GeneratorConfig{
		NumMerchants:  10,
		DurationHours: 12,
		FraudRate:     0.1,
		Seed:          42,
		OutputDir:     t.TempDir(),
	}

	total1, fraud1, err := Generate(cfg)
	if err != nil {
		t.Fatalf("first generate: %v", err)
	}

	cfg.OutputDir = t.TempDir()
	total2, fraud2, err := Generate(cfg)
	if err != nil {
		t.Fatalf("second generate: %v", err)
	}

	if total1 != total2 {
		t.Errorf("total txns differ: %d vs %d", total1, total2)
	}
	if fraud1 != fraud2 {
		t.Errorf("fraud txns differ: %d vs %d", fraud1, fraud2)
	}
}
