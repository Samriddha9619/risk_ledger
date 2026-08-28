package detector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
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
	HumanQueueSize    int     `json:"human_queue_size"` // = AIEscalated only
	WorkloadReduction float64 `json:"workload_reduction"`
	AutoBlockCorrect  int     `json:"auto_block_correct"`
	AutoBlockWrong    int     `json:"auto_block_wrong"`
	AIBlockCorrect    int     `json:"ai_block_correct"`
	AIBlockWrong      int     `json:"ai_block_wrong"`
	AIApproveCorrect  int     `json:"ai_approve_correct"`
	AIApproveWrong    int     `json:"ai_approve_wrong"` // dangerous: AI approved actual fraud
}

// TriageEngine performs AI-powered triage on flagged transactions.
type TriageEngine struct {
	explainer *Explainer
	profiles  map[string]*MerchantProfile

	// Thresholds for auto-resolution
	AutoBlockThreshold   float64 // above this → auto-block (no AI needed)
	AutoApproveThreshold float64 // below this → auto-approve (no AI needed)
}

// NewTriageEngine creates a triage engine.
func NewTriageEngine(explainer *Explainer, profiles map[string]*MerchantProfile) *TriageEngine {
	return &TriageEngine{
		explainer:            explainer,
		profiles:             profiles,
		AutoBlockThreshold:   0.78, // above this → auto-block (high confidence fraud)
		AutoApproveThreshold: 0.50, // below this → auto-approve (detection engine says normal)
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
		// Limit AI calls to top 50 by score (rate limit protection)
		limit := 50
		if len(needsAI) < limit {
			limit = len(needsAI)
		}

		// Process in batches to respect rate limits
		batchSize := 5
		for i := 0; i < limit; i++ {
			alert := needsAI[i]
			label := labels[alert.TxnID]
			profile := te.profiles[alert.MerchantID]
			merchantTxns := txnsByMerchant[alert.MerchantID]

			d := te.aiTriage(ctx, alert, profile, merchantTxns)
			d.IsFraud = label.IsFraud

			decisions = append(decisions, d)
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
					metrics.AIApproveWrong++ // dangerous
				}
			case "ESCALATE":
				metrics.AIEscalated++
				metrics.HumanQueueSize++
			}

			// Rate limiting: pause every batchSize calls
			if (i+1)%batchSize == 0 && i+1 < limit {
				time.Sleep(12 * time.Second)
			}
		}

		// Remaining alerts beyond the AI limit get fallback triage
		for i := limit; i < len(needsAI); i++ {
			alert := needsAI[i]
			label := labels[alert.TxnID]
			d := te.fallbackTriage(alert)
			d.IsFraud = label.IsFraud
			decisions = append(decisions, d)
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
		}
	} else {
		// No AI available — use rule-based fallback for all
		for _, alert := range needsAI {
			label := labels[alert.TxnID]
			d := te.fallbackTriage(alert)
			d.IsFraud = label.IsFraud
			decisions = append(decisions, d)
			metrics.AITriaged++
			switch d.Decision {
			case "BLOCK":
				metrics.AIBlocked++
				if label.IsFraud {
					metrics.AIBlockCorrect++
				} else {
					metrics.AIBlockWrong++
				}
			case "ESCALATE":
				metrics.AIEscalated++
				metrics.HumanQueueSize++
			}
		}
	}

	// Compute workload reduction
	if metrics.TotalFlagged > 0 {
		resolved := metrics.AutoBlocked + metrics.AIBlocked + metrics.AIApproved
		metrics.WorkloadReduction = float64(resolved) / float64(metrics.TotalFlagged)
	}

	return decisions, metrics
}

// aiTriage sends a triage request to Gemini.
func (te *TriageEngine) aiTriage(ctx context.Context, alert Alert, profile *MerchantProfile, recentTxns []data.Transaction) TriageDecision {
	prompt := te.buildTriagePrompt(alert, profile, recentTxns)

	d := TriageDecision{
		TxnID:         alert.TxnID,
		MerchantID:    alert.MerchantID,
		StatScore:     alert.StatScore,
		ForestScore:   alert.ForestScore,
		CombinedScore: alert.CombinedScore,
		RuleFired:     alert.StatAlert.RuleFired,
		Source:        "ai",
	}

	resp := te.callGeminiTriage(ctx, prompt)
	if resp.Error != "" {
		// AI failed — fall back to rule-based
		fb := te.fallbackTriage(alert)
		fb.Source = "fallback"
		fb.Reasoning = fmt.Sprintf("AI unavailable (%s), used rule-based fallback: %s", resp.Error, fb.Reasoning)
		return fb
	}

	d.Decision = resp.Decision
	d.Confidence = resp.Confidence
	d.Reasoning = resp.Reasoning
	d.RiskFactors = resp.RiskFactors

	// Validate decision
	switch d.Decision {
	case "BLOCK", "APPROVE", "ESCALATE":
		// valid
	default:
		d.Decision = "ESCALATE"
		d.Reasoning = fmt.Sprintf("AI returned invalid decision '%s', escalating. Original: %s", resp.Decision, d.Reasoning)
	}

	return d
}

// fallbackTriage uses simple rules when AI is unavailable.
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

	// Rule-based triage: if stat score is high and rule fired, block
	if alert.StatScore > 0.6 && alert.StatAlert.RuleFired != "" {
		d.Decision = "BLOCK"
		d.Confidence = alert.StatScore
		d.Reasoning = fmt.Sprintf("Rule %s fired with score %.2f", alert.StatAlert.RuleFired, alert.StatScore)
		d.RiskFactors = []string{alert.StatAlert.RuleFired}
	} else if alert.CombinedScore > 0.6 {
		d.Decision = "ESCALATE"
		d.Confidence = alert.CombinedScore
		d.Reasoning = "Combined score elevated but no dominant rule — needs human review"
	} else {
		d.Decision = "APPROVE"
		d.Confidence = 1.0 - alert.CombinedScore
		d.Reasoning = "Low combined score with no strong rule signal"
	}

	return d
}

func (te *TriageEngine) buildTriagePrompt(alert Alert, profile *MerchantProfile, recentTxns []data.Transaction) string {
	var sb strings.Builder
	sb.WriteString("You are an automated fraud triage system at an Indian payment gateway.\n")
	sb.WriteString("You must make ONE decision for this flagged transaction: BLOCK, APPROVE, or ESCALATE.\n\n")
	sb.WriteString("BLOCK = definitely fraud, block immediately, no human needed\n")
	sb.WriteString("APPROVE = false alarm, let it through, no human needed\n")
	sb.WriteString("ESCALATE = uncertain, send to human reviewer\n\n")

	sb.WriteString(fmt.Sprintf("Transaction ID: %d\n", alert.TxnID))
	sb.WriteString(fmt.Sprintf("Merchant: %s\n", alert.MerchantID))
	sb.WriteString(fmt.Sprintf("Detection scores: stat=%.2f, ml=%.2f, combined=%.2f\n", alert.StatScore, alert.ForestScore, alert.CombinedScore))
	sb.WriteString(fmt.Sprintf("Primary rule fired: %s\n", alert.StatAlert.RuleFired))

	if profile != nil {
		sb.WriteString(fmt.Sprintf("\nMerchant profile:\n"))
		sb.WriteString(fmt.Sprintf("  Average transaction: ₹%.0f\n", profile.Mean/100.0))
		sb.WriteString(fmt.Sprintf("  Std deviation: ₹%.0f\n", profile.Stddev()/100.0))
		sb.WriteString(fmt.Sprintf("  Total transactions: %d\n", profile.Count))
	}

	if alert.StatAlert.RuleFired != "" {
		sb.WriteString(fmt.Sprintf("\nDetection details:\n"))
		sb.WriteString(fmt.Sprintf("  Amount Z-score: %.2f\n", alert.StatAlert.AmountZScore))
		sb.WriteString(fmt.Sprintf("  Velocity ratio: %.2fx normal\n", alert.StatAlert.VelocityRatio))
		sb.WriteString(fmt.Sprintf("  Small burst count: %d\n", alert.StatAlert.SmallBurstCount))
	}

	if len(recentTxns) > 0 {
		sb.WriteString("\nLast 5 transactions:\n")
		start := len(recentTxns) - 5
		if start < 0 {
			start = 0
		}
		for _, t := range recentTxns[start:] {
			sb.WriteString(fmt.Sprintf("  ₹%.0f via %s at %s\n",
				float64(t.AmountPaise)/100.0,
				t.Method,
				time.Unix(t.Timestamp, 0).Format("15:04:05")))
		}
	}

	sb.WriteString("\nRespond in this exact JSON format:\n")
	sb.WriteString(`{"decision": "BLOCK|APPROVE|ESCALATE", "confidence": 0.0-1.0, "reasoning": "one line", "risk_factors": ["factor1", "factor2"]}`)
	sb.WriteString("\n\nBe decisive. Only ESCALATE if genuinely uncertain.")
	return sb.String()
}

type triageResponse struct {
	Decision    string   `json:"decision"`
	Confidence  float64  `json:"confidence"`
	Reasoning   string   `json:"reasoning"`
	RiskFactors []string `json:"risk_factors"`
	Error       string   `json:"-"`
}

func (te *TriageEngine) callGeminiTriage(ctx context.Context, prompt string) triageResponse {
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		te.explainer.Model, te.explainer.APIKey)

	reqBody := geminiRequest{
		Contents: []geminiContent{{
			Parts: []geminiPart{{Text: prompt}},
		}},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return triageResponse{Error: fmt.Sprintf("marshal: %v", err)}
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return triageResponse{Error: fmt.Sprintf("create request: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := te.explainer.client.Do(req)
	if err != nil {
		return triageResponse{Error: fmt.Sprintf("request: %v", err)}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return triageResponse{Error: fmt.Sprintf("read: %v", err)}
	}

	if resp.StatusCode != 200 {
		return triageResponse{Error: fmt.Sprintf("API %d", resp.StatusCode)}
	}

	var gemResp geminiResponse
	if err := json.Unmarshal(respBody, &gemResp); err != nil {
		return triageResponse{Error: fmt.Sprintf("parse: %v", err)}
	}

	if len(gemResp.Candidates) == 0 || len(gemResp.Candidates[0].Content.Parts) == 0 {
		return triageResponse{Error: "empty response"}
	}

	text := gemResp.Candidates[0].Content.Parts[0].Text
	text = strings.TrimSpace(text)
	if idx := strings.Index(text, "{"); idx >= 0 {
		if end := strings.LastIndex(text, "}"); end > idx {
			text = text[idx : end+1]
		}
	}

	var result triageResponse
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return triageResponse{Error: fmt.Sprintf("parse JSON: %v", err)}
	}
	return result
}
