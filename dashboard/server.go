package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"time"

	"github.com/Samriddha9619/risk_ledger/detector"
	"github.com/Samriddha9619/risk_ledger/eval"
	"github.com/Samriddha9619/risk_ledger/ledger"
)

//go:embed static
var staticFiles embed.FS

// Config holds dashboard server configuration.
type Config struct {
	Port         int
	DataDir      string
	DBDir        string
	SpeedMs      int // milliseconds between streamed transactions
	NoLLM        bool
}

// Serve starts the dashboard HTTP server.
func Serve(cfg Config) error {
	// Load data
	fmt.Println("[1/3] Loading data from GoDB...")
	store, err := ledger.NewTransactionStore(cfg.DBDir, false)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}

	txns, err := store.ScanAll()
	if err != nil {
		return fmt.Errorf("scanning: %w", err)
	}
	fmt.Printf("  Loaded %d transactions\n", len(txns))

	labels, err := ledger.LoadLabelsFromCSV(cfg.DataDir + "/labels.csv")
	if err != nil {
		return fmt.Errorf("loading labels: %w", err)
	}

	// Sort by timestamp
	sort.Slice(txns, func(i, j int) bool { return txns[i].Timestamp < txns[j].Timestamp })

	// Split and train
	splitIdx := len(txns) * 70 / 100
	trainSet := txns[:splitIdx]
	testSet := txns[splitIdx:]

	fmt.Println("[2/3] Training detection engine...")
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
	engine.TrainForest(trainingFeatures)

	// Pre-compute alerts for the report endpoint
	var alerts []detector.Alert
	amounts := make(map[int64]int64)
	for _, txn := range testSet {
		alert := engine.Score(txn)
		alerts = append(alerts, alert)
		amounts[txn.TxnID] = txn.AmountPaise
	}

	// Load JSON report if it exists
	var reportJSON []byte
	if raw, err := os.ReadFile("results/evaluation.json"); err == nil {
		reportJSON = raw
	}

	// Set up routes
	mux := http.NewServeMux()

	// Serve static files
	mux.Handle("GET /static/", http.FileServerFS(staticFiles))

	// Serve index.html at root
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		content, err := staticFiles.ReadFile("static/index.html")
		if err != nil {
			http.Error(w, "index.html not found", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(content)
	})

	// API: return evaluation report JSON
	mux.HandleFunc("GET /api/report", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if reportJSON != nil {
			w.Write(reportJSON)
		} else {
			w.Write([]byte(`{"error": "no report available, run 'risk_ledger run' first"}`))
		}
	})

	// API: return top alerts
	mux.HandleFunc("GET /api/alerts", func(w http.ResponseWriter, r *http.Request) {
		sorted := make([]detector.Alert, len(alerts))
		copy(sorted, alerts)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].CombinedScore > sorted[j].CombinedScore
		})
		top := sorted
		if len(top) > 20 {
			top = top[:20]
		}

		type alertJSON struct {
			TxnID         int64   `json:"txn_id"`
			MerchantID    string  `json:"merchant_id"`
			Amount        float64 `json:"amount_rupees"`
			StatScore     float64 `json:"stat_score"`
			ForestScore   float64 `json:"forest_score"`
			CombinedScore float64 `json:"combined_score"`
			RuleFired     string  `json:"rule_fired"`
			IsFraud       bool    `json:"is_fraud"`
		}

		var out []alertJSON
		for _, a := range top {
			label := labels[a.TxnID]
			out = append(out, alertJSON{
				TxnID:         a.TxnID,
				MerchantID:    a.MerchantID,
				Amount:        float64(amounts[a.TxnID]) / 100.0,
				StatScore:     a.StatScore,
				ForestScore:   a.ForestScore,
				CombinedScore: a.CombinedScore,
				RuleFired:     a.StatAlert.RuleFired,
				IsFraud:       label.IsFraud,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	// SSE: stream test transactions in real-time
	mux.HandleFunc("GET /api/stream", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", 500)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		// Create a fresh engine for streaming (so profiles build up from scratch)
		streamEngine := detector.NewDetectionEngine()
		var streamFeatures [][]float64
		for _, txn := range trainSet {
			label := labels[txn.TxnID]
			features := streamEngine.ProcessTraining(txn)
			if !label.IsFraud {
				fs := make([]float64, detector.NumFeatures)
				copy(fs, features[:])
				streamFeatures = append(streamFeatures, fs)
			}
		}
		streamEngine.TrainForest(streamFeatures)

		// Running metrics
		var tp, fp, tn, fn int
		threshold := 0.69

		type streamEvent struct {
			TxnID         int64   `json:"txn_id"`
			MerchantID    string  `json:"merchant_id"`
			Amount        float64 `json:"amount_rupees"`
			Method        string  `json:"method"`
			Category      string  `json:"category"`
			StatScore     float64 `json:"stat_score"`
			ForestScore   float64 `json:"forest_score"`
			CombinedScore float64 `json:"combined_score"`
			RuleFired     string  `json:"rule_fired"`
			IsFraud       bool    `json:"is_fraud"`
			Flagged       bool    `json:"flagged"`
			// Running metrics
			Precision float64 `json:"precision"`
			Recall    float64 `json:"recall"`
			F1        float64 `json:"f1"`
			AlertCount int    `json:"alert_count"`
			TxnCount  int     `json:"txn_count"`
		}

		speed := time.Duration(cfg.SpeedMs) * time.Millisecond

		for i, txn := range testSet {
			select {
			case <-r.Context().Done():
				return
			default:
			}

			alert := streamEngine.Score(txn)
			label := labels[txn.TxnID]
			flagged := alert.CombinedScore >= threshold

			// Update running metrics
			switch {
			case flagged && label.IsFraud:
				tp++
			case flagged && !label.IsFraud:
				fp++
			case !flagged && label.IsFraud:
				fn++
			case !flagged && !label.IsFraud:
				tn++
			}

			var precision, recall, f1 float64
			if tp+fp > 0 {
				precision = float64(tp) / float64(tp+fp)
			}
			if tp+fn > 0 {
				recall = float64(tp) / float64(tp+fn)
			}
			if precision+recall > 0 {
				f1 = 2.0 * precision * recall / (precision + recall)
			}

			evt := streamEvent{
				TxnID:         txn.TxnID,
				MerchantID:    txn.MerchantID,
				Amount:        float64(txn.AmountPaise) / 100.0,
				Method:        txn.Method,
				Category:      txn.Category,
				StatScore:     alert.StatScore,
				ForestScore:   alert.ForestScore,
				CombinedScore: alert.CombinedScore,
				RuleFired:     alert.StatAlert.RuleFired,
				IsFraud:       label.IsFraud,
				Flagged:       flagged,
				Precision:     precision,
				Recall:        recall,
				F1:            f1,
				AlertCount:    tp + fp,
				TxnCount:      i + 1,
			}

			evtJSON, _ := json.Marshal(evt)
			fmt.Fprintf(w, "data: %s\n\n", evtJSON)
			flusher.Flush()

			time.Sleep(speed)
		}

		// Send done event
		fmt.Fprintf(w, "event: done\ndata: {}\n\n")
		flusher.Flush()
	})

	// Compute summary for the overview endpoint
	merchantSet := make(map[string]bool)
	for _, txn := range txns {
		merchantSet[txn.MerchantID] = true
	}
	fraudCount := 0
	for _, l := range labels {
		if l.IsFraud {
			fraudCount++
		}
	}

	combinedResults := eval.SweepThresholds(alerts, labels, amounts, 100)
	bestF1 := eval.FindBestF1(combinedResults)

	mux.HandleFunc("GET /api/summary", func(w http.ResponseWriter, r *http.Request) {
		summary := map[string]interface{}{
			"total_txns":    len(txns),
			"num_merchants": len(merchantSet),
			"fraud_count":   fraudCount,
			"train_size":    len(trainSet),
			"test_size":     len(testSet),
			"best_f1":       bestF1.F1,
			"threshold":     bestF1.Threshold,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(summary)
	})

	// Graceful shutdown: listen for SIGINT (Ctrl+C)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		fmt.Println("\n  Shutting down dashboard...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	fmt.Printf("[3/3] Dashboard running at http://localhost:%d\n", cfg.Port)
	fmt.Println("  Press Ctrl+C to stop")
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}
