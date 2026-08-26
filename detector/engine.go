package detector

import (
	"math"

	"github.com/Samriddha9619/risk_ledger/data"
)

// Alert is the output of the detection engine for a single transaction.
type Alert struct {
	TxnID         int64
	MerchantID    string
	StatScore     float64
	ForestScore   float64
	CombinedScore float64
	StatAlert     StatAlert
	Features      [NumFeatures]float64
}

// EngineConfig holds tunable parameters for the detection engine.
type EngineConfig struct {
	StatWeight   float64 // weight for statistical score in combination
	ForestWeight float64 // weight for Isolation Forest score in combination
	NumTrees     int     // number of trees in the Isolation Forest
	SubsampleSz  int     // subsample size per tree
}

// DefaultEngineConfig returns the production configuration.
func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		StatWeight:   0.5,
		ForestWeight: 0.5,
		NumTrees:     100,
		SubsampleSz:  256,
	}
}

// DetectionEngine combines statistical and ML-based detection.
type DetectionEngine struct {
	Stats     *StatisticalDetector
	Extractor *FeatureExtractor
	Forest    *IsolationForest

	StatWeight   float64
	ForestWeight float64
}

// NewDetectionEngine creates the two-layer engine with default config.
func NewDetectionEngine() *DetectionEngine {
	return NewDetectionEngineWithConfig(DefaultEngineConfig())
}

// NewDetectionEngineWithConfig creates a detection engine with the given configuration.
func NewDetectionEngineWithConfig(cfg EngineConfig) *DetectionEngine {
	stats := NewStatisticalDetector()
	return &DetectionEngine{
		Stats:        stats,
		Extractor:    NewFeatureExtractor(stats.Profiles),
		Forest:       NewIsolationForest(cfg.NumTrees, cfg.SubsampleSz),
		StatWeight:   cfg.StatWeight,
		ForestWeight: cfg.ForestWeight,
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

// TrainForestWithSeed trains the Isolation Forest with a specific random seed.
func (e *DetectionEngine) TrainForestWithSeed(features [][]float64, seed int64) {
	e.Forest.Train(features, seed)
}

// Score runs both layers and combines them.
//
// Strategy: max-boost. We take the weighted average of the two scores,
// but if the statistical layer is highly confident (>0.7), we boost the
// combined score to at least 70% of the stat score. This prevents the
// forest from suppressing strong rule-based detections (like velocity spikes)
// while still letting the forest contribute when both layers agree.
func (e *DetectionEngine) Score(txn data.Transaction) Alert {
	statAlert := e.Stats.Process(txn)
	features := e.Extractor.Extract(txn)

	featureSlice := features[:]
	forestScore := e.Forest.Score(featureSlice)

	// Weighted average as base
	combined := e.StatWeight*statAlert.Score + e.ForestWeight*forestScore

	// Boost: if stats layer is confident, don't let forest drag it below 70% of stat score
	if statAlert.Score > 0.7 {
		floor := statAlert.Score * 0.7
		combined = math.Max(combined, floor)
	}

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