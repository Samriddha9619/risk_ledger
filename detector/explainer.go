package detector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Samriddha9619/risk_ledger/data"
)

// Explanation holds the LLM-generated explanation for a fraud alert.
type Explanation struct {
	MerchantID  string
	Summary     string
	RiskFactors []string
	Action      string
	Error       string // non-empty if LLM call failed
}

// Explainer calls Gemini API to generate human-readable fraud explanations.
type Explainer struct {
	APIKey  string
	Model   string
	Enabled bool
	client  *http.Client // properly configured HTTP client
}

// NewExplainer creates an explainer. If apiKey is empty, it tries GEMINI_API_KEY env var.
// If neither is available, the explainer is disabled (returns fallback explanations).
func NewExplainer(apiKey string) *Explainer {
	if apiKey == "" {
		apiKey = os.Getenv("OPENROUTER_API_KEY")
	}
	return &Explainer{
		APIKey:  apiKey,
		Model:   "openrouter/free",
		Enabled: apiKey != "",
		client: &http.Client{
			Timeout: 15 * time.Second, // reduced to fail fast on slow free models
		},
	}
}

// mockGeminiTransport intercepts API calls and returns simulated successful AI responses.
type mockGeminiTransport struct{}

func (m *mockGeminiTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Simulate AI deciding to BLOCK for high amounts, ESCALATE for others
	decision := "ESCALATE"
	if req.ContentLength > 800 { // just a heuristic for the mock
		decision = "BLOCK"
	}
	
	mockJSON := fmt.Sprintf(`{
		"candidates": [{
			"content": {
				"parts": [{
					"text": "{\"decision\": \"%s\", \"confidence\": 0.95, \"reasoning\": \"AI confirmed anomalous patterns in the transaction history.\", \"risk_factors\": [\"Unusual velocity\", \"Amount spike\"], \"summary\": \"High risk of automated attack.\", \"action\": \"Block and request KYC.\"}"
				}]
			}
		}]
	}`, decision)

	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(mockJSON)),
		Header:     make(http.Header),
	}, nil
}

// NewExplainerDisabled creates an explainer that is guaranteed disabled.
// No API calls will be made regardless of environment variables.
// Use this when --no-llm is set.
func NewExplainerDisabled() *Explainer {
	return &Explainer{
		Enabled: false,
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

// ExplainAlerts generates explanations for the top-N highest-scored alerts.
// It groups alerts by merchant to reduce API calls.
func (e *Explainer) ExplainAlerts(alerts []Alert, profiles map[string]*MerchantProfile, allTxns []data.Transaction, topN int) []Explanation {
	if !e.Enabled {
		return e.fallbackExplanations(alerts, profiles, topN)
	}

	// Sort alerts by combined score descending
	sorted := make([]Alert, len(alerts))
	copy(sorted, alerts)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].CombinedScore > sorted[j].CombinedScore
	})

	// Group by merchant, take top N unique merchants
	seen := make(map[string]bool)
	var topAlerts []Alert
	for _, a := range sorted {
		if seen[a.MerchantID] {
			continue
		}
		seen[a.MerchantID] = true
		topAlerts = append(topAlerts, a)
		if len(topAlerts) >= topN {
			break
		}
	}

	// Build txn lookup by merchant
	txnsByMerchant := make(map[string][]data.Transaction)
	for _, t := range allTxns {
		txnsByMerchant[t.MerchantID] = append(txnsByMerchant[t.MerchantID], t)
	}

	ctx := context.Background()
	var explanations []Explanation
	for _, alert := range topAlerts {
		profile := profiles[alert.MerchantID]
		merchantTxns := txnsByMerchant[alert.MerchantID]

		prompt := buildPrompt(alert, profile, merchantTxns)
		explanation := e.callGeminiWithRetry(ctx, prompt, alert.MerchantID)
		explanations = append(explanations, explanation)

		// Rate limiting: Gemini free tier is 15 RPM
		time.Sleep(4 * time.Second)
	}
	return explanations
}

func buildPrompt(alert Alert, profile *MerchantProfile, recentTxns []data.Transaction) string {
	var sb strings.Builder
	sb.WriteString("You are a fraud analyst at an Indian payment gateway. Analyze this suspicious activity and provide:\n")
	sb.WriteString("1. A brief explanation of why it's suspicious (2-3 sentences)\n")
	sb.WriteString("2. The specific risk factors (bullet points)\n")
	sb.WriteString("3. A recommended defensive action for the merchant\n\n")

	sb.WriteString(fmt.Sprintf("Merchant: %s\n", alert.MerchantID))
	sb.WriteString(fmt.Sprintf("Anomaly Score: %.2f (threshold: 0.5)\n", alert.CombinedScore))
	sb.WriteString(fmt.Sprintf("Statistical Score: %.2f\n", alert.StatScore))
	sb.WriteString(fmt.Sprintf("ML (Isolation Forest) Score: %.2f\n", alert.ForestScore))

	if profile != nil {
		sb.WriteString(fmt.Sprintf("\nMerchant Normal Profile:\n"))
		sb.WriteString(fmt.Sprintf("  Average transaction: ₹%.0f\n", profile.Mean/100.0))
		sb.WriteString(fmt.Sprintf("  Std deviation: ₹%.0f\n", profile.Stddev()/100.0))
		sb.WriteString(fmt.Sprintf("  Total transactions seen: %d\n", profile.Count))
	}

	if alert.StatAlert.RuleFired != "" {
		sb.WriteString(fmt.Sprintf("\nPrimary detection rule: %s\n", alert.StatAlert.RuleFired))
		sb.WriteString(fmt.Sprintf("  Amount Z-score: %.2f\n", alert.StatAlert.AmountZScore))
		sb.WriteString(fmt.Sprintf("  Velocity ratio: %.2f\n", alert.StatAlert.VelocityRatio))
		sb.WriteString(fmt.Sprintf("  Small burst count: %d\n", alert.StatAlert.SmallBurstCount))
	}

	// Show last few transactions for context
	if len(recentTxns) > 0 {
		sb.WriteString("\nRecent transactions for this merchant (last 10):\n")
		start := len(recentTxns) - 10
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
	sb.WriteString(`{"summary": "...", "risk_factors": ["...", "..."], "action": "..."}`)
	sb.WriteString("\n\nDo not suggest any offensive or fraud-enabling actions. Focus on defensive measures only.")
	return sb.String()
}

// openRouterRequest/openRouterResponse model the OpenRouter Chat Completions API.
type openRouterMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openRouterRequest struct {
	Model    string              `json:"model"`
	Messages []openRouterMessage `json:"messages"`
	MaxTokens int                `json:"max_tokens,omitempty"`
}

type openRouterResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type llmOutput struct {
	Summary     string   `json:"summary"`
	RiskFactors []string `json:"risk_factors"`
	Action      string   `json:"action"`
}

// callGeminiWithRetry calls Gemini with exponential backoff on 429/5xx errors.
func (e *Explainer) callGeminiWithRetry(ctx context.Context, prompt string, merchantID string) Explanation {
	maxRetries := 3
	backoff := 2 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		exp := e.callGemini(ctx, prompt, merchantID)
		if exp.Error == "" {
			return exp
		}

		// Only retry on server errors or rate limits
		if !strings.Contains(exp.Error, "API 429") &&
			!strings.Contains(exp.Error, "API 5") {
			return exp // client error, don't retry
		}

		if attempt < maxRetries-1 {
			select {
			case <-ctx.Done():
				return Explanation{MerchantID: merchantID, Error: "context cancelled"}
			case <-time.After(backoff):
				backoff *= 2
			}
		}
	}

	return Explanation{MerchantID: merchantID, Error: "max retries exceeded"}
}

func (e *Explainer) callGemini(ctx context.Context, prompt string, merchantID string) Explanation {
	url := "https://openrouter.ai/api/v1/chat/completions"

	reqBody := openRouterRequest{
		Model: e.Model,
		Messages: []openRouterMessage{{
			Role:    "user",
			Content: prompt,
		}},
		MaxTokens: 400,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return Explanation{MerchantID: merchantID, Error: fmt.Sprintf("marshal error: %v", err)}
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return Explanation{MerchantID: merchantID, Error: fmt.Sprintf("create request: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.APIKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return Explanation{MerchantID: merchantID, Error: fmt.Sprintf("API call failed: %v", err)}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Explanation{MerchantID: merchantID, Error: fmt.Sprintf("read response: %v", err)}
	}

	if resp.StatusCode != 200 {
		return Explanation{MerchantID: merchantID, Error: fmt.Sprintf("API %d: %s", resp.StatusCode, string(respBody))}
	}

	var orResp openRouterResponse
	if err := json.Unmarshal(respBody, &orResp); err != nil {
		return Explanation{MerchantID: merchantID, Error: fmt.Sprintf("parse response: %v", err)}
	}

	if len(orResp.Choices) == 0 || orResp.Choices[0].Message.Content == "" {
		return Explanation{MerchantID: merchantID, Error: "empty response from OpenRouter"}
	}

	text := orResp.Choices[0].Message.Content
	// Try to extract JSON from the response (Gemini may wrap it in markdown)
	text = strings.TrimSpace(text)
	if idx := strings.Index(text, "{"); idx >= 0 {
		if end := strings.LastIndex(text, "}"); end > idx {
			text = text[idx : end+1]
		}
	}

	var output llmOutput
	if err := json.Unmarshal([]byte(text), &output); err != nil {
		// If JSON parsing fails, use the raw text as summary
		return Explanation{
			MerchantID: merchantID,
			Summary:    orResp.Choices[0].Message.Content,
			Action:     "Review flagged transactions manually",
		}
	}

	return Explanation{
		MerchantID:  merchantID,
		Summary:     output.Summary,
		RiskFactors: output.RiskFactors,
		Action:      output.Action,
	}
}

// fallbackExplanations generates rule-based explanations when LLM is unavailable.
func (e *Explainer) fallbackExplanations(alerts []Alert, profiles map[string]*MerchantProfile, topN int) []Explanation {
	sorted := make([]Alert, len(alerts))
	copy(sorted, alerts)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].CombinedScore > sorted[j].CombinedScore
	})

	seen := make(map[string]bool)
	var explanations []Explanation
	for _, a := range sorted {
		if seen[a.MerchantID] {
			continue
		}
		seen[a.MerchantID] = true

		exp := Explanation{MerchantID: a.MerchantID}
		profile := profiles[a.MerchantID]

		switch a.StatAlert.RuleFired {
		case "amount_zscore":
			exp.Summary = fmt.Sprintf("Transactions with amounts significantly deviating from merchant's average (Z-score: %.1f). Average is ₹%.0f.",
				a.StatAlert.AmountZScore, profile.Mean/100.0)
			exp.RiskFactors = []string{"Unusual transaction amounts", "Possible card-not-present fraud"}
			exp.Action = "Enable amount-based velocity checks and 3DS for high-value transactions"
		case "velocity_spike":
			exp.Summary = fmt.Sprintf("Transaction velocity is %.1fx the normal rate for this merchant.",
				a.StatAlert.VelocityRatio)
			exp.RiskFactors = []string{"Abnormal transaction frequency", "Possible automated attack"}
			exp.Action = "Implement rate limiting and CAPTCHA for high-velocity periods"
		case "small_burst":
			exp.Summary = fmt.Sprintf("Burst of %d small transactions detected, matching card-testing pattern.",
				a.StatAlert.SmallBurstCount)
			exp.RiskFactors = []string{"Multiple small-value transactions", "Card testing signature"}
			exp.Action = "Enable 3DS verification for transactions under ₹100 and implement cooldown periods"
		default:
			exp.Summary = fmt.Sprintf("Anomalous behavior detected by ML model (Isolation Forest score: %.2f).",
				a.ForestScore)
			exp.RiskFactors = []string{"Statistical anomaly across multiple features"}
			exp.Action = "Review flagged transactions manually and monitor merchant activity"
		}

		explanations = append(explanations, exp)
		if len(explanations) >= topN {
			break
		}
	}
	return explanations
}
