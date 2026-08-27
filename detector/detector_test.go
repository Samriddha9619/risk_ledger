package detector

import (
	"math"
	"testing"

	"github.com/Samriddha9619/risk_ledger/data"
)

// TestIsolationForestKnownAnomaly verifies the forest scores a known outlier higher than normal points.
func TestIsolationForestKnownAnomaly(t *testing.T) {
	// Build dataset: 500 normal points clustered around [0.5, 0.5]
	// Plus one extreme outlier at [1.0, 1.0]
	var dataset [][]float64
	for i := 0; i < 500; i++ {
		x := 0.4 + float64(i%10)*0.02 // 0.4-0.58
		y := 0.4 + float64(i%10)*0.02
		dataset = append(dataset, []float64{x, y})
	}

	forest := NewIsolationForest(100, 256)
	forest.Train(dataset, 42)

	normalScore := forest.Score([]float64{0.5, 0.5})
	anomalyScore := forest.Score([]float64{1.0, 1.0})

	if anomalyScore <= normalScore {
		t.Errorf("anomaly score (%.4f) should be higher than normal score (%.4f)", anomalyScore, normalScore)
	}

	// Anomaly should be > 0.5 (the "anomalous" region per the paper)
	if anomalyScore < 0.5 {
		t.Errorf("anomaly score (%.4f) should be >= 0.5 for a clear outlier", anomalyScore)
	}
}

// TestIsolationForestScoreRange verifies all scores are in [0, 1].
func TestIsolationForestScoreRange(t *testing.T) {
	var dataset [][]float64
	for i := 0; i < 300; i++ {
		dataset = append(dataset, []float64{float64(i) / 300.0, float64(i%50) / 50.0})
	}

	forest := NewIsolationForest(50, 128)
	forest.Train(dataset, 99)

	testPoints := [][]float64{
		{0.0, 0.0},
		{0.5, 0.5},
		{1.0, 1.0},
		{-1.0, -1.0},
		{100.0, 100.0},
	}

	for _, pt := range testPoints {
		score := forest.Score(pt)
		if score < 0.0 || score > 1.0 {
			t.Errorf("score %.4f for point %v is out of [0,1] range", score, pt)
		}
	}
}

// TestExpectedPathLength verifies c(n) against known values from the paper.
func TestExpectedPathLength(t *testing.T) {
	tests := []struct {
		n        float64
		expected float64
		tol      float64
	}{
		{1, 0, 0},
		{2, 1, 0},
		// c(n) = 2*H(n-1) - 2*(n-1)/n where H(k) ≈ ln(k) + 0.5772
		// c(256) = 2*(ln(255)+0.5772) - 2*255/256 ≈ 10.24
		{256, 10.24, 0.1},
		// c(1000) = 2*(ln(999)+0.5772) - 2*999/1000 ≈ 12.97
		{1000, 12.97, 0.1},
	}

	for _, tc := range tests {
		got := expectedPathLength(tc.n)
		if math.Abs(got-tc.expected) > tc.tol {
			t.Errorf("expectedPathLength(%.0f) = %.4f, want ≈ %.2f (±%.2f)", tc.n, got, tc.expected, tc.tol)
		}
	}
}

// TestIsolationForestUntrainedReturnsHalf verifies an untrained forest returns 0.5.
func TestIsolationForestUntrainedReturnsHalf(t *testing.T) {
	forest := NewIsolationForest(10, 64)
	// Don't call Train
	score := forest.Score([]float64{0.5, 0.5})
	if score != 0.5 {
		t.Errorf("untrained forest should return 0.5, got %.4f", score)
	}
}

// TestMerchantProfileWelford verifies Welford's algorithm produces correct mean/stddev.
func TestMerchantProfileWelford(t *testing.T) {
	profile := &MerchantProfile{}

	// Feed known values: 10, 20, 30, 40, 50
	amounts := []int64{1000, 2000, 3000, 4000, 5000} // in paise
	for _, a := range amounts {
		profile.Update(data.Transaction{
			TxnID:       1,
			MerchantID:  "test",
			AmountPaise: a,
			Timestamp:   1000,
		})
	}

	expectedMean := 3000.0
	if math.Abs(profile.Mean-expectedMean) > 0.01 {
		t.Errorf("mean = %.2f, want %.2f", profile.Mean, expectedMean)
	}

	// Population stddev of [1000,2000,3000,4000,5000] = sqrt(2000000) ≈ 1414.21
	expectedStd := math.Sqrt(2000000.0)
	gotStd := profile.Stddev()
	if math.Abs(gotStd-expectedStd) > 1.0 {
		t.Errorf("stddev = %.2f, want ≈ %.2f", gotStd, expectedStd)
	}
}

// TestMerchantProfileVelocity verifies the velocity window counting.
func TestMerchantProfileVelocity(t *testing.T) {
	profile := &MerchantProfile{}

	baseTime := int64(1000000)
	// Add 5 transactions: at t=0, t=60, t=120, t=180, t=240 (all within 5 min)
	for i := 0; i < 5; i++ {
		profile.Update(data.Transaction{
			TxnID:       int64(i + 1),
			MerchantID:  "test",
			AmountPaise: 10000,
			Timestamp:   baseTime + int64(i*60),
		})
	}

	// All 5 should be within a 300-second window from the last timestamp
	vel := profile.VelocityInWindow(baseTime+240, 300)
	if vel != 5 {
		t.Errorf("velocity = %d, want 5 (all txns within window)", vel)
	}

	// Only last 2 should be within a 90-second window from the last timestamp
	vel = profile.VelocityInWindow(baseTime+240, 90)
	if vel != 2 {
		t.Errorf("velocity = %d, want 2 (only last 2 txns within 90s)", vel)
	}
}

// TestMerchantProfileSmallTxnVelocity verifies the small-amount tracking ring buffer.
func TestMerchantProfileSmallTxnVelocity(t *testing.T) {
	profile := &MerchantProfile{}
	baseTime := int64(1000000)

	// Mix of small and large transactions
	txns := []struct {
		amount int64
		offset int64
	}{
		{100, 0},    // small
		{50000, 60}, // large
		{200, 120},  // small
		{300, 180},  // small
		{80000, 240}, // large
	}

	for i, tx := range txns {
		profile.Update(data.Transaction{
			TxnID:       int64(i + 1),
			MerchantID:  "test",
			AmountPaise: tx.amount,
			Timestamp:   baseTime + tx.offset,
		})
	}

	// Only 3 small transactions (<5000 paise) within the full window
	smallVel := profile.SmallTxnVelocity(baseTime+240, 300)
	if smallVel != 3 {
		t.Errorf("small txn velocity = %d, want 3", smallVel)
	}
}

// TestStatisticalDetectorScoring verifies detection rules fire correctly.
func TestStatisticalDetectorScoring(t *testing.T) {
	det := NewStatisticalDetector()

	// Build up a profile with 20 normal transactions for merchant_001
	baseTime := int64(1000000)
	for i := 0; i < 20; i++ {
		det.Process(data.Transaction{
			TxnID:       int64(i + 1),
			MerchantID:  "merchant_001",
			AmountPaise: 10000 + int64(i*100), // ₹100-₹119 range
			Timestamp:   baseTime + int64(i*3600),
			Method:      "upi",
			Category:    "grocery",
		})
	}

	// Now send a huge amount — should trigger amount_zscore
	alert := det.Process(data.Transaction{
		TxnID:       100,
		MerchantID:  "merchant_001",
		AmountPaise: 500000, // ₹5000 — way above ₹100 average
		Timestamp:   baseTime + 100000,
		Method:      "card",
		Category:    "grocery",
	})

	if alert.Score == 0 {
		t.Error("expected non-zero score for amount anomaly")
	}
	if alert.AmountZScore < 3.0 {
		t.Errorf("expected high Z-score, got %.2f", alert.AmountZScore)
	}
}

// TestFeatureExtractorNeutralForNewMerchant verifies new merchants get neutral features.
func TestFeatureExtractorNeutralForNewMerchant(t *testing.T) {
	profiles := make(map[string]*MerchantProfile)
	ext := NewFeatureExtractor(profiles)

	features := ext.Extract(data.Transaction{
		TxnID:       1,
		MerchantID:  "new_merchant",
		AmountPaise: 10000,
		Timestamp:   1000000,
	})

	for i, f := range features {
		if f != 0.5 {
			t.Errorf("feature[%d] = %.4f, want 0.5 for new merchant", i, f)
		}
	}
}

// TestDetectionEngineCombinedScore verifies the engine produces valid combined scores.
func TestDetectionEngineCombinedScore(t *testing.T) {
	engine := NewDetectionEngine()

	// Train with 50 normal transactions
	var features [][]float64
	baseTime := int64(1000000)
	for i := 0; i < 50; i++ {
		txn := data.Transaction{
			TxnID:       int64(i + 1),
			MerchantID:  "merchant_001",
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

	// Score a normal transaction
	normalAlert := engine.Score(data.Transaction{
		TxnID:       100,
		MerchantID:  "merchant_001",
		AmountPaise: 10000,
		Timestamp:   baseTime + 200000,
		Method:      "upi",
		Category:    "grocery",
	})

	if normalAlert.CombinedScore < 0 || normalAlert.CombinedScore > 1 {
		t.Errorf("combined score %.4f out of [0,1] range", normalAlert.CombinedScore)
	}
}
