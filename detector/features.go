package detector

import (
	"math"

	"github.com/Samriddha9619/risk_ledger/data"
)

// NumFeatures is the fixed-length input for the Isolation Forest.
const NumFeatures = 8

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

	// Feature 1: Velocity ratio (5-min window)
	currentVel := float64(profile.VelocityInWindow(txn.Timestamp, 300))
	expectedVel := profile.ExpectedVelocity(txn.Timestamp, 300)
	if expectedVel > 0.1 {
		ratio := currentVel / expectedVel
		features[1] = math.Min(math.Max(ratio-1.0, 0)/4.0, 1.0)
	}

	// Feature 2: Time of day (cyclical sin encoding)
	hour := float64((txn.Timestamp % 86400) / 3600)
	features[2] = (math.Sin(2.0*math.Pi*hour/24.0) + 1.0) / 2.0

	// Feature 3: Inter-transaction gap (inverted — short gap = high score)
	if profile.LastTxnTime > 0 && profile.Count > 1 {
		gap := float64(txn.Timestamp - profile.LastTxnTime)
		totalDuration := float64(txn.Timestamp - profile.FirstTxnTime)
		if totalDuration > 0 {
			avgGap := totalDuration / float64(profile.Count)
			if avgGap > 0 {
				ratio := gap / avgGap
				features[3] = 1.0 - math.Min(ratio/5.0, 1.0)
			}
		}
	}

	// Feature 4: Amount ratio (bidirectional)
	if profile.Mean > 0 {
		ratio := float64(txn.AmountPaise) / profile.Mean
		if ratio >= 1.0 {
			features[4] = math.Min(ratio/10.0, 1.0)
		} else {
			features[4] = math.Min((1.0/ratio)/10.0, 1.0)
		}
	}

	// Feature 5: 1-minute burst count (raw, normalized)
	vel1m := float64(profile.VelocityInWindow(txn.Timestamp, 60))
	features[5] = math.Min(vel1m/15.0, 1.0)

	// Feature 6: Small-txn fraction in recent window
	smallVel := float64(profile.SmallTxnVelocity(txn.Timestamp, 300))
	if currentVel > 0 {
		features[6] = math.Min(smallVel/currentVel, 1.0)
	}

	// Feature 7: Transaction count acceleration
	// Compare velocity in last 2 min vs overall 5 min rate
	vel2m := float64(profile.VelocityInWindow(txn.Timestamp, 120))
	if currentVel > 2 {
		// If 2-min window has >60% of 5-min traffic, rate is accelerating
		accel := (vel2m / currentVel) / 0.4 // 0.4 = expected fraction (2/5)
		features[7] = math.Min(math.Max(accel-1.0, 0)/3.0, 1.0)
	}

	return features
}