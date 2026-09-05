package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Samriddha9619/risk_ledger/detector"
)

// JSONReport is the machine-readable evaluation output.
type JSONReport struct {
	Dataset      DatasetInfo         `json:"dataset"`
	Layers       map[string]Metrics  `json:"layers"`
	BestF1       ThresholdResult     `json:"best_f1"`
	CostOptimal  ThresholdResult     `json:"cost_optimal"`
	Patterns     []PatternMetrics    `json:"patterns"`
	Explanations []ExplanationOutput `json:"explanations,omitempty"`
}

type DatasetInfo struct {
	TotalTxns    int     `json:"total_transactions"`
	NumMerchants int     `json:"num_merchants"`
	FraudRate    float64 `json:"fraud_rate"`
	TrainSize    int     `json:"train_size"`
	TestSize     int     `json:"test_size"`
}

type Metrics struct {
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
	CostRupees float64 `json:"cost_rupees_per_day"`
}

type ThresholdResult struct {
	Threshold  float64 `json:"threshold"`
	Precision  float64 `json:"precision"`
	Recall     float64 `json:"recall"`
	F1         float64 `json:"f1"`
	FPCost     float64 `json:"fp_cost_rupees"`
	FNCost     float64 `json:"fn_cost_rupees"`
	TotalCost  float64 `json:"total_cost_rupees"`
}

type PatternMetrics struct {
	Pattern string  `json:"pattern"`
	Total   int     `json:"total"`
	Caught  int     `json:"caught"`
	Recall  float64 `json:"recall"`
}

type ExplanationOutput struct {
	MerchantID  string   `json:"merchant_id"`
	Summary     string   `json:"summary"`
	RiskFactors []string `json:"risk_factors"`
	Action      string   `json:"action"`
}

// PrintReport outputs the evaluation report to stdout.
func PrintReport(
	totalTxns int,
	numMerchants int,
	fraudRate float64,
	trainSize int,
	testSize int,
	statsOnly EvalResult,
	mlOnly EvalResult,
	combined EvalResult,
	bestF1 EvalResult,
	optimal EvalResult,
	patterns []PerPatternResult,
	explanations []detector.Explanation,
	noLLM bool,
) {
	sep := strings.Repeat("═", 60)
	fmt.Println()
	fmt.Println(sep)
	fmt.Println("  RISK LEDGER — Fraud Spike Detector — Evaluation Report")
	fmt.Println(sep)
	fmt.Println()

	fmt.Printf("  Dataset:     %d transactions | %d merchants | %.1f%% fraud rate\n",
		totalTxns, numMerchants, fraudRate*100)
	fmt.Printf("  Split:       %d train (70%%) | %d test (30%%) — temporal split\n",
		trainSize, testSize)
	fmt.Println()

	// Layer comparison table
	fmt.Println("  ┌─────────────┬───────────┬────────┬───────┬──────────────┐")
	fmt.Println("  │ Layer       │ Precision │ Recall │ F1    │ Cost (₹/day) │")
	fmt.Println("  ├─────────────┼───────────┼────────┼───────┼──────────────┤")
	printRow("Stats only", statsOnly)
	printRow("ML only", mlOnly)
	printRow("Combined", combined)
	fmt.Println("  └─────────────┴───────────┴────────┴───────┴──────────────┘")
	fmt.Println()

	// Best F1
	fmt.Printf("  Best F1 threshold:     %.2f (F1=%.3f)\n", bestF1.Threshold, bestF1.F1)
	fmt.Printf("  Cost-optimal threshold: %.2f (Cost=₹%.0f/day)\n",
		optimal.Threshold, optimal.TotalCost/100.0)
	fmt.Println()

	// Confusion matrix at best F1
	fmt.Printf("  Confusion Matrix (threshold=%.2f):\n", bestF1.Threshold)
	fmt.Println("                Predicted")
	fmt.Println("                Fraud    Legit")
	fmt.Printf("  Actual Fraud │ %5d  │ %5d │\n", bestF1.TP, bestF1.FN)
	fmt.Printf("  Actual Legit │ %5d  │ %5d │\n", bestF1.FP, bestF1.TN)
	fmt.Println()

	// Per-pattern breakdown
	if len(patterns) > 0 {
		fmt.Println("  Per-Pattern Recall (at best F1 threshold):")
		for _, p := range patterns {
			fmt.Printf("    %-20s  %d/%d caught  (Recall=%.2f)\n",
				p.Pattern, p.Caught, p.Total, p.Recall)
		}
		fmt.Println()
	}

	// False-positive cost analysis
	fmt.Println("  False-Positive Cost Analysis:")
	fmt.Printf("    FP cost model:  2%% of blocked transaction amount\n")
	fmt.Printf("    FN cost model:  100%% of missed fraud amount\n")
	fmt.Printf("    At best F1:     ₹%.0f FP cost + ₹%.0f FN cost = ₹%.0f total\n",
		bestF1.FPCost/100.0, bestF1.FNCost/100.0, bestF1.TotalCost/100.0)
	fmt.Printf("    At optimal:     ₹%.0f FP cost + ₹%.0f FN cost = ₹%.0f total\n",
		optimal.FPCost/100.0, optimal.FNCost/100.0, optimal.TotalCost/100.0)
	fmt.Println()

	// LLM Explanations
	if len(explanations) > 0 {
		if noLLM {
			fmt.Println("  ─── Rule-Based Alert Explanations (Top Alerts) ───")
		} else {
			fmt.Println("  ─── AI-Powered Alert Explanations (Top Alerts) ───")
		}
		fmt.Println()
		for i, exp := range explanations {
			if exp.Error != "" {
				fmt.Printf("  [%d] %s — Error: %s\n", i+1, exp.MerchantID, exp.Error)
				continue
			}
			fmt.Printf("  [%d] Merchant: %s\n", i+1, exp.MerchantID)
			fmt.Printf("      Summary: %s\n", exp.Summary)
			if len(exp.RiskFactors) > 0 {
				fmt.Printf("      Risk Factors:\n")
				for _, rf := range exp.RiskFactors {
					fmt.Printf("        • %s\n", rf)
				}
			}
			fmt.Printf("      Action:  %s\n", exp.Action)
			fmt.Println()
		}
	}

	fmt.Println(sep)
}

// WriteJSONReport writes the evaluation results to a JSON file.
func WriteJSONReport(
	path string,
	totalTxns int,
	numMerchants int,
	fraudRate float64,
	trainSize int,
	testSize int,
	statsOnly EvalResult,
	mlOnly EvalResult,
	combined EvalResult,
	bestF1 EvalResult,
	optimal EvalResult,
	patterns []PerPatternResult,
	explanations []detector.Explanation,
) error {
	if err := os.MkdirAll("results", 0755); err != nil {
		return err
	}

	report := JSONReport{
		Dataset: DatasetInfo{
			TotalTxns:    totalTxns,
			NumMerchants: numMerchants,
			FraudRate:    fraudRate,
			TrainSize:    trainSize,
			TestSize:     testSize,
		},
		Layers: map[string]Metrics{
			"stats_only": {statsOnly.Precision, statsOnly.Recall, statsOnly.F1, statsOnly.TotalCost / 100.0},
			"ml_only":    {mlOnly.Precision, mlOnly.Recall, mlOnly.F1, mlOnly.TotalCost / 100.0},
			"combined":   {combined.Precision, combined.Recall, combined.F1, combined.TotalCost / 100.0},
		},
		BestF1: ThresholdResult{
			bestF1.Threshold, bestF1.Precision, bestF1.Recall, bestF1.F1,
			bestF1.FPCost / 100.0, bestF1.FNCost / 100.0, bestF1.TotalCost / 100.0,
		},
		CostOptimal: ThresholdResult{
			optimal.Threshold, optimal.Precision, optimal.Recall, optimal.F1,
			optimal.FPCost / 100.0, optimal.FNCost / 100.0, optimal.TotalCost / 100.0,
		},
	}

	for _, p := range patterns {
		report.Patterns = append(report.Patterns, PatternMetrics{
			Pattern: p.Pattern, Total: p.Total, Caught: p.Caught, Recall: p.Recall,
		})
	}

	for _, exp := range explanations {
		if exp.Error == "" {
			report.Explanations = append(report.Explanations, ExplanationOutput{
				MerchantID:  exp.MerchantID,
				Summary:     exp.Summary,
				RiskFactors: exp.RiskFactors,
				Action:      exp.Action,
			})
		}
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func printRow(name string, r EvalResult) {
	costPerDay := r.TotalCost / 100.0
	fmt.Printf("  │ %-11s │   %.3f   │ %.3f  │ %.3f │ ₹%-11.0f│\n",
		name, r.Precision, r.Recall, r.F1, costPerDay)
}

// PrintTriageReport outputs the triage metrics with honest labeling.
// noLLM indicates whether the LLM was disabled (labels will say "Rule-based" instead of "AI").
// autoBlockThreshold is the actual threshold used for auto-blocking.
func PrintTriageReport(m detector.TriageMetrics, noLLM bool, autoBlockThreshold float64) {
	sep := strings.Repeat("─", 60)
	fmt.Println()
	fmt.Println("  " + sep)
	if noLLM {
		fmt.Println("  RULE-BASED TRIAGE REPORT — Verification Workload Reduction")
	} else {
		fmt.Println("  AI TRIAGE REPORT — Verification Workload Reduction")
	}
	fmt.Println("  " + sep)
	fmt.Println()

	fmt.Printf("  Total transactions:   %d\n", m.TotalTransactions)
	fmt.Printf("  Total flagged:        %d\n", m.TotalFlagged)
	fmt.Println()

	// Use honest labels: "Rule-based" when no LLM was involved
	decisionLabel := "AI"
	if noLLM {
		decisionLabel = "Rule-based"
	}

	if !noLLM && m.TotalFlagged > 0 {
		needsAI := m.TotalFlagged - m.AutoBlocked
		if needsAI > 0 {
			aiPct := float64(m.AITriaged) / float64(needsAI) * 100
			fmt.Printf("  AI-resolved: %d/%d (%.1f%%) — remaining routed to deterministic fallback (rate-limit/timeout)\n", m.AITriaged, needsAI, aiPct)
			fmt.Println()
		}
	}

	fmt.Println("  Resolution breakdown:")
	fmt.Printf("    Auto-blocked (score > %.2f):     %d\n", autoBlockThreshold, m.AutoBlocked)
	if m.AITriaged > 0 {
		fmt.Printf("    %s blocked:                      %d\n", decisionLabel, m.AIBlocked)
		fmt.Printf("    %s approved (false alarm):        %d\n", decisionLabel, m.AIApproved)
		fmt.Printf("    %s escalated → human queue:       %d\n", decisionLabel, m.AIEscalated)
	}
	if m.FallbackTriaged > 0 {
		fmt.Printf("    Fallback blocked:                 %d\n", m.FallbackBlocked)
		fmt.Printf("    Fallback approved (false alarm):   %d\n", m.FallbackApproved)
		fmt.Printf("    Fallback escalated → human queue:  %d\n", m.FallbackEscalated)
	}
	fmt.Println()

	totalResolved := m.AutoBlocked + m.AIBlocked + m.AIApproved + m.FallbackBlocked + m.FallbackApproved
	fmt.Printf("  ┌────────────────────────────────────────────┐\n")
	fmt.Printf("  │  WORKLOAD REDUCTION:  %.0f%%                  │\n", m.WorkloadReduction*100)
	fmt.Printf("  │  %d of %d alerts auto-resolved          │\n", totalResolved, m.TotalFlagged)
	fmt.Printf("  │  Human queue: %d transactions              │\n", m.HumanQueueSize)
	fmt.Printf("  └────────────────────────────────────────────┘\n")
	fmt.Println()

	fmt.Println("  Accuracy:")
	fmt.Printf("    Auto-block correct:   %d/%d\n", m.AutoBlockCorrect, m.AutoBlocked)
	if m.AutoBlocked > 0 {
		fmt.Printf("    Auto-block precision: %.1f%%\n", float64(m.AutoBlockCorrect)/float64(m.AutoBlocked)*100)
	}
	if m.AIBlocked+m.AIApproved > 0 {
		aiCorrect := m.AIBlockCorrect + m.AIApproveCorrect
		aiTotal := m.AIBlocked + m.AIApproved
		if aiTotal < 10 {
			fmt.Printf("    %s decision accuracy: insufficient sample (n=%d)\n", decisionLabel, aiTotal)
		} else {
			fmt.Printf("    %s decision accuracy: %d/%d (%.1f%%)\n", decisionLabel, aiCorrect, aiTotal, float64(aiCorrect)/float64(aiTotal)*100)
		}
	}
	if m.FallbackBlocked+m.FallbackApproved > 0 {
		fbCorrect := m.FallbackBlockCorrect + m.FallbackApproveCorrect
		fbTotal := m.FallbackBlocked + m.FallbackApproved
		if fbTotal < 10 {
			fmt.Printf("    Fallback decision accuracy: insufficient sample (n=%d)\n", fbTotal)
		} else {
			fmt.Printf("    Fallback decision accuracy: %d/%d (%.1f%%)\n", fbCorrect, fbTotal, float64(fbCorrect)/float64(fbTotal)*100)
		}
	}
	
	if m.AIApproveWrong > 0 {
		fmt.Printf("    ⚠ %s approved actual fraud: %d (DANGEROUS)\n", decisionLabel, m.AIApproveWrong)
	} else if m.AITriaged > 0 {
		fmt.Printf("    ✓ %s approved actual fraud: 0 (safe)\n", decisionLabel)
	}

	if m.FallbackApproveWrong > 0 {
		fmt.Printf("    ⚠ Fallback approved actual fraud: %d (DANGEROUS)\n", m.FallbackApproveWrong)
	} else if m.FallbackTriaged > 0 {
		fmt.Printf("    ✓ Fallback approved actual fraud: 0 (safe)\n")
	}
	fmt.Println()
}

// WriteTriageJSON writes triage decisions and metrics to a JSON file.
func WriteTriageJSON(path string, decisions []detector.TriageDecision, metrics detector.TriageMetrics) error {
	if err := os.MkdirAll("results", 0755); err != nil {
		return err
	}

	report := struct {
		Metrics   detector.TriageMetrics    `json:"metrics"`
		Decisions []detector.TriageDecision `json:"decisions"`
	}{
		Metrics:   metrics,
		Decisions: decisions,
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// PrintMultiSeedReport outputs the multi-seed evaluation summary.
func PrintMultiSeedReport(ms MultiSeedResult) {
	sep := strings.Repeat("─", 60)
	fmt.Println()
	fmt.Println("  " + sep)
	fmt.Println("  MULTI-SEED VALIDATION")
	fmt.Println("  " + sep)
	fmt.Println()

	fmt.Printf("  Seeds tested: ")
	for i, s := range ms.Seeds {
		if i > 0 {
			fmt.Printf(", ")
		}
		fmt.Printf("%d", s)
	}
	fmt.Println()
	fmt.Println()

	fmt.Println("  ┌────────────┬────────────────┬────────────────┬────────────────┐")
	fmt.Println("  │ Seed       │ Precision      │ Recall         │ F1             │")
	fmt.Println("  ├────────────┼────────────────┼────────────────┼────────────────┤")
	for i, seed := range ms.Seeds {
		fmt.Printf("  │ %-10d │ %.4f         │ %.4f         │ %.4f         │\n",
			seed, ms.Precisions[i], ms.Recalls[i], ms.F1s[i])
	}
	fmt.Println("  ├────────────┼────────────────┼────────────────┼────────────────┤")
	fmt.Printf("  │ Mean       │ %.4f         │ %.4f         │ %.4f         │\n",
		ms.MeanPrecision, ms.MeanRecall, ms.MeanF1)
	fmt.Printf("  │ Std        │ %.4f         │ %.4f         │ %.4f         │\n",
		ms.StdPrecision, ms.StdRecall, ms.StdF1)
	fmt.Println("  └────────────┴────────────────┴────────────────┴────────────────┘")
	fmt.Println()

	if ms.StdF1 > 0.05 {
		fmt.Println("  ⚠ WARNING: F1 std > 0.05 — results may be sensitive to random seed")
	} else {
		fmt.Println("  ✓ F1 stable across seeds (std ≤ 0.05)")
	}
	fmt.Println()
}

