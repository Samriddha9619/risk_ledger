package eval

import (
	"fmt"
	"strings"
)

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
	fmt.Println(sep)
}

func printRow(name string, r EvalResult) {
	// Normalize cost to daily estimate (assume test set covers ~1 day)
	costPerDay := r.TotalCost / 100.0 // paise to rupees
	fmt.Printf("  │ %-11s │   %.3f   │ %.3f  │ %.3f │ ₹%-11.0f│\n",
		name, r.Precision, r.Recall, r.F1, costPerDay)
}
