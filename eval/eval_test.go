package eval

import (
	"testing"

	"github.com/Samriddha9619/risk_ledger/data"
	"github.com/Samriddha9619/risk_ledger/detector"
)

// TestEvaluateWithAmountsBasic verifies confusion matrix math with known inputs.
func TestEvaluateWithAmountsBasic(t *testing.T) {
	// 4 alerts: 2 fraud, 2 legit. Threshold = 0.5
	alerts := []detector.Alert{
		{TxnID: 1, CombinedScore: 0.9},  // predicted fraud
		{TxnID: 2, CombinedScore: 0.8},  // predicted fraud
		{TxnID: 3, CombinedScore: 0.2},  // predicted legit
		{TxnID: 4, CombinedScore: 0.1},  // predicted legit
	}

	labels := map[int64]data.FraudLabel{
		1: {TxnID: 1, IsFraud: true},   // TP
		2: {TxnID: 2, IsFraud: false},  // FP
		3: {TxnID: 3, IsFraud: true},   // FN
		4: {TxnID: 4, IsFraud: false},  // TN
	}

	amounts := map[int64]int64{
		1: 100000,  // ₹1000
		2: 50000,   // ₹500
		3: 200000,  // ₹2000
		4: 30000,   // ₹300
	}

	result := EvaluateWithAmounts(alerts, labels, amounts, 0.5)

	if result.TP != 1 {
		t.Errorf("TP = %d, want 1", result.TP)
	}
	if result.FP != 1 {
		t.Errorf("FP = %d, want 1", result.FP)
	}
	if result.FN != 1 {
		t.Errorf("FN = %d, want 1", result.FN)
	}
	if result.TN != 1 {
		t.Errorf("TN = %d, want 1", result.TN)
	}

	// Precision = TP/(TP+FP) = 1/2 = 0.5
	if result.Precision != 0.5 {
		t.Errorf("Precision = %.4f, want 0.5", result.Precision)
	}
	// Recall = TP/(TP+FN) = 1/2 = 0.5
	if result.Recall != 0.5 {
		t.Errorf("Recall = %.4f, want 0.5", result.Recall)
	}
	// F1 = 2*0.5*0.5/(0.5+0.5) = 0.5
	if result.F1 != 0.5 {
		t.Errorf("F1 = %.4f, want 0.5", result.F1)
	}

	// FP cost = 50000 * 0.02 = 1000 paise
	if result.FPCost != 1000.0 {
		t.Errorf("FPCost = %.2f, want 1000.0", result.FPCost)
	}
	// FN cost = 200000 paise (full fraud amount)
	if result.FNCost != 200000.0 {
		t.Errorf("FNCost = %.2f, want 200000.0", result.FNCost)
	}
}

// TestSweepThresholdsMonotonic verifies that recall decreases as threshold increases.
func TestSweepThresholdsMonotonic(t *testing.T) {
	var alerts []detector.Alert
	labels := make(map[int64]data.FraudLabel)
	amounts := make(map[int64]int64)

	// Create 100 alerts with linearly spaced scores, half fraud
	for i := 0; i < 100; i++ {
		alerts = append(alerts, detector.Alert{
			TxnID:         int64(i),
			CombinedScore: float64(i) / 100.0,
		})
		labels[int64(i)] = data.FraudLabel{
			TxnID:   int64(i),
			IsFraud: i >= 50, // top half is fraud
		}
		amounts[int64(i)] = 10000
	}

	results := SweepThresholds(alerts, labels, amounts, 20)

	// Recall should be non-increasing as threshold goes up
	for i := 1; i < len(results); i++ {
		if results[i].Recall > results[i-1].Recall+0.001 { // small tolerance for edge cases
			t.Errorf("recall increased from %.4f to %.4f between thresholds %.2f and %.2f",
				results[i-1].Recall, results[i].Recall, results[i-1].Threshold, results[i].Threshold)
		}
	}
}

// TestFindBestF1 verifies the best F1 selection.
func TestFindBestF1(t *testing.T) {
	results := []EvalResult{
		{Threshold: 0.1, F1: 0.3},
		{Threshold: 0.5, F1: 0.8},
		{Threshold: 0.9, F1: 0.2},
	}

	best := FindBestF1(results)
	if best.Threshold != 0.5 {
		t.Errorf("best threshold = %.2f, want 0.5", best.Threshold)
	}
	if best.F1 != 0.8 {
		t.Errorf("best F1 = %.4f, want 0.8", best.F1)
	}
}

// TestFindOptimalThreshold verifies cost minimization.
func TestFindOptimalThreshold(t *testing.T) {
	results := []EvalResult{
		{Threshold: 0.1, TotalCost: 50000},
		{Threshold: 0.5, TotalCost: 10000}, // lowest cost
		{Threshold: 0.9, TotalCost: 30000},
	}

	optimal := FindOptimalThreshold(results)
	if optimal.Threshold != 0.5 {
		t.Errorf("optimal threshold = %.2f, want 0.5", optimal.Threshold)
	}
}

// TestPerPatternRecall verifies per-fraud-type breakdown.
func TestPerPatternRecall(t *testing.T) {
	alerts := []detector.Alert{
		{TxnID: 1, CombinedScore: 0.9}, // caught
		{TxnID: 2, CombinedScore: 0.8}, // caught
		{TxnID: 3, CombinedScore: 0.2}, // missed
		{TxnID: 4, CombinedScore: 0.7}, // caught
	}

	labels := map[int64]data.FraudLabel{
		1: {TxnID: 1, IsFraud: true, FraudType: "card_testing"},
		2: {TxnID: 2, IsFraud: true, FraudType: "card_testing"},
		3: {TxnID: 3, IsFraud: true, FraudType: "amount_spike"},
		4: {TxnID: 4, IsFraud: true, FraudType: "amount_spike"},
	}

	results := EvaluatePerPattern(alerts, labels, 0.5)

	patternMap := make(map[string]PerPatternResult)
	for _, r := range results {
		patternMap[r.Pattern] = r
	}

	ct := patternMap["card_testing"]
	if ct.Caught != 2 || ct.Total != 2 {
		t.Errorf("card_testing: caught=%d total=%d, want 2/2", ct.Caught, ct.Total)
	}

	as := patternMap["amount_spike"]
	if as.Caught != 1 || as.Total != 2 {
		t.Errorf("amount_spike: caught=%d total=%d, want 1/2", as.Caught, as.Total)
	}
}

// TestAggregateMultiSeed verifies mean and std computation.
func TestAggregateMultiSeed(t *testing.T) {
	seeds := []int64{42, 123, 456}
	results := []EvalResult{
		{F1: 0.5, Precision: 0.6, Recall: 0.4},
		{F1: 0.6, Precision: 0.7, Recall: 0.5},
		{F1: 0.7, Precision: 0.8, Recall: 0.6},
	}

	ms := AggregateMultiSeed(seeds, results)

	// Mean F1 = (0.5+0.6+0.7)/3 = 0.6
	if ms.MeanF1 < 0.599 || ms.MeanF1 > 0.601 {
		t.Errorf("MeanF1 = %.4f, want ≈ 0.6", ms.MeanF1)
	}

	// Std F1 = sqrt(((0.1^2 + 0 + 0.1^2) / 3)) ≈ 0.0816
	if ms.StdF1 < 0.05 || ms.StdF1 > 0.10 {
		t.Errorf("StdF1 = %.4f, want ≈ 0.08", ms.StdF1)
	}

	if len(ms.Seeds) != 3 {
		t.Errorf("Seeds count = %d, want 3", len(ms.Seeds))
	}
}

// TestAggregateMultiSeedEmpty verifies empty input doesn't panic.
func TestAggregateMultiSeedEmpty(t *testing.T) {
	ms := AggregateMultiSeed(nil, nil)
	if ms.MeanF1 != 0 || ms.StdF1 != 0 {
		t.Errorf("empty aggregate should return zeros, got mean=%.4f std=%.4f", ms.MeanF1, ms.StdF1)
	}
}
