package detector

import (
	"math"

	"github.com/Samriddha9619/risk_ledger/data"
)

// MerchantProfile tracks per-merchant statistics using Welford's online algorithm.
type MerchantProfile struct {
	Count        int64
	Mean         float64
	SumSqDiff    float64 // Welford's M2 for online variance
	FirstTxnTime int64
	LastTxnTime  int64

	// Ring buffer for recent transaction timestamps
	recentTimes    [recentWindowSize]int64
	recentIdx      int
	recentCount    int // total items written (capped conceptually, but tracks fill)

	// Ring buffer for recent small-amount (<₹50) transaction timestamps
	smallTimes     [smallWindowSize]int64
	smallIdx       int
	smallCount     int
}

const (
	recentWindowSize = 200 // track last 200 timestamps per merchant
	smallWindowSize  = 50  // track last 50 small-txn timestamps per merchant
)

// Update adds a new transaction to the merchant profile.
func (p *MerchantProfile) Update(txn data.Transaction) {
	p.Count++

	// Track first transaction time
	if p.FirstTxnTime == 0 {
		p.FirstTxnTime = txn.Timestamp
	}

	// Welford's online mean/variance
	delta := float64(txn.AmountPaise) - p.Mean
	p.Mean += delta / float64(p.Count)
	delta2 := float64(txn.AmountPaise) - p.Mean
	p.SumSqDiff += delta * delta2

	p.LastTxnTime = txn.Timestamp

	// Add to recent timestamps ring buffer
	p.recentTimes[p.recentIdx] = txn.Timestamp
	p.recentIdx = (p.recentIdx + 1) % recentWindowSize
	p.recentCount++

	// Track small-amount transactions separately for card-testing detection
	if txn.AmountPaise < 5000 { // under ₹50
		p.smallTimes[p.smallIdx] = txn.Timestamp
		p.smallIdx = (p.smallIdx + 1) % smallWindowSize
		p.smallCount++
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
	if p.recentCount < recentWindowSize {
		n = p.recentCount
	}
	for i := 0; i < n; i++ {
		if p.recentTimes[i] > 0 && currentTime-p.recentTimes[i] <= windowSec {
			count++
		}
	}
	return count
}

// SmallTxnVelocity returns the count of small-amount transactions within the last windowSec seconds.
func (p *MerchantProfile) SmallTxnVelocity(currentTime int64, windowSec int64) int {
	count := 0
	n := smallWindowSize
	if p.smallCount < smallWindowSize {
		n = p.smallCount
	}
	for i := 0; i < n; i++ {
		if p.smallTimes[i] > 0 && currentTime-p.smallTimes[i] <= windowSec {
			count++
		}
	}
	return count
}

// ExpectedVelocity returns the average transactions per window based on full history.
func (p *MerchantProfile) ExpectedVelocity(currentTime int64, windowSec int64) float64 {
	totalDuration := currentTime - p.FirstTxnTime
	if totalDuration <= 0 {
		return 0
	}
	return (float64(p.Count) / float64(totalDuration)) * float64(windowSec)
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

		// Rule 2: Velocity ratio — compare current window velocity to historical average
		currentVelocity := profile.VelocityInWindow(txn.Timestamp, d.VelocityWindowSec)
		expectedVelocity := profile.ExpectedVelocity(txn.Timestamp, d.VelocityWindowSec)
		if expectedVelocity > 0.1 {
			alert.VelocityRatio = float64(currentVelocity) / expectedVelocity
		}

		// Rule 3: Small amount burst — counts only small-amount txns in window
		if txn.AmountPaise < 5000 { // under ₹50
			alert.SmallBurstCount = profile.SmallTxnVelocity(txn.Timestamp, d.VelocityWindowSec)
		}

		// Compute individual scores (0-1), tuned for sensitivity
		zScore := math.Min(alert.AmountZScore/4.0, 1.0)
		velScore := math.Min(math.Max(alert.VelocityRatio-1.0, 0)/4.0, 1.0) // subtract 1 so ratio=1 maps to 0
		burstScore := math.Min(float64(alert.SmallBurstCount)/10.0, 1.0)

		// Combined: weighted max — velocity gets a 1.5x boost because it was underperforming
		// (velocity spikes have normal amounts, so Z-score doesn't fire — velocity needs extra weight)
		alert.Score = math.Max(zScore, math.Max(velScore*1.5, burstScore))
		if alert.Score > 1.0 {
			alert.Score = 1.0
		}

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
