package detector

import (
	"github.com/Samriddha9619/risk_ledger/data"
)

// Alert is the output of the detection engine for a single transaction.
type Alert struct {
	TxnID        int64
	MerchantID   string
	StatScore    float64
	ForestScore  float64
	CombinedScore float64
	StatAlert    StatAlert
	Features     [NumFeatures]float64
}

// DetectionEngine combines statistical and ML-based detection.
type DetectionEngine struct {
	Stats     *StatisticalDetector
	Extractor *FeatureExtractor
	Forest    *IsolationForest

	StatWeight   float64
	ForestWeight float64
}

// NewDetectionEngine creates the two-layer engine.
// Call TrainForest before scoring test data.
func NewDetectionEngine() *DetectionEngine {
	stats := NewStatisticalDetector()
	return &DetectionEngine{
		Stats:        stats,
		Extractor:    NewFeatureExtractor(stats.Profiles),
		Forest:       NewIsolationForest(100, 256),
		StatWeight:   0.4,
		ForestWeight: 0.6,
	}
}

// ProcessTraining feeds a transaction into the statistical detector to build profiles.
// Returns the feature vector for later forest training.
func (e *DetectionEngine) ProcessTraining(txn data.Transaction) [NumFeatures]float64 {
	e.Stats.Process(txn)
	return e.Extractor.Extract(txn)
}

// TrainForest trains the Isolation Forest on collected feature vectors.
func (e *DetectionEngine) TrainForest(features [][]float64) {
	e.Forest.Train(features, 42)
}

// Score runs both layers on a transaction and returns a combined alert.
func (e *DetectionEngine) Score(txn data.Transaction) Alert {
	statAlert := e.Stats.Process(txn)
	features := e.Extractor.Extract(txn)

	featureSlice := features[:]
	forestScore := e.Forest.Score(featureSlice)

	combined := e.StatWeight*statAlert.Score + e.ForestWeight*forestScore

	// Clamp to [0, 1]
	if combined > 1.0 {
		combined = 1.0
	}

	return Alert{
		TxnID:         txn.TxnID,
		MerchantID:    txn.MerchantID,
		StatScore:     statAlert.Score,
		ForestScore:   forestScore,
		CombinedScore: combined,
		StatAlert:     statAlert,
		Features:      features,
	}
}