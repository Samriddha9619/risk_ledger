package eval

import (
	"github.com/Samriddha9619/risk_ledger/data"
	"github.com/Samriddha9619/risk_ledger/detector"
)

// EvalResult holds metrics at a single threshold.
type EvalResult struct {
	Threshold float64
	TP        int
	FP        int
	TN        int
	FN        int
	Precision float64
	Recall    float64
	F1        float64
	FPR       float64
	FPCost    float64 // total false-positive cost in paise
	FNCost    float64 // total false-negative cost in paise
	TotalCost float64
}

// PerPatternResult holds recall for a specific fraud pattern.
type PerPatternResult struct {
	Pattern string
	Total   int
	Caught  int
	Recall  float64
}

// Evaluate computes binary classification metrics at a given threshold.
func Evaluate(alerts []detector.Alert, labels map[int64]data.FraudLabel, threshold float64) EvalResult {
	result := EvalResult{Threshold: threshold}

	for _, alert := range alerts {
		label := labels[alert.TxnID]
		predicted := alert.CombinedScore >= threshold
		actual := label.IsFraud

		switch {
		case predicted && actual:
			result.TP++
		case predicted && !actual:
			result.FP++
			// FP cost: merchant loses ~2% of blocked transaction value
			// We don't have the amount in alert, so use a flat estimate
			result.FPCost += 500 * 100 // ₹500 average transaction × 2% ≈ ₹10 per FP (in paise)
		case !predicted && actual:
			result.FN++
			// FN cost: merchant loses full fraud amount
			// Use average fraud amount estimate
			result.FNCost += 5000 * 100 // ₹5000 average fraud amount (in paise)
		case !predicted && !actual:
			result.TN++
		}
	}

	// Precision
	if result.TP+result.FP > 0 {
		result.Precision = float64(result.TP) / float64(result.TP+result.FP)
	}
	// Recall
	if result.TP+result.FN > 0 {
		result.Recall = float64(result.TP) / float64(result.TP+result.FN)
	}
	// F1
	if result.Precision+result.Recall > 0 {
		result.F1 = 2.0 * result.Precision * result.Recall / (result.Precision + result.Recall)
	}
	// FPR
	if result.FP+result.TN > 0 {
		result.FPR = float64(result.FP) / float64(result.FP+result.TN)
	}
	// Total cost
	result.TotalCost = result.FPCost + result.FNCost

	return result
}

// EvaluateWithAmounts computes metrics using actual transaction amounts for cost.
func EvaluateWithAmounts(alerts []detector.Alert, labels map[int64]data.FraudLabel, amounts map[int64]int64, threshold float64) EvalResult {
	result := EvalResult{Threshold: threshold}

	for _, alert := range alerts {
		label := labels[alert.TxnID]
		predicted := alert.CombinedScore >= threshold
		actual := label.IsFraud
		amount := float64(amounts[alert.TxnID])

		switch {
		case predicted && actual:
			result.TP++
		case predicted && !actual:
			result.FP++
			result.FPCost += amount * 0.02 // 2% conversion loss
		case !predicted && actual:
			result.FN++
			result.FNCost += amount // full fraud loss
		case !predicted && !actual:
			result.TN++
		}
	}

	if result.TP+result.FP > 0 {
		result.Precision = float64(result.TP) / float64(result.TP+result.FP)
	}
	if result.TP+result.FN > 0 {
		result.Recall = float64(result.TP) / float64(result.TP+result.FN)
	}
	if result.Precision+result.Recall > 0 {
		result.F1 = 2.0 * result.Precision * result.Recall / (result.Precision + result.Recall)
	}
	if result.FP+result.TN > 0 {
		result.FPR = float64(result.FP) / float64(result.FP+result.TN)
	}
	result.TotalCost = result.FPCost + result.FNCost

	return result
}

// SweepThresholds evaluates metrics across numSteps evenly-spaced thresholds.
func SweepThresholds(alerts []detector.Alert, labels map[int64]data.FraudLabel, amounts map[int64]int64, numSteps int) []EvalResult {
	results := make([]EvalResult, numSteps+1)
	for i := 0; i <= numSteps; i++ {
		threshold := float64(i) / float64(numSteps)
		results[i] = EvaluateWithAmounts(alerts, labels, amounts, threshold)
	}
	return results
}

// FindOptimalThreshold returns the result that minimizes total cost.
func FindOptimalThreshold(results []EvalResult) EvalResult {
	best := results[0]
	for _, r := range results[1:] {
		if r.TotalCost < best.TotalCost {
			best = r
		}
	}
	return best
}

// FindBestF1 returns the result that maximizes F1 score.
func FindBestF1(results []EvalResult) EvalResult {
	best := results[0]
	for _, r := range results[1:] {
		if r.F1 > best.F1 {
			best = r
		}
	}
	return best
}

// EvaluatePerPattern computes recall per fraud pattern.
func EvaluatePerPattern(alerts []detector.Alert, labels map[int64]data.FraudLabel, threshold float64) []PerPatternResult {
	patternCounts := make(map[string]*PerPatternResult)

	for _, alert := range alerts {
		label := labels[alert.TxnID]
		if !label.IsFraud {
			continue
		}
		pr, exists := patternCounts[label.FraudType]
		if !exists {
			pr = &PerPatternResult{Pattern: label.FraudType}
			patternCounts[label.FraudType] = pr
		}
		pr.Total++
		if alert.CombinedScore >= threshold {
			pr.Caught++
		}
	}

	var results []PerPatternResult
	for _, pr := range patternCounts {
		if pr.Total > 0 {
			pr.Recall = float64(pr.Caught) / float64(pr.Total)
		}
		results = append(results, *pr)
	}
	return results
}