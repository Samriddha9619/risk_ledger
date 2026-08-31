package data

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
)

// LoadTransactionsCSV reads a transactions CSV into a slice.
// CSV format: txn_id,merchant_id,amount_paise,timestamp,method,category
func LoadTransactionsCSV(path string) ([]Transaction, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading CSV: %w", err)
	}

	txns := make([]Transaction, 0, len(records))
	for _, row := range records {
		if len(row) < NumColumns {
			continue
		}
		txnID, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parsing txn_id %q: %w", row[0], err)
		}
		amountPaise, err := strconv.ParseInt(row[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parsing amount %q: %w", row[2], err)
		}
		timestamp, err := strconv.ParseInt(row[3], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parsing timestamp %q: %w", row[3], err)
		}
		txns = append(txns, Transaction{
			TxnID:       txnID,
			MerchantID:  row[1],
			AmountPaise: amountPaise,
			Timestamp:   timestamp,
			Method:      row[4],
			Category:    row[5],
		})
	}
	return txns, nil
}

// LoadLabelsCSV reads a labels CSV into a map of txn_id → FraudLabel.
// CSV format: txn_id,is_fraud,fraud_type
func LoadLabelsCSV(path string) (map[int64]FraudLabel, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}

	labels := make(map[int64]FraudLabel, len(records))
	for _, row := range records {
		if len(row) < 3 {
			continue
		}
		txnID, _ := strconv.ParseInt(row[0], 10, 64)
		isFraud := row[1] == "1"
		labels[txnID] = FraudLabel{
			TxnID:     txnID,
			IsFraud:   isFraud,
			FraudType: row[2],
		}
	}
	return labels, nil
}
