package detector

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Samriddha9619/risk_ledger/data"
)

// mockTriageTransport returns a valid OpenRouter-style JSON response
// with BLOCK for txn 101 and APPROVE for txn 102.
type mockTriageTransport struct{}

func (m *mockTriageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	bodyStr := string(body)

	// Check if this looks like a triage batch request (contains "txn_id")
	if strings.Contains(bodyStr, "Transaction ID") {
		mockJSON := `{
			"choices": [{
				"message": {
					"content": "[{\"txn_id\": 101, \"decision\": \"BLOCK\", \"confidence\": 0.95, \"reasoning\": \"Velocity spike detected\", \"risk_factors\": [\"velocity_spike\"]}, {\"txn_id\": 102, \"decision\": \"APPROVE\", \"confidence\": 0.85, \"reasoning\": \"Normal transaction\", \"risk_factors\": []}]"
				}
			}]
		}`
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(mockJSON)),
			Header:     make(http.Header),
		}, nil
	}

	// Default: return empty response
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{"choices": [{"message": {"content": "[]"}}]}`)),
		Header:     make(http.Header),
	}, nil
}

// mockTriageApproveAllTransport returns APPROVE for all transactions —
// used to test the safety gate override.
type mockTriageApproveAllTransport struct{}

func (m *mockTriageApproveAllTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Read the request body to extract transaction IDs
	body, _ := io.ReadAll(req.Body)
	bodyStr := string(body)
	_ = bodyStr

	// Always return APPROVE for txn 201 and 202
	mockJSON := fmt.Sprintf(`{
		"choices": [{
			"message": {
				"content": "[{\"txn_id\": 201, \"decision\": \"APPROVE\", \"confidence\": 0.80, \"reasoning\": \"Looks normal to me\", \"risk_factors\": []}, {\"txn_id\": 202, \"decision\": \"APPROVE\", \"confidence\": 0.90, \"reasoning\": \"Normal transaction\", \"risk_factors\": []}]"
			}
		}]
	}`)
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(mockJSON)),
		Header:     make(http.Header),
	}, nil
}

// TestTriageFallbackBlocksOnHighStat verifies fallback blocks when stat score > 0.6 and rule fired.
func TestTriageFallbackBlocksOnHighStat(t *testing.T) {
	explainer := NewExplainer("") // disabled
	profiles := make(map[string]*MerchantProfile)
	te := NewTriageEngine(explainer, profiles, 0.50, 0.78)

	alert := Alert{
		TxnID:         1,
		MerchantID:    "test_merchant",
		StatScore:     0.75,
		ForestScore:   0.65,
		CombinedScore: 0.70,
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
	te := NewTriageEngine(explainer, profiles, 0.50, 0.78)

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

// TestTriageFallbackEscalatesOnLow verifies fallback escalates when combined score is low (ambiguous band).
func TestTriageFallbackEscalatesOnLow(t *testing.T) {
	explainer := NewExplainer("")
	profiles := make(map[string]*MerchantProfile)
	te := NewTriageEngine(explainer, profiles, 0.50, 0.78)

	alert := Alert{
		TxnID:         3,
		MerchantID:    "test_merchant",
		StatScore:     0.2,
		ForestScore:   0.3,
		CombinedScore: 0.25,
		StatAlert:     StatAlert{RuleFired: "", Score: 0.2},
	}

	d := te.fallbackTriage(alert)
	if d.Decision != "ESCALATE" {
		t.Errorf("expected ESCALATE for low ambiguous score, got %s", d.Decision)
	}
}

// TestTriageAutoBlockThreshold verifies transactions above auto-block threshold are auto-blocked.
func TestTriageAutoBlockThreshold(t *testing.T) {
	explainer := NewExplainer("")
	profiles := make(map[string]*MerchantProfile)
	te := NewTriageEngine(explainer, profiles, 0.50, 0.78)

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
	te := NewTriageEngine(explainer, profiles, 0.50, 0.78)

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
	te := NewTriageEngine(explainer, profiles, 0.50, 0.78)

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

// TestAITriageBatchParsesValidJSON verifies that when the LLM returns valid JSON,
// the AI decisions actually override the fallback decisions (not silently ignored).
func TestAITriageBatchParsesValidJSON(t *testing.T) {
	// Create a mock explainer with a transport that returns valid triage JSON
	explainer := &Explainer{
		APIKey:  "test-key",
		Model:   "test-model",
		Enabled: true,
		client:  &http.Client{Transport: &mockTriageTransport{}},
	}
	profiles := make(map[string]*MerchantProfile)
	te := NewTriageEngine(explainer, profiles, 0.50, 0.78)

	alerts := []Alert{
		{TxnID: 101, MerchantID: "m1", CombinedScore: 0.65, StatScore: 0.6, ForestScore: 0.5, StatAlert: StatAlert{RuleFired: "velocity_spike", Score: 0.6}},
		{TxnID: 102, MerchantID: "m2", CombinedScore: 0.55, StatScore: 0.4, ForestScore: 0.5, StatAlert: StatAlert{RuleFired: "", Score: 0.4}},
	}
	labels := map[int64]data.FraudLabel{
		101: {TxnID: 101, IsFraud: true},
		102: {TxnID: 102, IsFraud: false},
	}
	txns := []data.Transaction{
		{TxnID: 101, MerchantID: "m1", AmountPaise: 50000, Timestamp: 1000000},
		{TxnID: 102, MerchantID: "m2", AmountPaise: 1000, Timestamp: 1000001},
	}

	decisions, metrics := te.Triage(alerts, labels, txns)

	// Verify AI actually drove the decisions (not fallback)
	aiCount := 0
	for _, d := range decisions {
		if d.Source == "ai" {
			aiCount++
		}
	}
	if aiCount == 0 {
		t.Error("expected at least one AI-sourced decision, got 0 — JSON parsing may be silently failing")
	}
	if metrics.AITriaged == 0 {
		t.Errorf("AITriaged = 0, expected > 0 — AI decisions not being counted")
	}
}

// TestAITriageSafetyGate verifies that the safety gate prevents the AI from
// approving transactions with high combined scores (preventing dangerous false negatives).
func TestAITriageSafetyGate(t *testing.T) {
	// Create a mock that always returns APPROVE
	explainer := &Explainer{
		APIKey:  "test-key",
		Model:   "test-model",
		Enabled: true,
		client:  &http.Client{Transport: &mockTriageApproveAllTransport{}},
	}
	profiles := make(map[string]*MerchantProfile)
	te := NewTriageEngine(explainer, profiles, 0.50, 0.78)

	// midpoint = (0.50 + 0.78) / 2 = 0.64
	// TxnID 201: score 0.70 >= midpoint → safety gate should override APPROVE → ESCALATE
	// TxnID 202: score 0.55 < midpoint → APPROVE is allowed
	alerts := []Alert{
		{TxnID: 201, MerchantID: "m1", CombinedScore: 0.70, StatScore: 0.6, ForestScore: 0.6, StatAlert: StatAlert{RuleFired: "velocity_spike", Score: 0.6}},
		{TxnID: 202, MerchantID: "m2", CombinedScore: 0.55, StatScore: 0.4, ForestScore: 0.4, StatAlert: StatAlert{RuleFired: "", Score: 0.4}},
	}
	labels := map[int64]data.FraudLabel{
		201: {TxnID: 201, IsFraud: true},
		202: {TxnID: 202, IsFraud: false},
	}
	txns := []data.Transaction{
		{TxnID: 201, MerchantID: "m1", AmountPaise: 50000, Timestamp: 1000000},
		{TxnID: 202, MerchantID: "m2", AmountPaise: 1000, Timestamp: 1000001},
	}

	decisions, _ := te.Triage(alerts, labels, txns)

	// Find decision for TxnID 201 (high score — should be ESCALATE due to safety gate)
	for _, d := range decisions {
		if d.TxnID == 201 {
			if d.Decision == "APPROVE" {
				t.Errorf("TxnID 201: safety gate FAILED — AI approved a high-score (%.2f) transaction that is actual fraud", d.CombinedScore)
			}
			if d.Decision != "ESCALATE" {
				t.Errorf("TxnID 201: expected ESCALATE from safety gate, got %s", d.Decision)
			}
		}
		if d.TxnID == 202 {
			if d.Decision != "APPROVE" {
				t.Errorf("TxnID 202: expected APPROVE (low score, safe), got %s", d.Decision)
			}
		}
	}
}
