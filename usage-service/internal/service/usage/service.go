package usage

import (
	"context"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/store"
	usageparser "github.com/seakee/cpa-manager-plus/usage-service/internal/usage"
)

type ImportResult struct {
	Format      string   `json:"format"`
	Added       int      `json:"added"`
	Skipped     int      `json:"skipped"`
	Total       int      `json:"total"`
	Failed      int      `json:"failed"`
	Unsupported int      `json:"unsupported"`
	Warnings    []string `json:"warnings"`
}

type Service struct {
	store *store.Store
}

func New(store *store.Store) *Service {
	return &Service{store: store}
}

func (s *Service) GetCompatibleUsage(ctx context.Context, limit int) (usageparser.Payload, error) {
	events, err := s.store.RecentEvents(ctx, limit)
	if err != nil {
		return usageparser.Payload{}, err
	}
	return usageparser.BuildPayload(events), nil
}

func (s *Service) Export(ctx context.Context) ([]byte, error) {
	return s.store.ExportJSONL(ctx)
}

func (s *Service) Import(ctx context.Context, data []byte) (ImportResult, *usageparser.ImportParseResult, error) {
	parsed, err := usageparser.ParseImportPayload(data)
	if err != nil {
		return ImportResult{}, &parsed, err
	}

	result, err := s.store.InsertEvents(ctx, parsed.Events)
	if err != nil {
		return ImportResult{}, &parsed, err
	}
	return ImportResult{
		Format:      parsed.Format,
		Added:       result.Inserted,
		Skipped:     result.Skipped,
		Total:       len(parsed.Events),
		Failed:      parsed.Failed,
		Unsupported: parsed.Unsupported,
		Warnings:    parsed.Warnings,
	}, &parsed, nil
}

func (s *Service) Counts(ctx context.Context) (events int64, deadLetters int64, err error) {
	return s.store.Counts(ctx)
}
