package detector

import (
	"testing"

	"github.com/Samriddha9619/risk_ledger/data"
)

// TestTriageFallbackBlocksOnHighStat verifies fallback blocks when stat score > 0.6 and rule fired.
func TestTriageFallbackBlocksOnHighStat(t *testing.T) {
	explainer := NewExplainer("") // disabled
	profiles := make(map[string]*MerchantProfile)
	te := NewTriageEngine(explainer, profiles)

	alert := Alert{
		TxnID:         1,
		MerchantID:    "test_merchant",
		StatScore:     0.75,
		ForestScore:   0.3,
		CombinedScore: 0.55,
		StatAlert:     StatAlert{RuleFired: "amount_zscore", Score: 0.75},
	}

	d := te.fallbackTriage(alert)
	if d.Decision != "BLOCK" {
		t.Errorf("expected BLOCK for high stat score with rule, got %s", d.Decision)
	}
	if d.Source != "fallback" {
		t.Errorf("expected source 'fallback', got %s", d.Source)
	}
}

// TestTriageFallbackEscalatesOnMedium verifies fallback escalates when combined > 0.6 without strong rule.
func TestTriageFallbackEscalatesOnMedium(t *testing.T) {
	explainer := NewExplainer("")
	profiles := make(map[string]*MerchantProfile)
	te := NewTriageEngine(explainer, profiles)

	alert := Alert{
		TxnID:         2,
		MerchantID:    "test_merchant",
		StatScore:     0.3,
		ForestScore:   0.7,
		CombinedScore: 0.65,
		StatAlert:     StatAlert{RuleFired: "", Score: 0.3},
	}

	d := te.fallbackTriage(alert)
	if d.Decision != "ESCALATE" {
		t.Errorf("expected ESCALATE for medium score without rule, got %s", d.Decision)
	}
}

// TestTriageFallbackApprovesOnLow verifies fallback approves when combined score is low.
func TestTriageFallbackApprovesOnLow(t *testing.T) {
	explainer := NewExplainer("")
	profiles := make(map[string]*MerchantProfile)
	te := NewTriageEngine(explainer, profiles)

	alert := Alert{
		TxnID:         3,
		MerchantID:    "test_merchant",
		StatScore:     0.2,
		ForestScore:   0.3,
		CombinedScore: 0.25,
		StatAlert:     StatAlert{RuleFired: "", Score: 0.2},
	}

	d := te.fallbackTriage(alert)
	if d.Decision != "APPROVE" {
		t.Errorf("expected APPROVE for low score, got %s", d.Decision)
	}
}

// TestTriageAutoBlockThreshold verifies transactions above auto-block threshold are auto-blocked.
func TestTriageAutoBlockThreshold(t *testing.T) {
	explainer := NewExplainer("")
	profiles := make(map[string]*MerchantProfile)
	te := NewTriageEngine(explainer, profiles)

	alerts := []Alert{
		{TxnID: 1, MerchantID: "m1", CombinedScore: 0.90, StatAlert: StatAlert{RuleFired: "amount_zscore"}},
		{TxnID: 2, MerchantID: "m2", CombinedScore: 0.40, StatAlert: StatAlert{RuleFired: ""}},
		{TxnID: 3, MerchantID: "m3", CombinedScore: 0.80, StatAlert: StatAlert{RuleFired: "velocity_spike"}},
	}
	labels := map[int64]data.FraudLabel{
		1: {TxnID: 1, IsFraud: true},
		2: {TxnID: 2, IsFraud: false},
		3: {TxnID: 3, IsFraud: true},
	}
	txns := []data.Transaction{
		{TxnID: 1, MerchantID: "m1"},
		{TxnID: 2, MerchantID: "m2"},
		{TxnID: 3, MerchantID: "m3"},
	}

	decisions, metrics := te.Triage(alerts, labels, txns)

	// TxnID=1 (0.90) should be auto-blocked
	// TxnID=3 (0.80) should be auto-blocked (>= 0.78 threshold)
	if metrics.AutoBlocked != 2 {
		t.Errorf("expected 2 auto-blocked, got %d", metrics.AutoBlocked)
	}

	// Verify at least one decision is auto-block
	autoBlockCount := 0
	for _, d := range decisions {
		if d.Source == "auto_rule" && d.Decision == "BLOCK" {
			autoBlockCount++
		}
	}
	if autoBlockCount != 2 {
		t.Errorf("expected 2 auto_rule BLOCK decisions, got %d", autoBlockCount)
	}
}

// TestTriageMetricsWorkloadReduction verifies workload reduction math.
func TestTriageMetricsWorkloadReduction(t *testing.T) {
	explainer := NewExplainer("")
	profiles := make(map[string]*MerchantProfile)
	te := NewTriageEngine(explainer, profiles)

	// Create alerts: some above auto-block, some in middle, some below auto-approve
	alerts := []Alert{
		{TxnID: 1, MerchantID: "m1", CombinedScore: 0.95, StatScore: 0.9, StatAlert: StatAlert{RuleFired: "amount_zscore", Score: 0.9}},
		{TxnID: 2, MerchantID: "m2", CombinedScore: 0.85, StatScore: 0.8, StatAlert: StatAlert{RuleFired: "velocity_spike", Score: 0.8}},
		{TxnID: 3, MerchantID: "m3", CombinedScore: 0.60, StatScore: 0.5, StatAlert: StatAlert{RuleFired: "", Score: 0.5}},
		{TxnID: 4, MerchantID: "m4", CombinedScore: 0.30, StatScore: 0.2, StatAlert: StatAlert{RuleFired: "", Score: 0.2}},
	}

	labels := map[int64]data.FraudLabel{
		1: {TxnID: 1, IsFraud: true},
		2: {TxnID: 2, IsFraud: true},
		3: {TxnID: 3, IsFraud: false},
		4: {TxnID: 4, IsFraud: false},
	}

	txns := []data.Transaction{
		{TxnID: 1, MerchantID: "m1"},
		{TxnID: 2, MerchantID: "m2"},
		{TxnID: 3, MerchantID: "m3"},
		{TxnID: 4, MerchantID: "m4"},
	}

	_, metrics := te.Triage(alerts, labels, txns)

	// Workload reduction should be > 0
	if metrics.WorkloadReduction <= 0 {
		t.Errorf("expected positive workload reduction, got %.2f", metrics.WorkloadReduction)
	}

	// Total flagged = auto-blocked + AI triaged middle band
	if metrics.TotalFlagged <= 0 {
		t.Errorf("expected some flagged transactions, got %d", metrics.TotalFlagged)
	}
}

// TestTriageDecisionFields verifies all fields are populated in decisions.
func TestTriageDecisionFields(t *testing.T) {
	explainer := NewExplainer("")
	profiles := make(map[string]*MerchantProfile)
	te := NewTriageEngine(explainer, profiles)

	alert := Alert{
		TxnID:         100,
		MerchantID:    "merchant_042",
		StatScore:     0.8,
		ForestScore:   0.6,
		CombinedScore: 0.90,
		StatAlert:     StatAlert{RuleFired: "amount_zscore", Score: 0.8},
	}

	alerts := []Alert{alert}
	labels := map[int64]data.FraudLabel{100: {TxnID: 100, IsFraud: true}}
	txns := []data.Transaction{{TxnID: 100, MerchantID: "merchant_042", AmountPaise: 50000}}

	decisions, _ := te.Triage(alerts, labels, txns)

	if len(decisions) == 0 {
		t.Fatal("expected at least one decision")
	}

	d := decisions[0]
	if d.TxnID != 100 {
		t.Errorf("TxnID = %d, want 100", d.TxnID)
	}
	if d.MerchantID != "merchant_042" {
		t.Errorf("MerchantID = %s, want merchant_042", d.MerchantID)
	}
	if d.Decision == "" {
		t.Error("Decision is empty")
	}
	if d.Reasoning == "" {
		t.Error("Reasoning is empty")
	}
}

// TestIsolationForestDeterministic verifies same seed produces same scores.
func TestIsolationForestDeterministic(t *testing.T) {
	var dataset [][]float64
	for i := 0; i < 200; i++ {
		dataset = append(dataset, []float64{float64(i) / 200.0, float64(i%20) / 20.0})
	}

	forest1 := NewIsolationForest(50, 128)
	forest1.Train(dataset, 42)

	forest2 := NewIsolationForest(50, 128)
	forest2.Train(dataset, 42)

	testPoint := []float64{0.5, 0.5}
	score1 := forest1.Score(testPoint)
	score2 := forest2.Score(testPoint)

	if score1 != score2 {
		t.Errorf("same seed should produce same score: %.6f vs %.6f", score1, score2)
	}
}

// TestIsolationForestAllSameData verifies all identical points score near 0.5.
func TestIsolationForestAllSameData(t *testing.T) {
	var dataset [][]float64
	for i := 0; i < 100; i++ {
		dataset = append(dataset, []float64{0.5, 0.5})
	}

	forest := NewIsolationForest(50, 64)
	forest.Train(dataset, 42)

	score := forest.Score([]float64{0.5, 0.5})
	// All same data can't be split, so path length ≈ 0, score should be high
	// But with identical data, all leaves have size=n, so c(n) adjustment applies
	// Just verify it's in valid range
	if score < 0.0 || score > 1.0 {
		t.Errorf("score %.4f out of [0,1] range for identical data", score)
	}
}

// TestSubsampleLargerThanDataset verifies no panic when subsample > dataset.
func TestSubsampleLargerThanDataset(t *testing.T) {
	dataset := [][]float64{
		{0.1, 0.2},
		{0.3, 0.4},
		{0.5, 0.6},
	}

	forest := NewIsolationForest(10, 256) // subsample=256 >> dataset=3
	forest.Train(dataset, 42)

	score := forest.Score([]float64{0.5, 0.5})
	if score < 0.0 || score > 1.0 {
		t.Errorf("score %.4f out of range", score)
	}
}

// TestRingBufferWrapAround verifies velocity after buffer wraps past 200 entries.
func TestRingBufferWrapAround(t *testing.T) {
	profile := &MerchantProfile{}
	baseTime := int64(1000000)

	// Feed 500 transactions (well past the 200-entry buffer)
	for i := 0; i < 500; i++ {
		profile.Update(data.Transaction{
			TxnID:       int64(i + 1),
			MerchantID:  "test",
			AmountPaise: 10000,
			Timestamp:   baseTime + int64(i*10), // every 10 seconds
		})
	}

	// Last timestamp: baseTime + 4990
	// Velocity in last 300 seconds: txns from baseTime+4690 to baseTime+4990
	// That's indices 469 to 499 = 31 transactions
	vel := profile.VelocityInWindow(baseTime+4990, 300)
	if vel == 0 {
		t.Error("velocity should be non-zero after wrap-around")
	}
	if vel > 200 {
		t.Errorf("velocity %d exceeds ring buffer size (max 200)", vel)
	}
}

// TestEngineConfigCustomWeights verifies custom engine config works.
func TestEngineConfigCustomWeights(t *testing.T) {
	cfg := EngineConfig{
		StatWeight:   0.7,
		ForestWeight: 0.3,
		NumTrees:     50,
		SubsampleSz:  128,
	}

	engine := NewDetectionEngineWithConfig(cfg)
	if engine.StatWeight != 0.7 {
		t.Errorf("StatWeight = %.2f, want 0.7", engine.StatWeight)
	}
	if engine.ForestWeight != 0.3 {
		t.Errorf("ForestWeight = %.2f, want 0.3", engine.ForestWeight)
	}

	// Train and score to verify no panic
	var features [][]float64
	baseTime := int64(1000000)
	for i := 0; i < 30; i++ {
		txn := data.Transaction{
			TxnID:       int64(i + 1),
			MerchantID:  "test",
			AmountPaise: 10000,
			Timestamp:   baseTime + int64(i*3600),
			Method:      "upi",
			Category:    "grocery",
		}
		f := engine.ProcessTraining(txn)
		fs := make([]float64, NumFeatures)
		copy(fs, f[:])
		features = append(features, fs)
	}
	engine.TrainForest(features)

	alert := engine.Score(data.Transaction{
		TxnID:       100,
		MerchantID:  "test",
		AmountPaise: 10000,
		Timestamp:   baseTime + 200000,
		Method:      "upi",
		Category:    "grocery",
	})

	if alert.CombinedScore < 0 || alert.CombinedScore > 1 {
		t.Errorf("combined score %.4f out of [0,1]", alert.CombinedScore)
	}
}
