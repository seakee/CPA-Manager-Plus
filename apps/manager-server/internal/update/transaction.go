package update

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Transaction struct {
	SchemaVersion   int      `json:"schemaVersion"`
	TransactionID   string   `json:"transactionId"`
	InstallManifest string   `json:"installManifest"`
	StatusPath      string   `json:"statusPath"`
	PackageRoot     string   `json:"packageRoot"`
	DataPaths       []string `json:"dataPaths"`
	CurrentVersion  string   `json:"currentVersion"`
	TargetVersion   string   `json:"targetVersion"`
	ParentPID       int      `json:"parentPid"`
	HealthURL       string   `json:"healthUrl"`
	InfoURL         string   `json:"infoUrl"`
}

func WriteTransaction(path string, transaction Transaction) error {
	if transaction.SchemaVersion == 0 {
		transaction.SchemaVersion = 1
	}
	data, err := json.MarshalIndent(transaction, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writePrivateAtomic(path, data)
}

func ReadTransaction(path string) (Transaction, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Transaction{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Transaction{}, errors.New("update transaction must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Transaction{}, err
	}
	var transaction Transaction
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&transaction); err != nil {
		return Transaction{}, fmt.Errorf("parse update transaction: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Transaction{}, fmt.Errorf("parse update transaction: %w", err)
	}
	if transaction.SchemaVersion != 1 || !validTransactionID(transaction.TransactionID) || transaction.ParentPID <= 0 {
		return Transaction{}, fmt.Errorf("update transaction is invalid")
	}
	manifest, err := LoadInstallManifest(transaction.InstallManifest)
	if err != nil {
		return Transaction{}, err
	}
	root, _ := filepath.Abs(manifest.InstallRoot)
	expectedManifestPath := filepath.Join(root, ".update", "install.json")
	expectedStatusPath := filepath.Join(root, ".update", "status.json")
	expectedTransactionRoot := filepath.Join(root, ".update", "transactions", transaction.TransactionID)
	expectedTransactionPath := filepath.Join(expectedTransactionRoot, "transaction.json")
	stagingRoot := filepath.Join(expectedTransactionRoot, "staging")
	if !samePath(transaction.InstallManifest, expectedManifestPath) ||
		!samePath(path, expectedTransactionPath) ||
		!samePath(transaction.StatusPath, expectedStatusPath) {
		return Transaction{}, errors.New("update transaction paths do not match the managed layout")
	}
	packageRoot, packageErr := filepath.Abs(transaction.PackageRoot)
	if packageErr != nil || samePath(packageRoot, stagingRoot) || !pathWithin(stagingRoot, packageRoot) {
		return Transaction{}, errors.New("update transaction package root is outside its staging directory")
	}
	if comparison, comparable := CompareVersions(transaction.CurrentVersion, transaction.TargetVersion); !comparable || comparison <= 0 {
		return Transaction{}, errors.New("update transaction versions are invalid")
	}
	if err := validateTransactionStatusURLs(transaction.HealthURL, transaction.InfoURL); err != nil {
		return Transaction{}, err
	}
	updateRoot := filepath.Join(root, ".update")
	dataPaths := make([]string, 0, len(transaction.DataPaths))
	for _, candidate := range transaction.DataPaths {
		absolute, pathErr := filepath.Abs(candidate)
		if pathErr != nil || samePath(root, absolute) || !pathWithin(root, absolute) ||
			pathWithin(updateRoot, absolute) || pathWithin(manifest.BackupRoot, absolute) {
			return Transaction{}, fmt.Errorf("update transaction path escapes install root")
		}
		for _, existing := range dataPaths {
			if samePath(existing, absolute) || pathWithin(existing, absolute) || pathWithin(absolute, existing) {
				return Transaction{}, errors.New("update transaction data paths overlap")
			}
		}
		dataPaths = append(dataPaths, absolute)
	}
	return transaction, nil
}

func validTransactionID(value string) bool {
	if len(value) != 32 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateTransactionStatusURLs(healthURL, infoURL string) error {
	health, err := parseTransactionStatusURL(healthURL, "/health")
	if err != nil {
		return err
	}
	info, err := parseTransactionStatusURL(infoURL, "/usage-service/info")
	if err != nil {
		return err
	}
	if !strings.EqualFold(health.Scheme, info.Scheme) || !strings.EqualFold(health.Host, info.Host) {
		return errors.New("update transaction status URLs must use the same local server origin")
	}
	return nil
}

func parseTransactionStatusURL(value, expectedPath string) (*url.URL, error) {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != expectedPath || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return nil, errors.New("update transaction status URL is invalid")
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		return nil, errors.New("update transaction status URL must include a valid port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, errors.New("update transaction status URL must include a valid port")
	}
	return parsed, nil
}

func ValidateTransactionStatus(manifest InstallManifest, transaction Transaction, status TransactionStatus) error {
	if status.TransactionID != transaction.TransactionID || status.InstallID != manifest.InstallID ||
		NormalizeVersion(status.CurrentVersion) != NormalizeVersion(transaction.CurrentVersion) ||
		NormalizeVersion(status.TargetVersion) != NormalizeVersion(transaction.TargetVersion) {
		return errors.New("update transaction does not match its status")
	}
	return nil
}
