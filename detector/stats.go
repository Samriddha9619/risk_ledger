package detector

import (
	"math"

	"github.com/Samriddha9619/risk_ledger/data"
)

// MerchantProfile tracks per-merchant statistics using Welford's online algorithm.
type MerchantProfile struct {
	Count       int64
	SumAmount   float64
	SumSqDiff   float64 // Welford's M2 for online variance
	Mean        float64
	LastTxnTime int64

	// Ring buffer for recent timestamps (velocity tracking)
	RecentTimes []int64
	recentIdx   int
	recentFull  bool
}

const recentWindowSize = 100 // track last 100 timestamps per merchant

// Update adds a new transaction to the merchant profile.
func (p *MerchantProfile) Update(txn data.Transaction) {
	p.Count++
	// Welford's online mean/variance
	delta := float64(txn.AmountPaise) - p.Mean
	p.Mean += delta / float64(p.Count)
	delta2 := float64(txn.AmountPaise) - p.Mean
	p.SumSqDiff += delta * delta2

	p.LastTxnTime = txn.Timestamp

	// Add to recent timestamps ring buffer
	if p.RecentTimes == nil {
		p.RecentTimes = make([]int64, recentWindowSize)
	}
	p.RecentTimes[p.recentIdx] = txn.Timestamp
	p.recentIdx++
	if p.recentIdx >= recentWindowSize {
		p.recentIdx = 0
		p.recentFull = true
	}
}

// Stddev returns the population standard deviation of amounts.
func (p *MerchantProfile) Stddev() float64 {
	if p.Count < 2 {
		return 0
	}
	return math.Sqrt(p.SumSqDiff / float64(p.Count))
}

// VelocityInWindow returns the count of transactions within the last windowSec seconds.
func (p *MerchantProfile) VelocityInWindow(currentTime int64, windowSec int64) int {
	count := 0
	n := recentWindowSize
	if !p.recentFull {
		n = p.recentIdx
	}
	for i := 0; i < n; i++ {
		if currentTime-p.RecentTimes[i] <= windowSec && p.RecentTimes[i] > 0 {
			count++
		}
	}
	return count
}

// StatAlert holds the result of statistical analysis for one transaction.
type StatAlert struct {
	AmountZScore    float64
	VelocityRatio   float64
	SmallBurstCount int
	Score           float64 // combined 0-1 score
	RuleFired       string  // which rule dominated
}

// StatisticalDetector runs rule-based checks on transactions.
type StatisticalDetector struct {
	Profiles map[string]*MerchantProfile

	// Configurable thresholds
	ZScoreThreshold   float64
	VelocityMultiple  float64
	SmallBurstLimit   int
	VelocityWindowSec int64
}

// NewStatisticalDetector creates a detector with default thresholds.
func NewStatisticalDetector() *StatisticalDetector {
	return &StatisticalDetector{
		Profiles:          make(map[string]*MerchantProfile),
		ZScoreThreshold:   3.0,
		VelocityMultiple:  5.0,
		SmallBurstLimit:   10,
		VelocityWindowSec: 300, // 5 minutes
	}
}

// Process updates the merchant profile and returns a statistical alert score.
func (d *StatisticalDetector) Process(txn data.Transaction) StatAlert {
	profile, exists := d.Profiles[txn.MerchantID]
	if !exists {
		profile = &MerchantProfile{}
		d.Profiles[txn.MerchantID] = profile
	}

	alert := StatAlert{}

	// Only score if we have enough history
	if profile.Count >= 10 {
		// Rule 1: Amount Z-score
		stddev := profile.Stddev()
		if stddev > 0 {
			alert.AmountZScore = math.Abs(float64(txn.AmountPaise)-profile.Mean) / stddev
		}

		// Rule 2: Velocity ratio
		currentVelocity := profile.VelocityInWindow(txn.Timestamp, d.VelocityWindowSec)
		// Expected velocity in window based on historical rate
		expectedVelocity := (float64(profile.Count) / float64(txn.Timestamp-profile.RecentTimes[0]+1)) * float64(d.VelocityWindowSec)
		if expectedVelocity > 0.1 {
			alert.VelocityRatio = float64(currentVelocity) / expectedVelocity
		}

		// Rule 3: Small amount burst (card testing signal)
		if txn.AmountPaise < 5000 { // under ₹50
			smallCount := 0
			n := recentWindowSize
			if !profile.recentFull {
				n = profile.recentIdx
			}
			for i := 0; i < n; i++ {
				if txn.Timestamp-profile.RecentTimes[i] <= d.VelocityWindowSec {
					smallCount++
				}
			}
			alert.SmallBurstCount = smallCount
		}

		// Compute individual scores (0-1)
		zScore := math.Min(alert.AmountZScore/6.0, 1.0)
		velScore := math.Min(alert.VelocityRatio/10.0, 1.0)
		burstScore := math.Min(float64(alert.SmallBurstCount)/20.0, 1.0)

		// Combined: max of the three
		alert.Score = math.Max(zScore, math.Max(velScore, burstScore))

		if zScore >= velScore && zScore >= burstScore {
			alert.RuleFired = "amount_zscore"
		} else if velScore >= burstScore {
			alert.RuleFired = "velocity_spike"
		} else {
			alert.RuleFired = "small_burst"
		}
	}

	// Update profile AFTER scoring (so we score against historical data, not including this txn)
	profile.Update(txn)
	return alert
}
