package data

import (
	"encoding/csv"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strconv"
)

// MerchantConfig defines a synthetic merchant's normal behavior.
type MerchantConfig struct {
	ID             string
	Category       string
	AvgAmountPaise int64   // log-normal center
	StdAmountPaise float64 // log-normal spread
	TxnsPerHour    float64 // Poisson rate
}

// GeneratorConfig controls the synthetic data output.
type GeneratorConfig struct {
	NumMerchants  int
	DurationHours int     // how many hours of data to generate
	FraudRate     float64 // fraction of merchants that get a fraud episode
	Seed          int64
	OutputDir     string
}

// DefaultConfig returns sensible defaults for ~50K transactions.
func DefaultConfig() GeneratorConfig {
	return GeneratorConfig{
		NumMerchants:  200,
		DurationHours: 72, // 3 days
		FraudRate:     0.08,
		Seed:          67,
		OutputDir:     "generated_data",
	}
}

// Generate creates synthetic transaction data and writes CSV files.
// Returns the total number of transactions and number of fraud transactions.
func Generate(cfg GeneratorConfig) (totalTxns int, fraudTxns int, err error) {
	rng := rand.New(rand.NewSource(cfg.Seed))

	if err := os.MkdirAll(cfg.OutputDir, 0755); err != nil {
		return 0, 0, fmt.Errorf("creating output dir: %w", err)
	}

	// Build merchant profiles
	merchants := buildMerchants(rng, cfg.NumMerchants)

	// Base timestamp: Aug 1 2026 00:00:00 IST
	baseTime := int64(1785346200)

	// Generate normal transactions for all merchants
	var allTxns []Transaction
	var allLabels []FraudLabel
	txnID := int64(1)

	for _, m := range merchants {
		txns := generateNormalTraffic(rng, m, baseTime, cfg.DurationHours, &txnID)
		for _, t := range txns {
			allTxns = append(allTxns, t)
			allLabels = append(allLabels, FraudLabel{TxnID: t.TxnID, IsFraud: false, FraudType: ""})
		}
	}

	// Inject fraud episodes into a subset of merchants
	numFraudMerchants := int(float64(len(merchants)) * cfg.FraudRate)
	// Shuffle and pick first N
	perm := rng.Perm(len(merchants))
	for i := 0; i < numFraudMerchants && i < len(perm); i++ {
		m := merchants[perm[i]]
		fraudType := []string{"card_testing", "amount_spike", "velocity_spike"}[rng.Intn(3)]
		// Pick a random time in the second half of the timeline for the fraud episode
		fraudStart := baseTime + int64(cfg.DurationHours*3600/2) + int64(rng.Intn(cfg.DurationHours*3600/2))

		var fraudTxnsList []Transaction
		switch fraudType {
		case "card_testing":
			fraudTxnsList = generateCardTestingFraud(rng, m, fraudStart, &txnID)
		case "amount_spike":
			fraudTxnsList = generateAmountSpikeFraud(rng, m, fraudStart, &txnID)
		case "velocity_spike":
			fraudTxnsList = generateVelocitySpikeFraud(rng, m, fraudStart, &txnID)
		}

		for _, t := range fraudTxnsList {
			allTxns = append(allTxns, t)
			allLabels = append(allLabels, FraudLabel{TxnID: t.TxnID, IsFraud: true, FraudType: fraudType})
			fraudTxns++
		}
	}

	// Sort everything by timestamp for temporal consistency
	sort.Slice(allTxns, func(i, j int) bool { return allTxns[i].Timestamp < allTxns[j].Timestamp })

	// --- Held-out fraud injection is NOT here; gradual_creep is only in GenerateHeldOutCreep ---

	// Build label lookup for sorted output
	labelMap := make(map[int64]FraudLabel, len(allLabels))
	for _, l := range allLabels {
		labelMap[l.TxnID] = l
	}

	// Write transactions CSV
	txnFile, err := os.Create(fmt.Sprintf("%s/transactions.csv", cfg.OutputDir))
	if err != nil {
		return 0, 0, err
	}
	defer txnFile.Close()
	txnWriter := csv.NewWriter(txnFile)
	defer txnWriter.Flush()

	for _, t := range allTxns {
		if err := txnWriter.Write([]string{
			strconv.FormatInt(t.TxnID, 10),
			t.MerchantID,
			strconv.FormatInt(t.AmountPaise, 10),
			strconv.FormatInt(t.Timestamp, 10),
			t.Method,
			t.Category,
		}); err != nil {
			return 0, 0, fmt.Errorf("writing transaction CSV: %w", err)
		}
	}

	// Write labels CSV
	labelFile, err := os.Create(fmt.Sprintf("%s/labels.csv", cfg.OutputDir))
	if err != nil {
		return 0, 0, err
	}
	defer labelFile.Close()
	labelWriter := csv.NewWriter(labelFile)
	defer labelWriter.Flush()

	for _, t := range allTxns {
		l := labelMap[t.TxnID]
		isFraud := "0"
		if l.IsFraud {
			isFraud = "1"
		}
		labelWriter.Write([]string{
			strconv.FormatInt(t.TxnID, 10),
			isFraud,
			l.FraudType,
		})
	}

	return len(allTxns), fraudTxns, nil
}

// buildMerchants creates diverse merchant profiles.
func buildMerchants(rng *rand.Rand, n int) []MerchantConfig {
	categories := []struct {
		name       string
		avgPaise   int64
		stdPaise   float64
		txnsPerHr  float64
		weight     float64
	}{
		{"grocery", 40000, 20000, 8.0, 0.35},     // ₹400 avg
		{"electronics", 800000, 500000, 1.5, 0.15}, // ₹8000 avg
		{"food", 30000, 15000, 12.0, 0.30},         // ₹300 avg
		{"fashion", 200000, 100000, 3.0, 0.10},     // ₹2000 avg
		{"services", 150000, 80000, 2.0, 0.10},     // ₹1500 avg
	}

	merchants := make([]MerchantConfig, n)
	for i := 0; i < n; i++ {
		// Pick category by weight
		r := rng.Float64()
		cumulative := 0.0
		catIdx := 0
		for j, c := range categories {
			cumulative += c.weight
			if r <= cumulative {
				catIdx = j
				break
			}
		}
		cat := categories[catIdx]

		// Add some per-merchant variation (±30%)
		variation := 0.7 + rng.Float64()*0.6
		txnVariation := 0.5 + rng.Float64()*1.0

		merchants[i] = MerchantConfig{
			ID:             fmt.Sprintf("merchant_%03d", i+1),
			Category:       cat.name,
			AvgAmountPaise: int64(float64(cat.avgPaise) * variation),
			StdAmountPaise: cat.stdPaise * variation,
			TxnsPerHour:    cat.txnsPerHr * txnVariation,
		}
	}
	return merchants
}

// generateNormalTraffic creates normal transactions for a merchant over the time period.
func generateNormalTraffic(rng *rand.Rand, m MerchantConfig, baseTime int64, durationHours int, nextID *int64) []Transaction {
	var txns []Transaction
	methods := []string{"upi", "card", "netbanking"}
	methodWeights := []float64{0.60, 0.30, 0.10}

	totalSeconds := durationHours * 3600

	// Generate inter-arrival times using exponential distribution
	avgGapSeconds := 3600.0 / m.TxnsPerHour
	currentTime := float64(baseTime)

	for currentTime < float64(baseTime+int64(totalSeconds)) {
		// Exponential inter-arrival
		gap := rng.ExpFloat64() * avgGapSeconds
		currentTime += gap

		if currentTime >= float64(baseTime+int64(totalSeconds)) {
			break
		}

		// Time-of-day weighting: reduce traffic at night (0-6 AM), peak at 12-8 PM
		hour := int((int64(currentTime)-baseTime)/3600) % 24
		activityWeight := timeOfDayWeight(hour)
		if rng.Float64() > activityWeight {
			continue // skip this transaction (simulates quiet hours)
		}

		// Log-normal amount distribution
		amount := sampleLogNormal(rng, float64(m.AvgAmountPaise), m.StdAmountPaise)
		if amount < 100 { // min ₹1
			amount = 100
		}

		// Pick payment method
		method := weightedChoice(rng, methods, methodWeights)

		txn := Transaction{
			TxnID:       *nextID,
			MerchantID:  m.ID,
			AmountPaise: amount,
			Timestamp:   int64(currentTime),
			Method:      method,
			Category:    m.Category,
		}
		txns = append(txns, txn)
		*nextID++
	}
	return txns
}

// --- Fraud Pattern Generators ---

// generateCardTestingFraud: 15-30 tiny transactions in 3-5 minutes.
func generateCardTestingFraud(rng *rand.Rand, m MerchantConfig, startTime int64, nextID *int64) []Transaction {
	count := 15 + rng.Intn(16) // 15-30
	window := 180 + rng.Intn(120) // 3-5 minutes in seconds
	var txns []Transaction

	for i := 0; i < count; i++ {
		txn := Transaction{
			TxnID:       *nextID,
			MerchantID:  m.ID,
			AmountPaise: 100 + int64(rng.Intn(4900)), // ₹1-₹50
			Timestamp:   startTime + int64(rng.Intn(window)),
			Method:      "card",
			Category:    m.Category,
		}
		txns = append(txns, txn)
		*nextID++
	}
	return txns
}

// generateAmountSpikeFraud: 5-10 transactions at 10-50x normal amount.
func generateAmountSpikeFraud(rng *rand.Rand, m MerchantConfig, startTime int64, nextID *int64) []Transaction {
	count := 5 + rng.Intn(6) // 5-10
	multiplier := 10.0 + rng.Float64()*40.0 // 10-50x
	window := 1800 // 30 minutes
	var txns []Transaction

	for i := 0; i < count; i++ {
		txn := Transaction{
			TxnID:       *nextID,
			MerchantID:  m.ID,
			AmountPaise: int64(float64(m.AvgAmountPaise) * multiplier),
			Timestamp:   startTime + int64(rng.Intn(window)),
			Method:      weightedChoice(rng, []string{"upi", "card", "netbanking"}, []float64{0.6, 0.3, 0.1}),
			Category:    m.Category,
		}
		txns = append(txns, txn)
		*nextID++
	}
	return txns
}

// generateVelocitySpikeFraud: 10x normal rate for 1-2 hours, normal amounts.
func generateVelocitySpikeFraud(rng *rand.Rand, m MerchantConfig, startTime int64, nextID *int64) []Transaction {
	durationSec := 3600 + rng.Intn(3600) // 1-2 hours
	spikeRate := m.TxnsPerHour * (8.0 + rng.Float64()*4.0) // 8-12x
	avgGap := 3600.0 / spikeRate
	var txns []Transaction

	currentTime := float64(startTime)
	endTime := float64(startTime + int64(durationSec))

	for currentTime < endTime {
		gap := rng.ExpFloat64() * avgGap
		currentTime += gap
		if currentTime >= endTime {
			break
		}

		// Normal-looking amounts
		amount := sampleLogNormal(rng, float64(m.AvgAmountPaise), m.StdAmountPaise)
		if amount < 100 {
			amount = 100
		}

		txn := Transaction{
			TxnID:       *nextID,
			MerchantID:  m.ID,
			AmountPaise: amount,
			Timestamp:   int64(currentTime),
			Method:      weightedChoice(rng, []string{"upi", "card", "netbanking"}, []float64{0.6, 0.3, 0.1}),
			Category:    m.Category,
		}
		txns = append(txns, txn)
		*nextID++
	}
	return txns
}

// generateGradualCreepFraud creates 20-50 transactions that slowly increase in amount
// over 3-5 days. Starts near the merchant's normal average and drifts upward by a few
// percent per transaction. Uses normal inter-arrival times and mixed payment methods
// to look realistic — the key signal is the monotonic amount drift, not a sudden spike.
func generateGradualCreepFraud(rng *rand.Rand, m MerchantConfig, startTime int64, nextID *int64) []Transaction {
	count := 20 + rng.Intn(31) // 20-50 transactions
	durationDays := 3 + rng.Intn(3) // 3-5 days
	durationSec := durationDays * 86400
	creepRate := 0.02 + rng.Float64()*0.03 // 2-5% per transaction

	var txns []Transaction
	methods := []string{"upi", "card", "netbanking"}
	methodWeights := []float64{0.60, 0.30, 0.10}

	// Space transactions roughly evenly across the duration, with jitter
	avgGapSec := float64(durationSec) / float64(count)

	currentTime := float64(startTime)
	baseAmount := float64(m.AvgAmountPaise)

	for i := 0; i < count; i++ {
		// Amount creeps up by creepRate per transaction
		amount := baseAmount * math.Pow(1.0+creepRate, float64(i))
		// Add small noise (±5%) to make it less perfectly monotonic
		noise := 0.95 + rng.Float64()*0.10
		amount *= noise

		if amount < 100 {
			amount = 100
		}

		txn := Transaction{
			TxnID:       *nextID,
			MerchantID:  m.ID,
			AmountPaise: int64(amount),
			Timestamp:   int64(currentTime),
			Method:      weightedChoice(rng, methods, methodWeights),
			Category:    m.Category,
		}
		txns = append(txns, txn)
		*nextID++

		// Move forward in time with jitter
		gap := avgGapSec * (0.5 + rng.Float64()) // 50-150% of average gap
		currentTime += gap
	}
	return txns
}

// GenerateHeldOutCreep creates a separate dataset containing ONLY normal traffic
// plus gradual_creep fraud. No card_testing, amount_spike, or velocity_spike.
// This is used for held-out evaluation: the detector was never tuned for this pattern.
func GenerateHeldOutCreep(cfg GeneratorConfig) (totalTxns int, fraudTxns int, err error) {
	rng := rand.New(rand.NewSource(cfg.Seed))

	if err := os.MkdirAll(cfg.OutputDir, 0755); err != nil {
		return 0, 0, fmt.Errorf("creating output dir: %w", err)
	}

	// Build merchant profiles
	merchants := buildMerchants(rng, cfg.NumMerchants)

	// Base timestamp: Aug 1 2026 00:00:00 IST
	baseTime := int64(1785346200)

	// Generate normal transactions for all merchants
	var allTxns []Transaction
	var allLabels []FraudLabel
	txnID := int64(1)

	for _, m := range merchants {
		txns := generateNormalTraffic(rng, m, baseTime, cfg.DurationHours, &txnID)
		for _, t := range txns {
			allTxns = append(allTxns, t)
			allLabels = append(allLabels, FraudLabel{TxnID: t.TxnID, IsFraud: false, FraudType: ""})
		}
	}

	// Inject ONLY gradual_creep fraud into a subset of merchants
	numFraudMerchants := int(float64(len(merchants)) * cfg.FraudRate)
	perm := rng.Perm(len(merchants))
	for i := 0; i < numFraudMerchants && i < len(perm); i++ {
		m := merchants[perm[i]]
		// Pick a random time in the second half of the timeline
		fraudStart := baseTime + int64(cfg.DurationHours*3600/2) + int64(rng.Intn(cfg.DurationHours*3600/4))

		fraudTxnsList := generateGradualCreepFraud(rng, m, fraudStart, &txnID)
		for _, t := range fraudTxnsList {
			allTxns = append(allTxns, t)
			allLabels = append(allLabels, FraudLabel{TxnID: t.TxnID, IsFraud: true, FraudType: "gradual_creep"})
			fraudTxns++
		}
	}

	// Sort by timestamp
	sort.Slice(allTxns, func(i, j int) bool { return allTxns[i].Timestamp < allTxns[j].Timestamp })

	// Build label lookup
	labelMap := make(map[int64]FraudLabel, len(allLabels))
	for _, l := range allLabels {
		labelMap[l.TxnID] = l
	}

	// Write transactions CSV
	txnFile, err := os.Create(fmt.Sprintf("%s/transactions.csv", cfg.OutputDir))
	if err != nil {
		return 0, 0, err
	}
	defer txnFile.Close()
	txnWriter := csv.NewWriter(txnFile)
	defer txnWriter.Flush()

	for _, t := range allTxns {
		if err := txnWriter.Write([]string{
			strconv.FormatInt(t.TxnID, 10),
			t.MerchantID,
			strconv.FormatInt(t.AmountPaise, 10),
			strconv.FormatInt(t.Timestamp, 10),
			t.Method,
			t.Category,
		}); err != nil {
			return 0, 0, fmt.Errorf("writing transaction CSV: %w", err)
		}
	}

	// Write labels CSV
	labelFile, err := os.Create(fmt.Sprintf("%s/labels.csv", cfg.OutputDir))
	if err != nil {
		return 0, 0, err
	}
	defer labelFile.Close()
	labelWriter := csv.NewWriter(labelFile)
	defer labelWriter.Flush()

	for _, t := range allTxns {
		l := labelMap[t.TxnID]
		isFraud := "0"
		if l.IsFraud {
			isFraud = "1"
		}
		labelWriter.Write([]string{
			strconv.FormatInt(t.TxnID, 10),
			isFraud,
			l.FraudType,
		})
	}

	return len(allTxns), fraudTxns, nil
}

// --- Utility Functions ---

func timeOfDayWeight(hour int) float64 {
	// Low at night (0-6), ramp up morning (7-11), peak afternoon (12-20), wind down (21-23)
	weights := []float64{
		0.05, 0.03, 0.02, 0.02, 0.03, 0.05, // 0-5 AM
		0.15, 0.30, 0.50, 0.70, 0.85, 0.90,  // 6-11 AM
		0.95, 1.00, 1.00, 0.95, 0.90, 0.95,  // 12-5 PM
		1.00, 0.90, 0.70, 0.50, 0.30, 0.15,  // 6-11 PM
	}
	if hour < 0 || hour > 23 {
		return 0.5
	}
	return weights[hour]
}

func sampleLogNormal(rng *rand.Rand, mean, std float64) int64 {
	if mean <= 0 {
		return 100 // minimum ₹1
	}
	// Convert mean/std of the actual distribution to log-space parameters
	variance := std * std
	denominator := math.Sqrt(variance + mean*mean)
	if denominator <= 0 {
		return int64(mean)
	}
	mu := math.Log(mean * mean / denominator)
	sigma := math.Sqrt(math.Log(1.0 + variance/(mean*mean)))
	if math.IsNaN(mu) || math.IsNaN(sigma) || sigma <= 0 {
		return int64(mean)
	}
	sample := math.Exp(mu + sigma*rng.NormFloat64())
	if math.IsNaN(sample) || math.IsInf(sample, 0) || sample < 0 {
		return int64(mean)
	}
	return int64(sample)
}

func weightedChoice(rng *rand.Rand, options []string, weights []float64) string {
	r := rng.Float64()
	cumulative := 0.0
	for i, w := range weights {
		cumulative += w
		if r <= cumulative {
			return options[i]
		}
	}
	return options[len(options)-1]
}
