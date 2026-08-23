package main

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/Samriddha9619/risk_ledger/data"
	"github.com/Samriddha9619/risk_ledger/detector"
	"github.com/Samriddha9619/risk_ledger/eval"
	"github.com/Samriddha9619/risk_ledger/ledger"
)

const (
	defaultDataDir = "generated_data"
	defaultDBDir   = "risk_ledger_db"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "generate":
		cmdGenerate()
	case "load":
		cmdLoad()
	case "train":
		fmt.Println("Training is done as part of 'eval'. Run: risk_ledger eval")
	case "eval":
		cmdEval()
	case "run":
		cmdRun()
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Risk Ledger — Fraud Spike Detector")
	fmt.Println()
	fmt.Println("Usage: risk_ledger <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  generate  Generate synthetic transaction data")
	fmt.Println("  load      Load generated data into GoDB")
	fmt.Println("  eval      Train detector and evaluate on held-out test set")
	fmt.Println("  run       Full pipeline: generate → load → eval")
}

func cmdGenerate() {
	cfg := data.DefaultConfig()
	fmt.Println("[1/1] Generating synthetic transaction data...")
	start := time.Now()
	total, fraud, err := data.Generate(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Generated %d transactions (%d fraud, %.1f%%) in %v\n",
		total, fraud, float64(fraud)/float64(total)*100, time.Since(start))
	fmt.Printf("  Output: %s/transactions.csv, %s/labels.csv\n", cfg.OutputDir, cfg.OutputDir)
}

func cmdLoad() {
	fmt.Println("[1/2] Initializing GoDB storage engine...")
	store, err := ledger.NewTransactionStore(defaultDBDir, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating store: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("[2/2] Loading transactions into GoDB...")
	start := time.Now()
	count, err := store.LoadFromCSV(defaultDataDir + "/transactions.csv")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading: %v\n", err)
		os.Exit(1)
	}

	if err := store.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "Error flushing: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("  Loaded %d transactions into GoDB in %v\n", count, time.Since(start))
}

func cmdEval() {
	fmt.Println("[1/4] Loading data from GoDB...")
	store, err := ledger.NewTransactionStore(defaultDBDir, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening store: %v\n", err)
		os.Exit(1)
	}

	txns, err := store.ScanAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error scanning: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Loaded %d transactions from GoDB\n", len(txns))

	labels, err := ledger.LoadLabelsFromCSV(defaultDataDir + "/labels.csv")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading labels: %v\n", err)
		os.Exit(1)
	}

	runEval(txns, labels)
}

func cmdRun() {
	fmt.Println("╔══════════════════════════════════════════╗")
	fmt.Println("║  Risk Ledger — Full Pipeline             ║")
	fmt.Println("╚══════════════════════════════════════════╝")
	fmt.Println()

	// Step 1: Generate
	cfg := data.DefaultConfig()
	fmt.Println("[1/4] Generating synthetic data...")
	start := time.Now()
	total, fraud, err := data.Generate(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  %d transactions (%d fraud) in %v\n", total, fraud, time.Since(start))

	// Step 2: Load into GoDB
	fmt.Println("[2/4] Loading into GoDB storage engine...")
	start = time.Now()
	store, err := ledger.NewTransactionStore(defaultDBDir, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	count, err := store.LoadFromCSV(cfg.OutputDir + "/transactions.csv")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if err := store.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  %d rows loaded in %v\n", count, time.Since(start))

	// Step 3: Read back from GoDB
	fmt.Println("[3/4] Reading back from GoDB...")
	start = time.Now()
	txns, err := store.ScanAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  %d transactions scanned in %v\n", len(txns), time.Since(start))

	labels, err := ledger.LoadLabelsFromCSV(cfg.OutputDir + "/labels.csv")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Step 4: Train + Eval
	runEval(txns, labels)
}

func runEval(txns []data.Transaction, labels map[int64]data.FraudLabel) {
	// Sort by timestamp for temporal split
	sort.Slice(txns, func(i, j int) bool { return txns[i].Timestamp < txns[j].Timestamp })

	// 70/30 temporal split
	splitIdx := len(txns) * 70 / 100
	trainSet := txns[:splitIdx]
	testSet := txns[splitIdx:]

	fmt.Printf("[4/4] Training on %d txns, evaluating on %d txns...\n", len(trainSet), len(testSet))
	start := time.Now()

	// Initialize engine
	engine := detector.NewDetectionEngine()

	// Training phase: feed training data to build profiles + collect features
	var trainingFeatures [][]float64
	for _, txn := range trainSet {
		label := labels[txn.TxnID]
		features := engine.ProcessTraining(txn)
		// Only train forest on non-fraud transactions (normal behavior)
		if !label.IsFraud {
			featureSlice := make([]float64, detector.NumFeatures)
			copy(featureSlice, features[:])
			trainingFeatures = append(trainingFeatures, featureSlice)
		}
	}

	// Train Isolation Forest
	fmt.Printf("  Training Isolation Forest on %d normal samples...\n", len(trainingFeatures))
	engine.TrainForest(trainingFeatures)

	// Scoring phase: score all test transactions
	var alerts []detector.Alert
	amounts := make(map[int64]int64)
	for _, txn := range testSet {
		alert := engine.Score(txn)
		alerts = append(alerts, alert)
		amounts[txn.TxnID] = txn.AmountPaise
	}
	fmt.Printf("  Scored %d test transactions in %v\n", len(alerts), time.Since(start))

	// Also generate stats-only and ML-only scores for comparison
	statsAlerts := make([]detector.Alert, len(alerts))
	mlAlerts := make([]detector.Alert, len(alerts))
	for i, a := range alerts {
		statsAlerts[i] = a
		statsAlerts[i].CombinedScore = a.StatScore
		mlAlerts[i] = a
		mlAlerts[i].CombinedScore = a.ForestScore
	}

	// Sweep thresholds
	combinedResults := eval.SweepThresholds(alerts, labels, amounts, 100)
	statsResults := eval.SweepThresholds(statsAlerts, labels, amounts, 100)
	mlResults := eval.SweepThresholds(mlAlerts, labels, amounts, 100)

	bestCombined := eval.FindBestF1(combinedResults)
	bestStats := eval.FindBestF1(statsResults)
	bestML := eval.FindBestF1(mlResults)
	optimal := eval.FindOptimalThreshold(combinedResults)

	// Per-pattern breakdown
	patterns := eval.EvaluatePerPattern(alerts, labels, bestCombined.Threshold)

	// Count unique merchants
	merchantSet := make(map[string]bool)
	for _, txn := range txns {
		merchantSet[txn.MerchantID] = true
	}

	// Count fraud
	fraudCount := 0
	for _, l := range labels {
		if l.IsFraud {
			fraudCount++
		}
	}
	fraudRate := float64(fraudCount) / float64(len(txns))

	// Print report
	eval.PrintReport(
		len(txns),
		len(merchantSet),
		fraudRate,
		len(trainSet),
		len(testSet),
		bestStats,
		bestML,
		bestCombined,
		bestCombined,
		optimal,
		patterns,
	)
}
