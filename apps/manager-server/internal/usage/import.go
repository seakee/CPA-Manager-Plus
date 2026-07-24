package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ImportFormatJSONL         = "usage_service_jsonl"
	ImportFormatLegacyExport  = "legacy_usage_export"
	ImportFormatLegacyPayload = "legacy_usage_payload"
)

var (
	ErrUnsupportedImportFormat = errors.New("unsupported usage import format")
	ErrLegacyUsageNoDetails    = errors.New("legacy usage export does not contain request details")
)

type ImportParseResult struct {
	Format      string
	Events      []Event
	Failed      int
	Unsupported int
	Warnings    []string
}

type ImportStreamResult struct {
	Format      string
	Total       int
	Failed      int
	Unsupported int
	Warnings    []string
}

type importBatcher struct {
	batchSize int
	batch     []Event
	total     int
	consume   func([]Event) error
}

func StreamImportPayload(reader io.Reader, batchSize int, consume func([]Event) error) (ImportStreamResult, error) {
	if batchSize <= 0 {
		batchSize = 256
	}
	batcher := &importBatcher{
		batchSize: batchSize,
		batch:     make([]Event, 0, batchSize),
		consume:   consume,
	}
	if seeker, ok := reader.(io.ReadSeeker); ok {
		result, handled, err := streamSeekableLegacyImport(seeker, batcher)
		if err != nil || handled {
			result.Total = batcher.total
			return result, err
		}
	}

	buffered := bufio.NewReader(reader)
	first, err := peekNonWhitespaceByte(buffered)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return ImportStreamResult{}, errors.New("empty usage import payload")
		}
		return ImportStreamResult{}, err
	}

	var result ImportStreamResult
	switch first {
	case '[':
		result, err = streamJSONArrayImport(buffered, batcher)
	case '{':
		result, err = streamJSONObjectOrJSONLImport(buffered, batcher)
	default:
		result = ImportStreamResult{Format: ImportFormatJSONL}
		err = streamJSONLImport(buffered, batcher, &result)
	}
	result.Total = batcher.total
	return result, err
}

type legacyStreamShape struct {
	format        string
	wrapped       bool
	endpointRanks map[string]int
	modelRanks    map[string]map[string]int
}

func streamSeekableLegacyImport(reader io.ReadSeeker, batcher *importBatcher) (ImportStreamResult, bool, error) {
	start, err := reader.Seek(0, io.SeekCurrent)
	if err != nil {
		return ImportStreamResult{}, false, err
	}
	shape, inspectErr := inspectLegacyStreamShape(reader)
	_, rewindErr := reader.Seek(start, io.SeekStart)
	if inspectErr != nil {
		return ImportStreamResult{}, false, inspectErr
	}
	if rewindErr != nil {
		return ImportStreamResult{}, false, rewindErr
	}
	if shape.format == "" {
		return ImportStreamResult{}, false, nil
	}
	result, err := streamLegacyObjectImport(reader, shape, batcher)
	return result, true, err
}

func inspectLegacyStreamShape(reader io.Reader) (legacyStreamShape, error) {
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return legacyStreamShape{}, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return legacyStreamShape{}, nil
	}

	var usageSeen bool
	var usageFound bool
	var usageEndpointKeys []string
	var usageModelKeys map[string][]string
	var directAPIsSeen bool
	var directAPIsFound bool
	var directEndpointKeys []string
	var directModelKeys map[string][]string
	var eventHashSeen bool
	var eventHashNonEmpty bool
	var camelEventHashSeen bool
	var camelEventHashNonEmpty bool
	for decoder.More() {
		key, err := nextJSONObjectKey(decoder)
		if err != nil {
			return legacyStreamShape{}, err
		}
		switch key {
		case "usage":
			if usageSeen {
				return legacyStreamShape{}, duplicateLegacyStructuralKeyError("usage")
			}
			usageSeen = true
			usageEndpointKeys, usageModelKeys, usageFound, err = inspectLegacyUsageValue(decoder)
			if err != nil {
				return legacyStreamShape{}, err
			}
		case "apis":
			if directAPIsSeen {
				return legacyStreamShape{}, duplicateLegacyStructuralKeyError("apis")
			}
			directAPIsSeen = true
			directEndpointKeys, directModelKeys, directAPIsFound, err = inspectLegacyAPIsValue(decoder)
			if err != nil {
				return legacyStreamShape{}, err
			}
		case "event_hash":
			eventHashSeen = true
			eventHashNonEmpty, err = inspectExportedEventHashValue(decoder)
			if err != nil {
				return legacyStreamShape{}, err
			}
		case "eventHash":
			camelEventHashSeen = true
			camelEventHashNonEmpty, err = inspectExportedEventHashValue(decoder)
			if err != nil {
				return legacyStreamShape{}, err
			}
		default:
			if err := skipNextJSONValue(decoder); err != nil {
				return legacyStreamShape{}, err
			}
		}
	}
	if err := consumeJSONDelimiter(decoder, '}'); err != nil {
		return legacyStreamShape{}, err
	}
	exportedEvent := eventHashNonEmpty
	if !eventHashSeen && camelEventHashSeen {
		exportedEvent = camelEventHashNonEmpty
	}
	if exportedEvent {
		return legacyStreamShape{}, nil
	}
	var shape legacyStreamShape
	if usageSeen {
		if usageFound {
			shape = buildLegacyStreamShape(ImportFormatLegacyExport, true, usageEndpointKeys, usageModelKeys)
		}
	} else if directAPIsFound {
		shape = buildLegacyStreamShape(ImportFormatLegacyPayload, false, directEndpointKeys, directModelKeys)
	}
	if shape.format != "" {
		if err := ensureDecoderEOF(decoder); err != nil {
			return legacyStreamShape{}, err
		}
	}
	return shape, nil
}

func inspectExportedEventHashValue(decoder *json.Decoder) (bool, error) {
	token, err := decoder.Token()
	if err != nil {
		return false, err
	}
	if delimiter, ok := token.(json.Delim); ok {
		return true, skipJSONValueFromToken(decoder, delimiter)
	}
	if token == nil {
		return false, nil
	}
	if value, ok := token.(string); ok {
		return strings.TrimSpace(value) != "", nil
	}
	return true, nil
}

func duplicateLegacyStructuralKeyError(key string) error {
	return fmt.Errorf("usage import legacy object contains duplicate structural key %q", key)
}

func inspectLegacyUsageValue(decoder *json.Decoder) ([]string, map[string][]string, bool, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, nil, false, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, nil, false, skipJSONValueFromToken(decoder, token)
	}

	var endpointKeys []string
	var modelKeys map[string][]string
	found := false
	apisSeen := false
	for decoder.More() {
		key, err := nextJSONObjectKey(decoder)
		if err != nil {
			return nil, nil, false, err
		}
		if key != "apis" {
			if err := skipNextJSONValue(decoder); err != nil {
				return nil, nil, false, err
			}
			continue
		}
		if apisSeen {
			return nil, nil, false, duplicateLegacyStructuralKeyError("usage.apis")
		}
		apisSeen = true
		keys, models, ok, err := inspectLegacyAPIsValue(decoder)
		if err != nil {
			return nil, nil, false, err
		}
		if ok {
			endpointKeys = keys
			modelKeys = models
			found = true
		}
	}
	if err := consumeJSONDelimiter(decoder, '}'); err != nil {
		return nil, nil, false, err
	}
	return endpointKeys, modelKeys, found, nil
}

func inspectLegacyAPIsValue(decoder *json.Decoder) ([]string, map[string][]string, bool, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, nil, false, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, nil, false, skipJSONValueFromToken(decoder, token)
	}

	endpointSet := make(map[string]struct{})
	modelKeys := make(map[string][]string)
	for decoder.More() {
		endpoint, err := nextJSONObjectKey(decoder)
		if err != nil {
			return nil, nil, false, err
		}
		if _, exists := endpointSet[endpoint]; exists {
			return nil, nil, false, duplicateLegacyStructuralKeyError("apis." + endpoint)
		}
		endpointSet[endpoint] = struct{}{}
		models, err := inspectLegacyEndpointValue(decoder)
		if err != nil {
			return nil, nil, false, err
		}
		if models != nil {
			modelKeys[endpoint] = models
		}
	}
	if err := consumeJSONDelimiter(decoder, '}'); err != nil {
		return nil, nil, false, err
	}
	if len(endpointSet) == 0 {
		return nil, nil, false, nil
	}
	endpointKeys := make([]string, 0, len(endpointSet))
	for endpoint := range endpointSet {
		endpointKeys = append(endpointKeys, endpoint)
	}
	return endpointKeys, modelKeys, true, nil
}

func inspectLegacyEndpointValue(decoder *json.Decoder) ([]string, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, skipJSONValueFromToken(decoder, token)
	}

	var modelKeys []string
	modelsSeen := false
	for decoder.More() {
		key, err := nextJSONObjectKey(decoder)
		if err != nil {
			return nil, err
		}
		if key != "models" {
			if err := skipNextJSONValue(decoder); err != nil {
				return nil, err
			}
			continue
		}
		if modelsSeen {
			return nil, duplicateLegacyStructuralKeyError("models")
		}
		modelsSeen = true
		keys, err := inspectLegacyModelsValue(decoder)
		if err != nil {
			return nil, err
		}
		modelKeys = keys
	}
	if err := consumeJSONDelimiter(decoder, '}'); err != nil {
		return nil, err
	}
	return modelKeys, nil
}

func inspectLegacyModelsValue(decoder *json.Decoder) ([]string, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, skipJSONValueFromToken(decoder, token)
	}

	modelSet := make(map[string]struct{})
	for decoder.More() {
		model, err := nextJSONObjectKey(decoder)
		if err != nil {
			return nil, err
		}
		if _, exists := modelSet[model]; exists {
			return nil, duplicateLegacyStructuralKeyError("models." + model)
		}
		modelSet[model] = struct{}{}
		if err := inspectLegacyModelValue(decoder); err != nil {
			return nil, err
		}
	}
	if err := consumeJSONDelimiter(decoder, '}'); err != nil {
		return nil, err
	}
	modelKeys := make([]string, 0, len(modelSet))
	for model := range modelSet {
		modelKeys = append(modelKeys, model)
	}
	return modelKeys, nil
}

func inspectLegacyModelValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return skipJSONValueFromToken(decoder, token)
	}

	detailsSeen := false
	for decoder.More() {
		key, err := nextJSONObjectKey(decoder)
		if err != nil {
			return err
		}
		if key == "details" {
			if detailsSeen {
				return duplicateLegacyStructuralKeyError("details")
			}
			detailsSeen = true
		}
		if err := skipNextJSONValue(decoder); err != nil {
			return err
		}
	}
	return consumeJSONDelimiter(decoder, '}')
}

func buildLegacyStreamShape(
	format string,
	wrapped bool,
	endpointKeys []string,
	modelKeys map[string][]string,
) legacyStreamShape {
	return legacyStreamShape{
		format:        format,
		wrapped:       wrapped,
		endpointRanks: rankSortedStrings(endpointKeys),
		modelRanks: func() map[string]map[string]int {
			ranks := make(map[string]map[string]int, len(modelKeys))
			for endpoint, keys := range modelKeys {
				ranks[endpoint] = rankSortedStrings(keys)
			}
			return ranks
		}(),
	}
}

func rankSortedStrings(values []string) map[string]int {
	values = append([]string(nil), values...)
	sort.Strings(values)
	ranks := make(map[string]int, len(values))
	for index, value := range values {
		ranks[value] = index + 1
	}
	return ranks
}

func streamLegacyObjectImport(
	reader io.Reader,
	shape legacyStreamShape,
	batcher *importBatcher,
) (ImportStreamResult, error) {
	result := ImportStreamResult{
		Format: shape.format,
		Warnings: []string{
			"legacy_usage_metadata_is_partial",
			"legacy_usage_source_matching_may_be_approximate",
		},
	}
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	nowMS := time.Now().UnixMilli()
	if err := expectJSONDelimiter(decoder, '{'); err != nil {
		return result, err
	}
	processed := false
	for decoder.More() {
		key, err := nextJSONObjectKey(decoder)
		if err != nil {
			return result, err
		}
		if shape.wrapped && key == "usage" && !processed {
			processed, err = streamLegacyUsageValue(decoder, shape, batcher, &result, nowMS)
			if err != nil {
				return result, err
			}
			continue
		}
		if !shape.wrapped && key == "apis" && !processed {
			processed, err = streamLegacyAPIsValue(decoder, shape, batcher, &result, nowMS)
			if err != nil {
				return result, err
			}
			continue
		}
		if err := skipNextJSONValue(decoder); err != nil {
			return result, err
		}
	}
	if err := consumeJSONDelimiter(decoder, '}'); err != nil {
		return result, err
	}
	if err := ensureDecoderEOF(decoder); err != nil {
		return result, err
	}
	if !processed || batcher.total == 0 {
		return result, ErrLegacyUsageNoDetails
	}
	return result, batcher.flush()
}

func streamLegacyUsageValue(
	decoder *json.Decoder,
	shape legacyStreamShape,
	batcher *importBatcher,
	result *ImportStreamResult,
	nowMS int64,
) (bool, error) {
	token, err := decoder.Token()
	if err != nil {
		return false, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return false, skipJSONValueFromToken(decoder, token)
	}

	processed := false
	for decoder.More() {
		key, err := nextJSONObjectKey(decoder)
		if err != nil {
			return false, err
		}
		if key == "apis" && !processed {
			processed, err = streamLegacyAPIsValue(decoder, shape, batcher, result, nowMS)
			if err != nil {
				return false, err
			}
			continue
		}
		if err := skipNextJSONValue(decoder); err != nil {
			return false, err
		}
	}
	if err := consumeJSONDelimiter(decoder, '}'); err != nil {
		return false, err
	}
	return processed, nil
}

func streamLegacyAPIsValue(
	decoder *json.Decoder,
	shape legacyStreamShape,
	batcher *importBatcher,
	result *ImportStreamResult,
	nowMS int64,
) (bool, error) {
	token, err := decoder.Token()
	if err != nil {
		return false, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return false, skipJSONValueFromToken(decoder, token)
	}

	for decoder.More() {
		endpoint, err := nextJSONObjectKey(decoder)
		if err != nil {
			return false, err
		}
		if err := streamLegacyEndpointValue(
			decoder,
			endpoint,
			shape.endpointRanks[endpoint],
			shape.modelRanks[endpoint],
			batcher,
			result,
			nowMS,
		); err != nil {
			return false, err
		}
	}
	if err := consumeJSONDelimiter(decoder, '}'); err != nil {
		return false, err
	}
	return true, nil
}

func streamLegacyEndpointValue(
	decoder *json.Decoder,
	endpoint string,
	endpointIndex int,
	modelRanks map[string]int,
	batcher *importBatcher,
	result *ImportStreamResult,
	nowMS int64,
) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		result.Failed++
		return skipJSONValueFromToken(decoder, token)
	}

	modelsFound := false
	for decoder.More() {
		key, err := nextJSONObjectKey(decoder)
		if err != nil {
			return err
		}
		if key == "models" && !modelsFound {
			modelsFound, err = streamLegacyModelsValue(
				decoder,
				endpoint,
				endpointIndex,
				modelRanks,
				batcher,
				result,
				nowMS,
			)
			if err != nil {
				return err
			}
			continue
		}
		if err := skipNextJSONValue(decoder); err != nil {
			return err
		}
	}
	if err := consumeJSONDelimiter(decoder, '}'); err != nil {
		return err
	}
	if !modelsFound {
		result.Failed++
	}
	return nil
}

func streamLegacyModelsValue(
	decoder *json.Decoder,
	endpoint string,
	endpointIndex int,
	modelRanks map[string]int,
	batcher *importBatcher,
	result *ImportStreamResult,
	nowMS int64,
) (bool, error) {
	token, err := decoder.Token()
	if err != nil {
		return false, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return false, skipJSONValueFromToken(decoder, token)
	}

	for decoder.More() {
		model, err := nextJSONObjectKey(decoder)
		if err != nil {
			return false, err
		}
		if err := streamLegacyModelValue(
			decoder,
			endpoint,
			model,
			endpointIndex,
			modelRanks[model],
			batcher,
			result,
			nowMS,
		); err != nil {
			return false, err
		}
	}
	if err := consumeJSONDelimiter(decoder, '}'); err != nil {
		return false, err
	}
	return true, nil
}

func streamLegacyModelValue(
	decoder *json.Decoder,
	endpoint string,
	model string,
	endpointIndex int,
	modelIndex int,
	batcher *importBatcher,
	result *ImportStreamResult,
	nowMS int64,
) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		result.Failed++
		return skipJSONValueFromToken(decoder, token)
	}

	detailsFound := false
	for decoder.More() {
		key, err := nextJSONObjectKey(decoder)
		if err != nil {
			return err
		}
		if key == "details" && !detailsFound {
			method, path := parseEndpoint(endpoint)
			detailsFound, err = streamLegacyDetailsValue(
				decoder,
				endpoint,
				method,
				path,
				model,
				endpointIndex,
				modelIndex,
				batcher,
				result,
				nowMS,
			)
			if err != nil {
				return err
			}
			continue
		}
		if err := skipNextJSONValue(decoder); err != nil {
			return err
		}
	}
	if err := consumeJSONDelimiter(decoder, '}'); err != nil {
		return err
	}
	if !detailsFound {
		result.Unsupported++
	}
	return nil
}

func streamLegacyDetailsValue(
	decoder *json.Decoder,
	endpoint string,
	method string,
	path string,
	model string,
	endpointIndex int,
	modelIndex int,
	batcher *importBatcher,
	result *ImportStreamResult,
	nowMS int64,
) (bool, error) {
	token, err := decoder.Token()
	if err != nil {
		return false, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '[' {
		return false, skipJSONValueFromToken(decoder, token)
	}

	detailIndex := 0
	for decoder.More() {
		var raw any
		if err := decoder.Decode(&raw); err != nil {
			return false, err
		}
		detail, ok := raw.(map[string]any)
		if !ok {
			result.Failed++
			detailIndex++
			continue
		}
		event, err := eventFromLegacyDetail(
			endpoint,
			method,
			path,
			model,
			detail,
			endpointIndex,
			modelIndex,
			detailIndex,
			nowMS,
		)
		detailIndex++
		if err != nil {
			result.Failed++
			continue
		}
		if err := batcher.add(event); err != nil {
			return false, err
		}
	}
	if err := consumeJSONDelimiter(decoder, ']'); err != nil {
		return false, err
	}
	return detailIndex > 0, nil
}

func nextJSONObjectKey(decoder *json.Decoder) (string, error) {
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	key, ok := token.(string)
	if !ok {
		return "", errors.New("usage import object key is invalid")
	}
	return key, nil
}

func expectJSONDelimiter(decoder *json.Decoder, expected json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != expected {
		return ErrUnsupportedImportFormat
	}
	return nil
}

func consumeJSONDelimiter(decoder *json.Decoder, expected json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != expected {
		return errors.New("usage import JSON delimiter is invalid")
	}
	return nil
}

func skipNextJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	return skipJSONValueFromToken(decoder, token)
}

func skipJSONValueFromToken(decoder *json.Decoder, token json.Token) error {
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		for decoder.More() {
			if _, err := nextJSONObjectKey(decoder); err != nil {
				return err
			}
			if err := skipNextJSONValue(decoder); err != nil {
				return err
			}
		}
		return consumeJSONDelimiter(decoder, '}')
	case '[':
		for decoder.More() {
			if err := skipNextJSONValue(decoder); err != nil {
				return err
			}
		}
		return consumeJSONDelimiter(decoder, ']')
	default:
		return errors.New("usage import JSON delimiter is invalid")
	}
}

func streamJSONArrayImport(reader io.Reader, batcher *importBatcher) (ImportStreamResult, error) {
	result := ImportStreamResult{Format: ImportFormatJSONL}
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return result, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
		return result, ErrUnsupportedImportFormat
	}
	for decoder.More() {
		var item json.RawMessage
		if err := decoder.Decode(&item); err != nil {
			return result, err
		}
		event, err := eventFromJSONRecord(item)
		if err != nil {
			result.Failed++
			continue
		}
		if err := batcher.add(event); err != nil {
			return result, err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return result, err
	}
	if err := ensureDecoderEOF(decoder); err != nil {
		return result, err
	}
	return result, batcher.flush()
}

func streamJSONObjectOrJSONLImport(reader io.Reader, batcher *importBatcher) (ImportStreamResult, error) {
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	var first json.RawMessage
	if err := decoder.Decode(&first); err != nil {
		return ImportStreamResult{Format: ImportFormatJSONL}, err
	}

	parsed, err := parseJSONObjectImport(first)
	if parsed.Format == ImportFormatLegacyExport || parsed.Format == ImportFormatLegacyPayload {
		result := ImportStreamResult{
			Format:      parsed.Format,
			Failed:      parsed.Failed,
			Unsupported: parsed.Unsupported,
			Warnings:    parsed.Warnings,
		}
		if err != nil {
			return result, err
		}
		if err := ensureOnlyWhitespace(io.MultiReader(decoder.Buffered(), reader)); err != nil {
			return result, err
		}
		for _, event := range parsed.Events {
			if err := batcher.add(event); err != nil {
				return result, err
			}
		}
		return result, batcher.flush()
	}
	if err != nil {
		return ImportStreamResult{Format: ImportFormatJSONL, Failed: parsed.Failed}, err
	}

	result := ImportStreamResult{Format: ImportFormatJSONL, Failed: parsed.Failed}
	for _, event := range parsed.Events {
		if err := batcher.add(event); err != nil {
			return result, err
		}
	}
	if err := streamJSONLImport(io.MultiReader(decoder.Buffered(), reader), batcher, &result); err != nil {
		return result, err
	}
	return result, nil
}

func streamJSONLImport(reader io.Reader, batcher *importBatcher, result *ImportStreamResult) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		event, err := eventFromJSONRecord([]byte(line))
		if err != nil {
			result.Failed++
			continue
		}
		if err := batcher.add(event); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return batcher.flush()
}

func (b *importBatcher) add(event Event) error {
	b.batch = append(b.batch, event)
	b.total++
	if len(b.batch) < b.batchSize {
		return nil
	}
	return b.flush()
}

func (b *importBatcher) flush() error {
	if len(b.batch) == 0 {
		return nil
	}
	// Import is intentionally batched rather than all-or-nothing: batches that
	// completed before a later parse, size-limit, or database error stay committed.
	if err := b.consume(b.batch); err != nil {
		return err
	}
	b.batch = b.batch[:0]
	return nil
}

func peekNonWhitespaceByte(reader *bufio.Reader) (byte, error) {
	for {
		value, err := reader.ReadByte()
		if err != nil {
			return 0, err
		}
		switch value {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			if err := reader.UnreadByte(); err != nil {
				return 0, err
			}
			return value, nil
		}
	}
}

func ensureDecoderEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("usage import payload contains multiple JSON values")
		}
		return err
	}
	return nil
}

func ensureOnlyWhitespace(reader io.Reader) error {
	buffer := make([]byte, 4096)
	for {
		read, err := reader.Read(buffer)
		for _, value := range buffer[:read] {
			switch value {
			case ' ', '\t', '\r', '\n':
			default:
				return errors.New("usage import payload contains multiple JSON values")
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func ParseImportPayload(data []byte) (ImportParseResult, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return ImportParseResult{}, errors.New("empty usage import payload")
	}

	switch trimmed[0] {
	case '{':
		result, err := parseJSONObjectImport(trimmed)
		if err != nil && bytes.Contains(trimmed, []byte{'\n'}) && !errors.Is(err, ErrLegacyUsageNoDetails) {
			return parseJSONLImport(trimmed)
		}
		return result, err
	case '[':
		return parseJSONArrayImport(trimmed)
	default:
		return parseJSONLImport(trimmed)
	}
}

func parseJSONObjectImport(data []byte) (ImportParseResult, error) {
	var record map[string]any
	if err := decodeJSON(data, &record); err != nil {
		return ImportParseResult{}, err
	}
	if event, ok, err := eventFromExportedRecord(record); ok || err != nil {
		if err != nil {
			return ImportParseResult{Format: ImportFormatJSONL, Failed: 1}, err
		}
		return ImportParseResult{Format: ImportFormatJSONL, Events: []Event{event}}, nil
	}

	if usageRaw, ok := record["usage"]; ok {
		usageRecord, ok := usageRaw.(map[string]any)
		if !ok {
			return ImportParseResult{}, ErrLegacyUsageNoDetails
		}
		if hasUsageAPIs(usageRecord) {
			result, err := eventsFromLegacyUsage(usageRecord, ImportFormatLegacyExport)
			if err != nil {
				return result, err
			}
			return result, nil
		}
		return ImportParseResult{
			Format:      ImportFormatLegacyExport,
			Unsupported: 1,
		}, ErrLegacyUsageNoDetails
	}

	if hasUsageAPIs(record) {
		return eventsFromLegacyUsage(record, ImportFormatLegacyPayload)
	}

	if looksLikeLegacyUsageSummary(record) {
		return ImportParseResult{
			Format:      ImportFormatLegacyPayload,
			Unsupported: 1,
		}, ErrLegacyUsageNoDetails
	}

	event, err := NormalizeRaw(data)
	if err != nil {
		return ImportParseResult{Format: ImportFormatJSONL, Failed: 1}, err
	}
	return ImportParseResult{Format: ImportFormatJSONL, Events: []Event{event}}, nil
}

func parseJSONArrayImport(data []byte) (ImportParseResult, error) {
	var items []json.RawMessage
	if err := decodeJSON(data, &items); err != nil {
		return ImportParseResult{}, err
	}

	result := ImportParseResult{Format: ImportFormatJSONL}
	for _, item := range items {
		event, err := eventFromJSONRecord(item)
		if err != nil {
			result.Failed++
			continue
		}
		result.Events = append(result.Events, event)
	}
	return result, nil
}

func parseJSONLImport(data []byte) (ImportParseResult, error) {
	result := ImportParseResult{Format: ImportFormatJSONL}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		event, err := eventFromJSONRecord([]byte(line))
		if err != nil {
			result.Failed++
			continue
		}
		result.Events = append(result.Events, event)
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	return result, nil
}

func eventFromJSONRecord(data []byte) (Event, error) {
	var record map[string]any
	if err := decodeJSON(data, &record); err != nil {
		return Event{}, err
	}
	if event, ok, err := eventFromExportedRecord(record); ok || err != nil {
		return event, err
	}
	return NormalizeRaw(data)
}

func eventFromExportedRecord(record map[string]any) (Event, bool, error) {
	eventHash := readString(record, "event_hash", "eventHash")
	if eventHash == "" {
		return Event{}, false, nil
	}

	timestampMS := readInt(record, "timestamp_ms", "timestampMs")
	timestamp := readString(record, "timestamp")
	if timestampMS <= 0 || timestamp == "" {
		parsedMS, parsedTimestamp := readTimestamp(record)
		if timestampMS <= 0 {
			timestampMS = parsedMS
		}
		if timestamp == "" {
			timestamp = parsedTimestamp
		}
	}

	inputTokens, outputTokens, reasoningTokens, cachedTokens, cacheTokens, cacheReadTokens, cacheCreationTokens, totalTokens := readTokenFields(record)
	failStatusCode, failBody := readFailFields(record)
	failSummary := readString(record, "fail_summary", "failSummary")
	if failSummary == "" {
		failSummary = FailSummaryFromBody(failBody)
	}

	requestedModel := readString(record, "requested_model", "requestedModel")
	resolvedModel := readString(record, "resolved_model", "resolvedModel")
	model := readString(record, "model")
	if model == "" {
		model = requestedModel
	}
	if model == "" {
		model = resolvedModel
	}
	if model == "" {
		model = "-"
	}
	provider := readString(record, "provider")
	executorType := readString(record, "executor_type", "executorType")
	providerSnapshot := readString(record, "auth_provider_snapshot", "authProviderSnapshot")
	rawJSON := importCacheAccountingRawJSON(record)
	rawHints := RawCacheAccountingHintsFromJSON(rawJSON)
	explicitMode := cacheInputModeFromRecord(record)
	if explicitMode == "" {
		explicitMode = rawHints.ExplicitMode
	}
	accounting := NormalizeCacheAccounting(CacheInputContext{
		ExplicitMode:     explicitMode,
		ExecutorType:     executorType,
		Provider:         provider,
		ProviderSnapshot: providerSnapshot,
		ResolvedModel:    resolvedModel,
		RequestedModel:   requestedModel,
		DisplayModel:     model,
	}, inputTokens, cachedTokens, cacheTokens, cacheReadTokens, cacheCreationTokens)
	if totalTokens <= 0 && rawHints.HasExplicitTotal {
		totalTokens = rawHints.ExplicitTotal
	}
	if totalTokens <= 0 {
		totalTokens = accounting.TotalInputTokens + maxInt64(outputTokens, 0) + maxInt64(reasoningTokens, 0)
	}

	event := Event{
		RequestID:                     readString(record, "request_id", "requestId"),
		EventHash:                     eventHash,
		TimestampMS:                   timestampMS,
		Timestamp:                     timestamp,
		Provider:                      provider,
		ExecutorType:                  executorType,
		Model:                         model,
		RequestedModel:                requestedModel,
		ResolvedModel:                 resolvedModel,
		Endpoint:                      readString(record, "endpoint"),
		Method:                        readString(record, "method"),
		Path:                          readString(record, "path"),
		AuthType:                      readString(record, "auth_type", "authType"),
		AuthIndex:                     readString(record, "auth_index", "authIndex", "AuthIndex"),
		Source:                        readString(record, "source"),
		SourceHash:                    readString(record, "source_hash", "sourceHash"),
		APIKeyHash:                    readString(record, "api_key_hash", "apiKeyHash"),
		AccountSnapshot:               readString(record, "account_snapshot", "accountSnapshot"),
		AuthLabelSnapshot:             readString(record, "auth_label_snapshot", "authLabelSnapshot"),
		AuthFileSnapshot:              readString(record, "auth_file_snapshot", "authFileSnapshot"),
		AuthProviderSnapshot:          providerSnapshot,
		AuthProjectIDSnapshot:         readString(record, "auth_project_id_snapshot", "authProjectIdSnapshot"),
		AuthSnapshotAtMS:              readInt(record, "auth_snapshot_at_ms", "authSnapshotAtMs"),
		ReasoningEffort:               readString(record, "reasoning_effort", "reasoningEffort"),
		ServiceTier:                   readString(record, "service_tier", "serviceTier"),
		RequestServiceTier:            readString(record, "request_service_tier", "requestServiceTier"),
		ResponseServiceTier:           readString(record, "response_service_tier", "responseServiceTier"),
		CacheInputMode:                accounting.Mode,
		InputTokens:                   inputTokens,
		OutputTokens:                  outputTokens,
		ReasoningTokens:               reasoningTokens,
		CachedTokens:                  cachedTokens,
		CacheTokens:                   cacheTokens,
		CacheReadTokens:               cacheReadTokens,
		CacheCreationTokens:           cacheCreationTokens,
		NormalizedUncachedInputTokens: accounting.UncachedInputTokens,
		NormalizedTotalInputTokens:    accounting.TotalInputTokens,
		NormalizedCacheReadTokens:     accounting.CacheReadTokens,
		NormalizedCacheCreationTokens: accounting.CacheCreationTokens,
		TotalTokens:                   totalTokens,
		LatencyMS:                     readOptionalInt(record, "latency_ms", "latencyMs"),
		TTFTMS:                        readOptionalInt(record, "ttft_ms", "ttftMs", "time_to_first_token_ms", "timeToFirstTokenMs"),
		Failed:                        readBool(record, "failed", "is_failed", "isFailed"),
		FailStatusCode:                int(failStatusCode),
		FailSummary:                   failSummary,
		FailBody:                      failBody,
		RawJSON:                       rawJSON,
		CreatedAtMS:                   readInt(record, "created_at_ms", "createdAtMs"),
	}
	event.ServiceTier = EffectiveServiceTier(CacheInputContext{
		ExecutorType:     event.ExecutorType,
		Provider:         event.Provider,
		ProviderSnapshot: event.AuthProviderSnapshot,
		AuthType:         event.AuthType,
	}, event.RequestServiceTier, event.ServiceTier, event.ResponseServiceTier)
	if event.Endpoint == "" {
		event.Endpoint = "-"
	}
	AttachResponseHeaderMetadata(&event, ResponseHeaderMetadataFromRecord(record, time.UnixMilli(timestampMS)))
	if event.CreatedAtMS <= 0 {
		event.CreatedAtMS = time.Now().UnixMilli()
	}
	return event, true, nil
}

func eventsFromLegacyUsage(usageRecord map[string]any, format string) (ImportParseResult, error) {
	apisRaw, ok := usageRecord["apis"].(map[string]any)
	if !ok {
		return ImportParseResult{Format: format, Unsupported: 1}, ErrLegacyUsageNoDetails
	}

	result := ImportParseResult{
		Format: format,
		Warnings: []string{
			"legacy_usage_metadata_is_partial",
			"legacy_usage_source_matching_may_be_approximate",
		},
	}
	now := time.Now().UnixMilli()
	endpointIndex := 0
	for _, endpoint := range sortedKeys(apisRaw) {
		apiRaw := apisRaw[endpoint]
		endpointIndex++
		apiEntry, ok := apiRaw.(map[string]any)
		if !ok {
			result.Failed++
			continue
		}
		modelsRaw, ok := apiEntry["models"].(map[string]any)
		if !ok {
			result.Failed++
			continue
		}

		method, path := parseEndpoint(endpoint)
		modelIndex := 0
		for _, model := range sortedKeys(modelsRaw) {
			modelRaw := modelsRaw[model]
			modelIndex++
			modelEntry, ok := modelRaw.(map[string]any)
			if !ok {
				result.Failed++
				continue
			}
			detailsRaw, ok := modelEntry["details"].([]any)
			if !ok || len(detailsRaw) == 0 {
				result.Unsupported++
				continue
			}
			for detailIndex, detailRaw := range detailsRaw {
				detail, ok := detailRaw.(map[string]any)
				if !ok {
					result.Failed++
					continue
				}
				event, err := eventFromLegacyDetail(
					endpoint,
					method,
					path,
					model,
					detail,
					endpointIndex,
					modelIndex,
					detailIndex,
					now,
				)
				if err != nil {
					result.Failed++
					continue
				}
				result.Events = append(result.Events, event)
			}
		}
	}

	if len(result.Events) == 0 {
		return result, ErrLegacyUsageNoDetails
	}
	return result, nil
}

func eventFromLegacyDetail(
	endpoint string,
	method string,
	path string,
	model string,
	detail map[string]any,
	endpointIndex int,
	modelIndex int,
	detailIndex int,
	now int64,
) (Event, error) {
	timestamp := readString(detail, "timestamp", "time", "created_at", "createdAt")
	if timestamp == "" {
		return Event{}, errors.New("legacy usage detail missing timestamp")
	}
	timestampMS, normalizedTimestamp := readTimestamp(detail)

	inputTokens, outputTokens, reasoningTokens, cachedTokens, cacheTokens, cacheReadTokens, cacheCreationTokens, totalTokens := readTokenFields(detail)
	failStatusCode, failBody := readFailFields(detail)
	failSummary := readString(detail, "fail_summary", "failSummary")
	if failSummary == "" {
		failSummary = FailSummaryFromBody(failBody)
	}

	sourceRaw := readString(detail, "source", "api_key", "apiKey", "key", "account", "email")
	apiKey := readString(detail, "api_key", "apiKey", "key")
	authIndex := readString(detail, "auth_index", "authIndex", "AuthIndex")
	rawJSON := legacyRawJSON(endpoint, model, detail)
	provider := readString(detail, "provider", "type", "auth_type", "authType")
	executorType := readString(detail, "executor_type", "executorType")
	providerSnapshot := readString(detail, "auth_provider_snapshot", "authProviderSnapshot")
	requestedModel := readString(detail, "requested_model", "requestedModel", "alias")
	resolvedModel := readString(detail, "resolved_model", "resolvedModel")
	accounting := NormalizeCacheAccounting(CacheInputContext{
		ExplicitMode:     cacheInputModeFromRecord(detail),
		ExecutorType:     executorType,
		Provider:         provider,
		ProviderSnapshot: providerSnapshot,
		ResolvedModel:    resolvedModel,
		RequestedModel:   requestedModel,
		DisplayModel:     model,
	}, inputTokens, cachedTokens, cacheTokens, cacheReadTokens, cacheCreationTokens)
	if totalTokens <= 0 {
		totalTokens = accounting.TotalInputTokens + maxInt64(outputTokens, 0) + maxInt64(reasoningTokens, 0)
	}
	requestID := readString(detail, "request_id", "requestId", "id")
	if requestID == "" {
		requestID = legacyRequestID(endpoint, model, normalizedTimestamp, rawJSON, endpointIndex, modelIndex, detailIndex)
	}

	event := Event{
		RequestID:                     requestID,
		TimestampMS:                   timestampMS,
		Timestamp:                     normalizedTimestamp,
		Provider:                      provider,
		ExecutorType:                  executorType,
		Model:                         model,
		RequestedModel:                requestedModel,
		ResolvedModel:                 resolvedModel,
		Endpoint:                      endpoint,
		Method:                        method,
		Path:                          path,
		AuthType:                      readString(detail, "auth_type", "authType"),
		AuthIndex:                     authIndex,
		Source:                        maskSource(sourceRaw),
		SourceHash:                    hashString(sourceRaw),
		APIKeyHash:                    hashString(apiKey),
		AccountSnapshot:               readString(detail, "account_snapshot", "accountSnapshot"),
		AuthLabelSnapshot:             readString(detail, "auth_label_snapshot", "authLabelSnapshot"),
		AuthFileSnapshot:              readString(detail, "auth_file_snapshot", "authFileSnapshot"),
		AuthProviderSnapshot:          providerSnapshot,
		AuthProjectIDSnapshot:         readString(detail, "auth_project_id_snapshot", "authProjectIdSnapshot"),
		AuthSnapshotAtMS:              readInt(detail, "auth_snapshot_at_ms", "authSnapshotAtMs"),
		ReasoningEffort:               readString(detail, "reasoning_effort", "reasoningEffort"),
		ServiceTier:                   readString(detail, "service_tier", "serviceTier"),
		RequestServiceTier:            readString(detail, "request_service_tier", "requestServiceTier"),
		ResponseServiceTier:           readString(detail, "response_service_tier", "responseServiceTier"),
		CacheInputMode:                accounting.Mode,
		InputTokens:                   inputTokens,
		OutputTokens:                  outputTokens,
		ReasoningTokens:               reasoningTokens,
		CachedTokens:                  cachedTokens,
		CacheTokens:                   cacheTokens,
		CacheReadTokens:               cacheReadTokens,
		CacheCreationTokens:           cacheCreationTokens,
		NormalizedUncachedInputTokens: accounting.UncachedInputTokens,
		NormalizedTotalInputTokens:    accounting.TotalInputTokens,
		NormalizedCacheReadTokens:     accounting.CacheReadTokens,
		NormalizedCacheCreationTokens: accounting.CacheCreationTokens,
		TotalTokens:                   totalTokens,
		LatencyMS:                     readOptionalInt(detail, "latency_ms", "latencyMs", "duration_ms", "durationMs", "elapsed_ms", "elapsedMs"),
		TTFTMS:                        readOptionalInt(detail, "ttft_ms", "ttftMs", "time_to_first_token_ms", "timeToFirstTokenMs"),
		Failed:                        readFailed(detail),
		FailStatusCode:                int(failStatusCode),
		FailSummary:                   failSummary,
		FailBody:                      failBody,
		RawJSON:                       rawJSON,
		CreatedAtMS:                   now,
	}
	event.ServiceTier = EffectiveServiceTier(CacheInputContext{
		ExecutorType:     event.ExecutorType,
		Provider:         event.Provider,
		ProviderSnapshot: event.AuthProviderSnapshot,
		AuthType:         event.AuthType,
	}, event.RequestServiceTier, event.ServiceTier, event.ResponseServiceTier)
	if event.Model == "" {
		event.Model = "-"
	}
	if event.Endpoint == "" {
		event.Endpoint = "-"
	}
	AttachResponseHeaderMetadata(&event, ResponseHeaderMetadataFromRecord(detail, time.UnixMilli(timestampMS)))
	event.EventHash = buildEventHash(event)
	return event, nil
}

func importCacheAccountingRawJSON(record map[string]any) string {
	existing := SafeRawJSON(readString(record, "raw_json", "rawJson"))
	recordHints := RawCacheAccountingHints{
		ExplicitMode: cacheInputModeFromRecord(record),
	}
	if total, ok := explicitPositiveTotalFromRecord(record); ok {
		recordHints.ExplicitTotal = total
		recordHints.HasExplicitTotal = true
	}
	existingHints := RawCacheAccountingHintsFromJSON(existing)
	modeCovered := recordHints.ExplicitMode == "" || existingHints.ExplicitMode == recordHints.ExplicitMode
	totalCovered := !recordHints.HasExplicitTotal || (existingHints.HasExplicitTotal && existingHints.ExplicitTotal == recordHints.ExplicitTotal)
	if modeCovered && totalCovered {
		return existing
	}
	provenance := map[string]any{}
	if recordHints.ExplicitMode != "" {
		provenance["cache_input_mode"] = recordHints.ExplicitMode
	}
	if recordHints.HasExplicitTotal {
		provenance["total_tokens"] = recordHints.ExplicitTotal
	}
	if existing != "" {
		provenance["raw_json"] = existing
	}
	raw, _ := json.Marshal(provenance)
	return string(raw)
}

func legacyRawJSON(endpoint string, model string, detail map[string]any) string {
	record := map[string]any{
		"format":   "legacy_usage_export",
		"endpoint": endpoint,
		"model":    model,
		"detail":   redactValue(detail),
	}
	raw, _ := json.Marshal(record)
	return string(raw)
}

func legacyRequestID(endpoint string, model string, timestamp string, rawJSON string, endpointIndex int, modelIndex int, detailIndex int) string {
	raw := strings.Join([]string{
		"legacy",
		strconv.Itoa(endpointIndex),
		strconv.Itoa(modelIndex),
		strconv.Itoa(detailIndex),
		endpoint,
		model,
		timestamp,
		rawJSON,
	}, "|")
	hash := hashString(raw)
	if len(hash) > 16 {
		hash = hash[:16]
	}
	return "legacy:" + hash
}

func parseEndpoint(endpoint string) (method string, path string) {
	if match := endpointPattern.FindStringSubmatch(endpoint); len(match) == 3 {
		return strings.ToUpper(match[1]), match[2]
	}
	return "", ""
}

func hasUsageAPIs(record map[string]any) bool {
	apis, ok := record["apis"].(map[string]any)
	return ok && len(apis) > 0
}

func sortedKeys(record map[string]any) []string {
	keys := make([]string, 0, len(record))
	for key := range record {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func looksLikeLegacyUsageSummary(record map[string]any) bool {
	_, hasTotal := record["total_requests"]
	_, hasSuccess := record["success_count"]
	_, hasFailure := record["failure_count"]
	return hasTotal || hasSuccess || hasFailure
}

func readBool(record map[string]any, keys ...string) bool {
	raw := first(record, keys...)
	switch value := raw.(type) {
	case bool:
		return value
	case json.Number:
		parsed, _ := value.Int64()
		return parsed != 0
	case float64:
		return value != 0
	case string:
		normalized := strings.ToLower(strings.TrimSpace(value))
		return normalized == "1" || normalized == "true" || normalized == "yes" || normalized == "on"
	default:
		return false
	}
}

func decodeJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("usage import payload contains multiple JSON values")
	}
	return nil
}
