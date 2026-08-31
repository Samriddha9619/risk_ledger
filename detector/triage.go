package detector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Samriddha9619/risk_ledger/data"
)

// TriageDecision represents the AI's decision on a flagged transaction.
type TriageDecision struct {
	TxnID       int64    `json:"txn_id"`
	MerchantID  string   `json:"merchant_id"`
	AmountPaise int64    `json:"amount_paise"`
	Decision    string   `json:"decision"`    // "BLOCK", "APPROVE", "ESCALATE"
	Confidence  float64  `json:"confidence"`  // 0.0-1.0
	Reasoning   string   `json:"reasoning"`   // one-line explanation
	RiskFactors []string `json:"risk_factors"`
	Source      string   `json:"source"` // "auto_rule", "ai", "fallback"

	// Original detection scores for context
	StatScore     float64 `json:"stat_score"`
	ForestScore   float64 `json:"forest_score"`
	CombinedScore float64 `json:"combined_score"`
	RuleFired     string  `json:"rule_fired"`
	IsFraud       bool    `json:"is_fraud"` // ground truth (for evaluation)
}

// TriageMetrics tracks the performance of the triage system.
type TriageMetrics struct {
	TotalTransactions int     `json:"total_transactions"`
	TotalFlagged      int     `json:"total_flagged"`
	AutoBlocked       int     `json:"auto_blocked"`
	AutoApproved      int     `json:"auto_approved"`
	AITriaged         int     `json:"ai_triaged"`
	AIBlocked         int     `json:"ai_blocked"`
	AIApproved        int     `json:"ai_approved"`
	AIEscalated       int     `json:"ai_escalated"`
	FallbackTriaged   int     `json:"fallback_triaged"`
	FallbackBlocked   int     `json:"fallback_blocked"`
	FallbackApproved  int     `json:"fallback_approved"`
	FallbackEscalated int     `json:"fallback_escalated"`
	HumanQueueSize    int     `json:"human_queue_size"` // = AIEscalated + FallbackEscalated
	WorkloadReduction float64 `json:"workload_reduction"`
	AutoBlockCorrect  int     `json:"auto_block_correct"`
	AutoBlockWrong    int     `json:"auto_block_wrong"`
	AIBlockCorrect    int     `json:"ai_block_correct"`
	AIBlockWrong      int     `json:"ai_block_wrong"`
	AIApproveCorrect  int     `json:"ai_approve_correct"`
	AIApproveWrong    int     `json:"ai_approve_wrong"` // dangerous: AI approved actual fraud
	FallbackBlockCorrect   int     `json:"fallback_block_correct"`
	FallbackBlockWrong     int     `json:"fallback_block_wrong"`
	FallbackApproveCorrect int     `json:"fallback_approve_correct"`
	FallbackApproveWrong   int     `json:"fallback_approve_wrong"`
}

// TriageEngine performs AI-powered triage on flagged transactions.
type TriageEngine struct {
	explainer *Explainer
	profiles  map[string]*MerchantProfile

	// Thresholds for auto-resolution
	AutoBlockThreshold   float64 // above this → auto-block (no AI needed)
	AutoApproveThreshold float64 // below this → auto-approve (no AI needed)

	// Demo mode: if > 0, only triage the top N alerts via AI, rest → fallback.
	// Set to 50 for live demos (finishes in <30s), 0 for full batch run.
	MaxAIAlerts int
}

// NewTriageEngine creates a triage engine with data-derived thresholds.
// autoApprove: below this combined score → auto-approve (no AI needed).
// autoBlock: above this combined score → auto-block (no AI needed).
// These thresholds should be computed from eval.SweepThresholds, not hardcoded.
func NewTriageEngine(explainer *Explainer, profiles map[string]*MerchantProfile, autoApprove, autoBlock float64) *TriageEngine {
	// Sanity: ensure thresholds are well-ordered
	if autoApprove >= autoBlock {
		autoApprove = autoBlock * 0.7
	}
	return &TriageEngine{
		explainer:            explainer,
		profiles:             profiles,
		AutoBlockThreshold:   autoBlock,
		AutoApproveThreshold: autoApprove,
	}
}

// Triage processes a batch of alerts and makes decisions.
// Returns individual decisions and aggregate metrics.
func (te *TriageEngine) Triage(alerts []Alert, labels map[int64]data.FraudLabel, allTxns []data.Transaction) ([]TriageDecision, TriageMetrics) {
	var decisions []TriageDecision
	var metrics TriageMetrics
	metrics.TotalTransactions = len(allTxns)

	// Build txn lookup for AI context
	txnsByMerchant := make(map[string][]data.Transaction)
	for _, t := range allTxns {
		txnsByMerchant[t.MerchantID] = append(txnsByMerchant[t.MerchantID], t)
	}

	// Collect alerts that need AI triage
	var needsAI []Alert

	for _, alert := range alerts {
		label := labels[alert.TxnID]
		score := alert.CombinedScore

		if score >= te.AutoBlockThreshold {
			// Auto-block: score is very high, clearly suspicious
			d := TriageDecision{
				TxnID:         alert.TxnID,
				MerchantID:    alert.MerchantID,
				AmountPaise:   0, // filled later if needed
				Decision:      "BLOCK",
				Confidence:    math.Min(score, 1.0),
				Reasoning:     fmt.Sprintf("Auto-blocked: combined score %.2f exceeds threshold %.2f", score, te.AutoBlockThreshold),
				RiskFactors:   []string{fmt.Sprintf("Rule: %s", alert.StatAlert.RuleFired)},
				Source:        "auto_rule",
				StatScore:     alert.StatScore,
				ForestScore:   alert.ForestScore,
				CombinedScore: alert.CombinedScore,
				RuleFired:     alert.StatAlert.RuleFired,
				IsFraud:       label.IsFraud,
			}
			decisions = append(decisions, d)
			metrics.TotalFlagged++
			metrics.AutoBlocked++
			if label.IsFraud {
				metrics.AutoBlockCorrect++
			} else {
				metrics.AutoBlockWrong++
			}
		} else if score < te.AutoApproveThreshold {
			// Auto-approve: score is very low
			metrics.AutoApproved++
		} else {
			// Middle band: needs AI triage
			metrics.TotalFlagged++
			needsAI = append(needsAI, alert)
		}
	}

	ctx := context.Background()

	// AI triage on the middle band
	if te.explainer.Enabled && len(needsAI) > 0 {
		aiAlerts := needsAI
		var fallbackOverflow []Alert

		// Demo mode: only triage the top N highest-risk alerts via AI.
		// The rest go to rule-based fallback. This lets a live demo finish in <30s
		// while still showing real AI decisions happening.
		if te.MaxAIAlerts > 0 && len(needsAI) > te.MaxAIAlerts {
			sort.Slice(needsAI, func(i, j int) bool {
				return needsAI[i].CombinedScore > needsAI[j].CombinedScore
			})
			aiAlerts = needsAI[:te.MaxAIAlerts]
			fallbackOverflow = needsAI[te.MaxAIAlerts:]
			fmt.Printf("  [demo] AI triaging top %d of %d flagged alerts, %d → fallback\n",
				len(aiAlerts), len(needsAI), len(fallbackOverflow))
		}

		// Concurrent AI triage with worker pool
		aiDecisions := te.aiTriageConcurrent(ctx, aiAlerts, labels, txnsByMerchant)

		// Count metrics (single-threaded, after all workers finish)
		for _, d := range aiDecisions {
			decisions = append(decisions, d)
			countDecisionMetrics(&metrics, d, labels[d.TxnID])
		}

		fmt.Printf("    AI succeeded: %d | Fell back (API error/timeout): %d\n", metrics.AITriaged, metrics.FallbackTriaged)

		// Fallback for overflow alerts (demo mode) or if AI was unavailable for some
		for _, alert := range fallbackOverflow {
			label := labels[alert.TxnID]
			d := te.fallbackTriage(alert)
			d.IsFraud = label.IsFraud
			decisions = append(decisions, d)
			countDecisionMetrics(&metrics, d, label)
		}
	} else {
		// No AI available — use rule-based fallback for all
		for _, alert := range needsAI {
			label := labels[alert.TxnID]
			d := te.fallbackTriage(alert)
			d.IsFraud = label.IsFraud
			decisions = append(decisions, d)
			countDecisionMetrics(&metrics, d, label)
		}
	}

	// Compute workload reduction
	if metrics.TotalFlagged > 0 {
		resolved := metrics.AutoBlocked + metrics.AIBlocked + metrics.AIApproved + metrics.FallbackBlocked + metrics.FallbackApproved
		metrics.WorkloadReduction = float64(resolved) / float64(metrics.TotalFlagged)
	}

	return decisions, metrics
}

// countDecisionMetrics updates the metrics struct based on a single decision.
// Must be called single-threaded (after concurrent workers finish).
func countDecisionMetrics(metrics *TriageMetrics, d TriageDecision, label data.FraudLabel) {
	if d.Source == "ai" {
		metrics.AITriaged++
		switch d.Decision {
		case "BLOCK":
			metrics.AIBlocked++
			if label.IsFraud {
				metrics.AIBlockCorrect++
			} else {
				metrics.AIBlockWrong++
			}
		case "APPROVE":
			metrics.AIApproved++
			if !label.IsFraud {
				metrics.AIApproveCorrect++
			} else {
				metrics.AIApproveWrong++
			}
		case "ESCALATE":
			metrics.AIEscalated++
			metrics.HumanQueueSize++
		}
	} else {
		metrics.FallbackTriaged++
		switch d.Decision {
		case "BLOCK":
			metrics.FallbackBlocked++
			if label.IsFraud {
				metrics.FallbackBlockCorrect++
			} else {
				metrics.FallbackBlockWrong++
			}
		case "APPROVE":
			metrics.FallbackApproved++
			if !label.IsFraud {
				metrics.FallbackApproveCorrect++
			} else {
				metrics.FallbackApproveWrong++
			}
		case "ESCALATE":
			metrics.FallbackEscalated++
			metrics.HumanQueueSize++
		}
	}
}

// aiTriageConcurrent processes alerts through AI triage using a concurrent worker pool.
// Workers write to non-overlapping slices — no races. Rate limiting via per-worker sleep.
func (te *TriageEngine) aiTriageConcurrent(ctx context.Context, alerts []Alert, labels map[int64]data.FraudLabel, txnsByMerchant map[string][]data.Transaction) []TriageDecision {
	decisions := make([]TriageDecision, len(alerts))
	batchSize := 20 // 20 txns × ~80 tokens/obj ≈ 1600 tokens, fits in MaxTokens=4096
	workers := 3    // 3 workers × ~1 req/5s = ~36 RPM peak, backoff handles 429s

	type batchJob struct {
		startIdx int
		batch    []Alert
	}

	jobs := make(chan batchJob, workers*2)
	var wg sync.WaitGroup
	var processed int64

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				result := te.aiTriageBatch(ctx, job.batch, labels, txnsByMerchant)
				// Write to non-overlapping slice indices — no race
				copy(decisions[job.startIdx:job.startIdx+len(result)], result)
				count := atomic.AddInt64(&processed, int64(len(result)))
				fmt.Printf("\r    Sent %d/%d flagged transactions to AI triage...", count, len(alerts))
				// Rate limit: stagger requests to stay under 20 RPM free-tier ceiling
				time.Sleep(3 * time.Second)
			}
		}()
	}

	// Feed all batches into the job channel
	for i := 0; i < len(alerts); i += batchSize {
		end := i + batchSize
		if end > len(alerts) {
			end = len(alerts)
		}
		jobs <- batchJob{startIdx: i, batch: alerts[i:end]}
	}
	close(jobs)
	wg.Wait()
	fmt.Println()

	return decisions
}


// fallbackTriage uses simple rules when AI is unavailable.
// It uses the engine's calibrated thresholds to make proportional decisions
// within the middle band (between auto-approve and auto-block).
func (te *TriageEngine) fallbackTriage(alert Alert) TriageDecision {
	d := TriageDecision{
		TxnID:         alert.TxnID,
		MerchantID:    alert.MerchantID,
		StatScore:     alert.StatScore,
		ForestScore:   alert.ForestScore,
		CombinedScore: alert.CombinedScore,
		RuleFired:     alert.StatAlert.RuleFired,
		Source:        "fallback",
	}

	// Use the midpoint of the calibrated band to split decisions.
	// Middle band is [AutoApproveThreshold, AutoBlockThreshold).
	// Upper half + strong rule → BLOCK; upper half → ESCALATE; lower half → APPROVE.
	midpoint := (te.AutoApproveThreshold + te.AutoBlockThreshold) / 2.0

	if alert.CombinedScore >= midpoint && alert.StatAlert.RuleFired != "" {
		d.Decision = "BLOCK"
		d.Confidence = alert.CombinedScore
		d.Reasoning = fmt.Sprintf("Rule %s fired with combined score %.2f (above midpoint %.2f)",
			alert.StatAlert.RuleFired, alert.CombinedScore, midpoint)
		d.RiskFactors = []string{alert.StatAlert.RuleFired}
	} else if alert.CombinedScore >= midpoint {
		d.Decision = "ESCALATE"
		d.Confidence = alert.CombinedScore
		d.Reasoning = fmt.Sprintf("Combined score %.2f in upper band but no dominant rule — needs human review",
			alert.CombinedScore)
	} else {
		d.Decision = "ESCALATE"
		d.Confidence = alert.CombinedScore
		d.Reasoning = fmt.Sprintf("Combined score %.2f in lower band, but AI is unavailable. Escalating to prevent false negatives.",
			alert.CombinedScore)
	}

	return d
}

func (te *TriageEngine) aiTriageBatch(ctx context.Context, alerts []Alert, labels map[int64]data.FraudLabel, txnsByMerchant map[string][]data.Transaction) []TriageDecision {
	if len(alerts) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString("You are an automated fraud triage system at an Indian payment gateway.\n")
	sb.WriteString("You must make ONE decision for each flagged transaction: BLOCK, APPROVE, or ESCALATE.\n")
	sb.WriteString("BLOCK = definitely fraud, block immediately, no human needed\n")
	sb.WriteString("APPROVE = false alarm, let it through, no human needed\n")
	sb.WriteString("ESCALATE = uncertain, send to human reviewer\n\n")
	sb.WriteString("Here is a list of transactions to evaluate:\n\n")

	for _, alert := range alerts {
		profile := te.profiles[alert.MerchantID]
		recentTxns := txnsByMerchant[alert.MerchantID]

		sb.WriteString(fmt.Sprintf("--- Transaction ID: %d ---\n", alert.TxnID))
		sb.WriteString(fmt.Sprintf("Merchant: %s\n", alert.MerchantID))
		sb.WriteString(fmt.Sprintf("Detection scores: stat=%.2f, ml=%.2f, combined=%.2f\n", alert.StatScore, alert.ForestScore, alert.CombinedScore))
		if alert.StatAlert.RuleFired != "" {
			sb.WriteString(fmt.Sprintf("Primary rule fired: %s\n", alert.StatAlert.RuleFired))
			sb.WriteString(fmt.Sprintf("  Amount Z-score: %.2f\n", alert.StatAlert.AmountZScore))
			sb.WriteString(fmt.Sprintf("  Velocity ratio: %.2fx normal\n", alert.StatAlert.VelocityRatio))
		}
		if profile != nil {
			sb.WriteString(fmt.Sprintf("Merchant profile: Avg=₹%.0f, Stddev=₹%.0f, Count=%d\n", profile.Mean/100.0, profile.Stddev()/100.0, profile.Count))
		}
		if len(recentTxns) > 0 {
			sb.WriteString("Last 3 transactions:\n")
			start := len(recentTxns) - 3
			if start < 0 {
				start = 0
			}
			for _, t := range recentTxns[start:] {
				sb.WriteString(fmt.Sprintf("  ₹%.0f via %s at %s\n", float64(t.AmountPaise)/100.0, t.Method, time.Unix(t.Timestamp, 0).Format("15:04:05")))
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Respond in this exact JSON array format:\n")
	sb.WriteString(`[{"txn_id": 123, "decision": "BLOCK|APPROVE|ESCALATE", "confidence": 0.9, "reasoning": "one line", "risk_factors": ["factor1"]}, ...]`)

	prompt := sb.String()

	respBody, err := te.callOpenRouterTriageBatch(ctx, prompt)

	// Create default fallback decisions
	decisions := make([]TriageDecision, len(alerts))
	decisionMap := make(map[int64]TriageDecision)

	for i, alert := range alerts {
		fb := te.fallbackTriage(alert)
		if err != nil {
			fb.Source = "fallback"
			fb.Reasoning = fmt.Sprintf("AI unavailable (%v), used rule-based fallback: %s", err, fb.Reasoning)
		}
		decisions[i] = fb
		decisionMap[alert.TxnID] = fb
	}

	if err != nil {
		return decisions
	}

	var aiResults []struct {
		TxnID       int64    `json:"txn_id"`
		Decision    string   `json:"decision"`
		Confidence  float64  `json:"confidence"`
		Reasoning   string   `json:"reasoning"`
		RiskFactors []string `json:"risk_factors"`
	}

	if err := json.Unmarshal([]byte(respBody), &aiResults); err != nil {
		fmt.Printf("    [WARN] AI JSON parse failed (falling back): %v | raw response (first 200 chars): %.200s\n", err, respBody)
		return decisions
	}

	// Build alert lookup for safety gate
	alertByTxnID := make(map[int64]Alert)
	for _, a := range alerts {
		alertByTxnID[a.TxnID] = a
	}
	midpoint := (te.AutoApproveThreshold + te.AutoBlockThreshold) / 2.0

	for _, res := range aiResults {
		if d, ok := decisionMap[res.TxnID]; ok {
			d.Decision = strings.ToUpper(strings.TrimSpace(res.Decision))
			d.Confidence = res.Confidence
			d.Reasoning = res.Reasoning
			d.RiskFactors = res.RiskFactors
			d.Source = "ai"

			switch d.Decision {
			case "BLOCK", "APPROVE", "ESCALATE":
			default:
				d.Decision = "ESCALATE"
				d.Reasoning = "Invalid AI decision: " + res.Decision
			}

			// Safety gate: AI cannot approve transactions with high combined scores.
			// This prevents dangerous false negatives where the LLM naively approves
			// a genuinely suspicious transaction.
			if d.Decision == "APPROVE" {
				if alert, found := alertByTxnID[res.TxnID]; found && alert.CombinedScore >= midpoint {
					d.Decision = "ESCALATE"
					d.Reasoning = fmt.Sprintf("AI wanted to approve but combined score %.2f >= midpoint %.2f — escalating for safety", alert.CombinedScore, midpoint)
				}
			}

			decisionMap[res.TxnID] = d
		}
	}

	for i, alert := range alerts {
		decisions[i] = decisionMap[alert.TxnID]
	}

	return decisions
}

func (te *TriageEngine) callOpenRouterTriageBatch(ctx context.Context, prompt string) (string, error) {
	url := "https://openrouter.ai/api/v1/chat/completions"

	type openRouterMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}

	type openRouterRequest struct {
		Model    string              `json:"model"`
		Messages []openRouterMessage `json:"messages"`
		MaxTokens int                `json:"max_tokens,omitempty"`
	}

	reqBody := openRouterRequest{
		Model: te.explainer.Model,
		Messages: []openRouterMessage{{
			Role:    "user",
			Content: prompt,
		}},
		MaxTokens: 4096, // batch of 10 txns needs ~2000-3000 tokens of JSON output
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+te.explainer.APIKey)

	resp, err := te.explainer.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read: %v", err)
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("API %d: %s", resp.StatusCode, string(respBody))
	}

	type openRouterResponse struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	var orResp openRouterResponse
	if err := json.Unmarshal(respBody, &orResp); err != nil {
		return "", fmt.Errorf("parse JSON: %v", err)
	}

	if len(orResp.Choices) == 0 || orResp.Choices[0].Message.Content == "" {
		return "", fmt.Errorf("empty response")
	}

	text := orResp.Choices[0].Message.Content
	text = strings.TrimSpace(text)
	if idx := strings.Index(text, "["); idx >= 0 {
		if end := strings.LastIndex(text, "]"); end > idx {
			text = text[idx : end+1]
		}
	}

	return text, nil
}
