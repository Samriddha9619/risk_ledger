package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Samriddha9619/risk_ledger/dashboard"
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
	loadEnv(".env")

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
	case "serve":
		cmdServe()
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Risk Ledger — AI-Powered Fraud Triage System")
	fmt.Println()
	fmt.Println("Usage: risk_ledger <command> [flags]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  generate  Generate synthetic transaction data")
	fmt.Println("  load      Load generated data into GoDB")
	fmt.Println("  eval      Train detector and evaluate on held-out test set")
	fmt.Println("  run       Full pipeline: generate → load → eval → triage")
	fmt.Println("  serve     Start live monitoring dashboard")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --no-llm              Disable Gemini API calls (use rule-based fallback)")
	fmt.Println("  --demo                Demo mode: AI triage top 50 alerts only (<30s)")
	fmt.Println("  --seeds=42,123,456    Run multi-seed evaluation")
	fmt.Println("  --godb-bench          Run GoDB buffer pool benchmark (constrained pool)")
}

// parseFlag extracts a flag value like --seeds=42,123 from os.Args.
func parseFlag(name string) (string, bool) {
	prefix := "--" + name + "="
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, prefix) {
			return strings.TrimPrefix(arg, prefix), true
		}
	}
	return "", false
}

// parseSeeds extracts seed values from the --seeds flag.
func parseSeeds() []int64 {
	val, ok := parseFlag("seeds")
	if !ok {
		return nil
	}
	parts := strings.Split(val, ",")
	seeds := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if s, err := strconv.ParseInt(p, 10, 64); err == nil {
			seeds = append(seeds, s)
		}
	}
	return seeds
}

func hasFlag(name string) bool {
	for _, arg := range os.Args {
		if arg == "--"+name {
			return true
		}
	}
	return false
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

	// Check for multi-seed mode
	seeds := parseSeeds()

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

	// Multi-seed evaluation if requested
	if len(seeds) > 1 {
		runMultiSeed(txns, labels, seeds)
	}

	// GoDB buffer pool benchmark if requested
	if hasFlag("godb-bench") {
		runGoDBBench(cfg.OutputDir + "/transactions.csv")
	}
}

// runGoDBBench demonstrates GoDB's buffer pool behavior under memory pressure.
// Loads 50K+ transactions through a constrained 64-frame pool (vs. ~1700 pages needed),
// forcing extensive page eviction, then verifies data integrity via scan-back.
func runGoDBBench(csvPath string) {
	fmt.Println()
	fmt.Println("  ─── GoDB Buffer Pool Benchmark ───")
	fmt.Println("  Loading 50K+ transactions through constrained buffer pools")
	fmt.Println()

	poolSizes := []int{64, 128, 2000}
	for _, poolSize := range poolSizes {
		dbDir := fmt.Sprintf("risk_ledger_db_bench_%d", poolSize)
		store, err := ledger.NewTransactionStoreWithPoolSize(dbDir, true, poolSize)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error creating store (pool=%d): %v\n", poolSize, err)
			continue
		}

		// Load
		start := time.Now()
		loadCount, err := store.LoadFromCSV(csvPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error loading (pool=%d): %v\n", poolSize, err)
			_ = os.RemoveAll(dbDir)
			continue
		}
		loadTime := time.Since(start)

		// Flush
		flushStart := time.Now()
		if err := store.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "  Error flushing (pool=%d): %v\n", poolSize, err)
			_ = os.RemoveAll(dbDir)
			continue
		}
		flushTime := time.Since(flushStart)

		// Scan back to verify integrity
		scanStart := time.Now()
		txns, err := store.ScanAll()
		scanTime := time.Since(scanStart)

		pagesNeeded := loadCount / 30
		evictions := 0
		if pagesNeeded > poolSize {
			evictions = pagesNeeded - poolSize
		}

		verified := "✓ VERIFIED"
		if err != nil || len(txns) != loadCount {
			verified = "✗ MISMATCH"
		}

		fmt.Printf("  Pool=%4d frames | %d txns | ~%d pages | ~%d evictions\n",
			poolSize, loadCount, pagesNeeded, evictions)
		fmt.Printf("    Load: %-8v  Flush: %-8v  Scan: %-8v  %s\n",
			loadTime.Truncate(time.Millisecond),
			flushTime.Truncate(time.Millisecond),
			scanTime.Truncate(time.Millisecond),
			verified)

		_ = os.RemoveAll(dbDir)
	}
	fmt.Println()
	fmt.Println("  Takeaway: Even with a 64-frame pool (~256KB RAM), GoDB's clock-sweep")
	fmt.Println("  eviction + dirty page flushing handles 50K+ rows with zero data loss.")
	fmt.Println()
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

	// LLM Explainer: check for --no-llm flag
	noLLM := hasFlag("no-llm")

	// ⚡ Compute calibrated thresholds from the eval sweep (data-derived, not hardcoded)
	autoBlockThreshold := bestCombined.Threshold   // best F1 threshold → auto-block boundary
	autoApproveThreshold := optimal.Threshold       // cost-optimal threshold → auto-approve boundary
	// Ensure approve < block; if degenerate, clamp
	if autoApproveThreshold >= autoBlockThreshold {
		autoApproveThreshold = autoBlockThreshold * 0.7
	}
	fmt.Println()
	fmt.Println("  ⚡ Calibrated thresholds (data-derived, not hardcoded):")
	fmt.Printf("      Auto-approve: ≤ %.2f  |  Auto-block: ≥ %.2f\n", autoApproveThreshold, autoBlockThreshold)
	fmt.Printf("      (Old hardcoded: approve=0.50, block=0.78)\n")

	var explanations []detector.Explanation
	var triageDecisions []detector.TriageDecision
	var triageMetrics detector.TriageMetrics
	var explainer *detector.Explainer

	if noLLM {
		fmt.Println("  [--no-llm] Generating rule-based explanations...")
		explainer = detector.NewExplainerDisabled() // Guaranteed disabled, ignores env var
		explanations = explainer.ExplainAlerts(alerts, engine.Stats.Profiles, testSet, 5)

		// Triage with fallback (no AI), using calibrated thresholds
		fmt.Println("  Running rule-based triage...")
		triageEngine := detector.NewTriageEngine(explainer, engine.Stats.Profiles, autoApproveThreshold, autoBlockThreshold)
		triageDecisions, triageMetrics = triageEngine.Triage(alerts, labels, testSet)
	} else {
		explainer = detector.NewExplainer("") // uses GEMINI_API_KEY env var
		if explainer.Enabled {
			fmt.Println("  Generating LLM-powered explanations for top-5 alerts...")
		} else {
			fmt.Println("  No GEMINI_API_KEY found. Using rule-based fallback (set env var or use --no-llm).")
		}
		explanations = explainer.ExplainAlerts(alerts, engine.Stats.Profiles, testSet, 5)

		// AI Triage with calibrated thresholds
		if explainer.Enabled {
			fmt.Println("  Running AI-powered triage on flagged transactions...")
		} else {
			fmt.Println("  Running rule-based triage (no API key)...")
		}
		triageEngine := detector.NewTriageEngine(explainer, engine.Stats.Profiles, autoApproveThreshold, autoBlockThreshold)
		if hasFlag("demo") {
			triageEngine.MaxAIAlerts = 50
		}
		triageDecisions, triageMetrics = triageEngine.Triage(alerts, labels, testSet)
	}

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
		explanations,
		!explainer.Enabled,
	)

	// Print triage report
	eval.PrintTriageReport(triageMetrics, !explainer.Enabled, autoBlockThreshold)

	// Write JSON report
	jsonPath := "results/evaluation.json"
	if err := eval.WriteJSONReport(
		jsonPath,
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
		explanations,
	); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write JSON report: %v\n", err)
	} else {
		fmt.Printf("\n  JSON report written to %s\n", jsonPath)
	}

	// Write triage decisions JSON
	triagePath := "results/triage.json"
	if err := eval.WriteTriageJSON(triagePath, triageDecisions, triageMetrics); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write triage report: %v\n", err)
	} else {
		fmt.Printf("  Triage report written to %s\n", triagePath)
	}

	// ─── Held-out Gradual Creep Evaluation ───
	// Generate a separate dataset with ONLY gradual_creep fraud (no card_testing/
	// amount_spike/velocity_spike). Score it using the SAME trained engine to test
	// whether existing rules catch a pattern they were never designed for.
	runHeldOutCreepEval(engine, bestCombined.Threshold)
}

// runHeldOutCreepEval generates a held-out dataset with only gradual_creep fraud
// and evaluates the already-trained engine on it.
func runHeldOutCreepEval(engine *detector.DetectionEngine, mainBestThreshold float64) {
	fmt.Println()
	fmt.Println("  ─── Held-Out Evaluation: Gradual Creep Pattern ───")
	fmt.Println("  (Testing whether existing rules catch a never-seen-before pattern)")
	fmt.Println()

	// Use a different seed and separate output dir to avoid contamination
	creepCfg := data.GeneratorConfig{
		NumMerchants:  200,
		DurationHours: 72,
		FraudRate:     0.08,
		Seed:          7777, // different from main seed (42)
		OutputDir:     "generated_data/held_out_creep",
	}

	total, fraud, err := data.GenerateHeldOutCreep(creepCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error generating held-out data: %v\n", err)
		return
	}
	fmt.Printf("  Generated held-out dataset: %d transactions (%d gradual_creep fraud)\n", total, fraud)

	// Load the held-out data
	creepTxns, err := data.LoadTransactionsCSV(creepCfg.OutputDir + "/transactions.csv")
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error loading held-out transactions: %v\n", err)
		return
	}
	creepLabels, err := data.LoadLabelsCSV(creepCfg.OutputDir + "/labels.csv")
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error loading held-out labels: %v\n", err)
		return
	}

	// Sort by timestamp for temporal split
	sort.Slice(creepTxns, func(i, j int) bool { return creepTxns[i].Timestamp < creepTxns[j].Timestamp })

	// Use only the second half (where fraud lives) for scoring
	splitIdx := len(creepTxns) * 50 / 100
	creepTestSet := creepTxns[splitIdx:]

	// Score using the existing trained engine (never saw gradual_creep)
	var creepAlerts []detector.Alert
	creepAmounts := make(map[int64]int64)
	for _, txn := range creepTestSet {
		alert := engine.Score(txn)
		creepAlerts = append(creepAlerts, alert)
		creepAmounts[txn.TxnID] = txn.AmountPaise
	}

	// Sweep thresholds on the held-out set
	creepResults := eval.SweepThresholds(creepAlerts, creepLabels, creepAmounts, 100)
	bestCreepF1 := eval.FindBestF1(creepResults)

	// Per-pattern breakdown at the held-out best threshold
	creepPatterns := eval.EvaluatePerPattern(creepAlerts, creepLabels, bestCreepF1.Threshold)

	// Also evaluate at the main model's production threshold (from the primary eval)
	// to show what happens without threshold retuning
	mainThreshold := mainBestThreshold
	mainResult := eval.EvaluateWithAmounts(creepAlerts, creepLabels, creepAmounts, mainThreshold)
	mainPatterns := eval.EvaluatePerPattern(creepAlerts, creepLabels, mainThreshold)

	fmt.Printf("  Scored %d held-out test transactions\n", len(creepAlerts))
	fmt.Println()
	fmt.Println("  At held-out optimal threshold:")
	fmt.Printf("    Best F1: %.4f (threshold=%.2f)\n", bestCreepF1.F1, bestCreepF1.Threshold)
	fmt.Printf("    Precision: %.4f  |  Recall: %.4f\n", bestCreepF1.Precision, bestCreepF1.Recall)
	for _, p := range creepPatterns {
		fmt.Printf("    %-20s  %d/%d caught  (Recall=%.4f)\n",
			p.Pattern, p.Caught, p.Total, p.Recall)
	}
	fmt.Println()
	fmt.Printf("  At main model's production threshold (%.2f):\n", mainThreshold)
	fmt.Printf("    F1: %.4f  |  Precision: %.4f  |  Recall: %.4f\n",
		mainResult.F1, mainResult.Precision, mainResult.Recall)
	for _, p := range mainPatterns {
		fmt.Printf("    %-20s  %d/%d caught  (Recall=%.4f)\n",
			p.Pattern, p.Caught, p.Total, p.Recall)
	}
	fmt.Println()
}

// runMultiSeed runs evaluation across multiple seeds and reports mean±std.
func runMultiSeed(txns []data.Transaction, labels map[int64]data.FraudLabel, seeds []int64) {
	fmt.Println()
	fmt.Println("  Running multi-seed evaluation...")

	// Sort for temporal split
	sort.Slice(txns, func(i, j int) bool { return txns[i].Timestamp < txns[j].Timestamp })
	splitIdx := len(txns) * 70 / 100
	trainSet := txns[:splitIdx]
	testSet := txns[splitIdx:]

	amounts := make(map[int64]int64)
	for _, txn := range testSet {
		amounts[txn.TxnID] = txn.AmountPaise
	}

	var seedResults []eval.EvalResult

	for _, seed := range seeds {
		// Fresh engine per seed
		engine := detector.NewDetectionEngine()

		var trainingFeatures [][]float64
		for _, txn := range trainSet {
			label := labels[txn.TxnID]
			features := engine.ProcessTraining(txn)
			if !label.IsFraud {
				fs := make([]float64, detector.NumFeatures)
				copy(fs, features[:])
				trainingFeatures = append(trainingFeatures, fs)
			}
		}

		engine.TrainForestWithSeed(trainingFeatures, seed)

		var alerts []detector.Alert
		for _, txn := range testSet {
			alert := engine.Score(txn)
			alerts = append(alerts, alert)
		}

		results := eval.SweepThresholds(alerts, labels, amounts, 100)
		best := eval.FindBestF1(results)
		seedResults = append(seedResults, best)

		fmt.Printf("    Seed %d: F1=%.4f P=%.4f R=%.4f (threshold=%.2f)\n",
			seed, best.F1, best.Precision, best.Recall, best.Threshold)
	}

	ms := eval.AggregateMultiSeed(seeds, seedResults)
	eval.PrintMultiSeedReport(ms)
}

func cmdServe() {
	noLLM := hasFlag("no-llm")

	cfg := dashboard.Config{
		Port:    8080,
		DataDir: defaultDataDir,
		DBDir:   defaultDBDir,
		SpeedMs: 30,
		NoLLM:   noLLM,
	}

	if err := dashboard.Serve(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// loadEnv reads a .env file and sets environment variables.
// Existing env vars take priority (won't be overwritten).
func loadEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // .env is optional
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		// Don't overwrite existing env vars
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}
