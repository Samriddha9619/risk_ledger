package detector

import (
	"math"

	"github.com/Samriddha9619/risk_ledger/data"
)

// FeatureVector is the fixed-length input for the Isolation Forest.
const NumFeatures = 5

// FeatureExtractor computes normalized features for ML scoring.
type FeatureExtractor struct {
	profiles map[string]*MerchantProfile // shared with StatisticalDetector
}

// NewFeatureExtractor creates an extractor that reads from existing merchant profiles.
func NewFeatureExtractor(profiles map[string]*MerchantProfile) *FeatureExtractor {
	return &FeatureExtractor{profiles: profiles}
}

// Extract computes a normalized feature vector for a single transaction.
// All features are in [0, 1] range.
func (fe *FeatureExtractor) Extract(txn data.Transaction) [NumFeatures]float64 {
	var features [NumFeatures]float64

	profile := fe.profiles[txn.MerchantID]
	if profile == nil || profile.Count < 10 {
		// Not enough history — return neutral features (0.5 = "no signal")
		for i := range features {
			features[i] = 0.5
		}
		return features
	}

	// Feature 0: Amount Z-score (normalized)
	stddev := profile.Stddev()
	if stddev > 0 {
		zscore := math.Abs(float64(txn.AmountPaise)-profile.Mean) / stddev
		features[0] = math.Min(zscore/6.0, 1.0)
	}

	// Feature 1: Velocity ratio (normalized)
	currentVel := profile.VelocityInWindow(txn.Timestamp, 300) // 5-min window
	// Estimate expected velocity
	timespan := txn.Timestamp - profile.RecentTimes[0]
	if timespan <= 0 {
		timespan = 1
	}
	expectedVel := (float64(profile.Count) / float64(timespan)) * 300.0
	if expectedVel > 0.1 {
		features[1] = math.Min(float64(currentVel)/expectedVel/10.0, 1.0)
	}

	// Feature 2: Time of day (cyclical encoding using sin)
	// Map hour to [0, 1] using sin(2π * hour/24), shifted to [0, 1]
	hour := float64((txn.Timestamp % 86400) / 3600)
	features[2] = (math.Sin(2.0*math.Pi*hour/24.0) + 1.0) / 2.0

	// Feature 3: Inter-transaction interval (normalized)
	if profile.LastTxnTime > 0 {
		gap := float64(txn.Timestamp - profile.LastTxnTime)
		// Estimate average gap
		avgGap := float64(timespan) / float64(profile.Count)
		if avgGap > 0 {
			// Low gap relative to average = suspicious (inverted and normalized)
			ratio := gap / avgGap
			features[3] = 1.0 - math.Min(ratio/5.0, 1.0) // closer to 1 = shorter gap than expected
		}
	}

	// Feature 4: Amount ratio (txn amount / merchant average, normalized)
	if profile.Mean > 0 {
		ratio := float64(txn.AmountPaise) / profile.Mean
		features[4] = math.Min(ratio/10.0, 1.0)
	}

	return features
}