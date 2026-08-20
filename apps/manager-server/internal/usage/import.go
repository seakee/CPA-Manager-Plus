package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	ImportFormatJSONL         = "usage_service_jsonl"
	ImportFormatLegacyExport  = "legacy_usage_export"
	ImportFormatLegacyPayload = "legacy_usage_payload"
	ArchiveSchemaVersion      = 1
	// MaxJSONLRecordBytes is the largest JSONL record accepted by the importer,
	// excluding its LF or CRLF line terminator.
	MaxJSONLRecordBytes = 10 * 1024 * 1024
	// maxCapturedJSONObjectBytes bounds the compatibility spool used only when
	// a caller supplies a non-seekable top-level JSON object. Resumable imports
	// use seekable files and are not constrained by this object-wide limit.
	maxCapturedJSONObjectBytes = 64 * 1024 * 1024
	maxLegacyShapeKeys         = 100_000
	maxLegacyShapeKeyBytes     = 8 * 1024 * 1024
)

var (
	ErrUnsupportedImportFormat  = errors.New("unsupported usage import format")
	ErrLegacyUsageNoDetails     = errors.New("legacy usage export does not contain request details")
	ErrJSONLRecordTooLarge      = errors.New("usage JSONL record exceeds the supported size")
	ErrImportObjectTooLarge     = errors.New("usage import JSON object exceeds the supported size")
	ErrLegacyShapeTooLarge      = errors.New("usage import legacy structure exceeds the supported size")
	ErrUnsupportedArchiveSchema = errors.New("unsupported usage archive schema version")
	ErrInvalidArchiveRecord     = errors.New("invalid usage archive record")
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
	batchSize  int
	batch      []Event
	batchBytes int64
	total      int
	consume    func([]Event) error
}

const boundedJSONDecoderReadBytes = 64 * 1024
const maxImportBatchRetainedBytes int64 = 32 * 1024 * 1024

type boundedJSONReader struct {
	reader    io.Reader
	remaining int64
}

func (r *boundedJSONReader) reset() {
	r.remaining = MaxJSONLRecordBytes + boundedJSONDecoderReadBytes
}

func (r *boundedJSONReader) Read(payload []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, jsonlRecordTooLargeError()
	}
	if len(payload) > boundedJSONDecoderReadBytes {
		payload = payload[:boundedJSONDecoderReadBytes]
	}
	if int64(len(payload)) > r.remaining {
		payload = payload[:int(r.remaining)]
	}
	written, err := r.reader.Read(payload)
	r.remaining -= int64(written)
	return written, err
}

type boundedJSONDecoder struct {
	*json.Decoder
	reader *boundedJSONReader
}

func newBoundedJSONDecoder(reader io.Reader) *boundedJSONDecoder {
	limited := &boundedJSONReader{reader: reader}
	limited.reset()
	decoder := json.NewDecoder(limited)
	decoder.UseNumber()
	return &boundedJSONDecoder{Decoder: decoder, reader: limited}
}

func (d *boundedJSONDecoder) resetLimit() {
	d.reader.reset()
}

func (d *boundedJSONDecoder) nextToken() (json.Token, error) {
	d.resetLimit()
	token, err := d.Token()
	d.resetLimit()
	return token, err
}

func (d *boundedJSONDecoder) decodeRaw() (json.RawMessage, error) {
	d.resetLimit()
	var raw json.RawMessage
	if err := d.Decode(&raw); err != nil {
		return nil, err
	}
	d.resetLimit()
	if len(raw) > MaxJSONLRecordBytes {
		return nil, jsonlRecordTooLargeError()
	}
	return raw, nil
}

func (d *boundedJSONDecoder) skipNextValue() error {
	_, err := d.decodeRaw()
	return err
}

func (d *boundedJSONDecoder) skipValueFromToken(token json.Token) error {
	err := skipJSONValueFromToken(d.Decoder, token)
	d.resetLimit()
	return err
}

func nextBoundedJSONObjectKey(decoder *boundedJSONDecoder) (string, error) {
	decoder.resetLimit()
	key, err := nextJSONObjectKey(decoder.Decoder)
	decoder.resetLimit()
	return key, err
}

func consumeBoundedJSONDelimiter(decoder *boundedJSONDecoder, expected json.Delim) error {
	err := consumeJSONDelimiter(decoder.Decoder, expected)
	decoder.resetLimit()
	return err
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

	buffered := bufio.NewReaderSize(reader, 64*1024)
	first, firstLinePrefixBytes, err := peekNonWhitespaceByte(buffered)
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
		result, err = streamJSONObjectOrJSONLImport(buffered, batcher, firstLinePrefixBytes)
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

type legacyStreamShapeLimits struct {
	maxKeys     int
	maxKeyBytes int64
}

type legacyStreamShapeBudget struct {
	limits   legacyStreamShapeLimits
	keys     int
	keyBytes int64
}

func (b *legacyStreamShapeBudget) add(key string) error {
	if b == nil {
		return nil
	}
	b.keys++
	b.keyBytes += int64(len(key))
	if (b.limits.maxKeys > 0 && b.keys > b.limits.maxKeys) ||
		(b.limits.maxKeyBytes > 0 && b.keyBytes > b.limits.maxKeyBytes) {
		return fmt.Errorf(
			"%w: keys=%d/%d key_bytes=%d/%d",
			ErrLegacyShapeTooLarge,
			b.keys,
			b.limits.maxKeys,
			b.keyBytes,
			b.limits.maxKeyBytes,
		)
	}
	return nil
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
	return inspectLegacyStreamShapeWithLimits(reader, legacyStreamShapeLimits{
		maxKeys:     maxLegacyShapeKeys,
		maxKeyBytes: maxLegacyShapeKeyBytes,
	})
}

func inspectLegacyStreamShapeWithLimits(reader io.Reader, limits legacyStreamShapeLimits) (legacyStreamShape, error) {
	decoder := newBoundedJSONDecoder(reader)
	budget := &legacyStreamShapeBudget{limits: limits}
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
		key, err := nextBoundedJSONObjectKey(decoder)
		if err != nil {
			return legacyStreamShape{}, err
		}
		switch key {
		case "usage":
			if usageSeen {
				return legacyStreamShape{}, duplicateLegacyStructuralKeyError("usage")
			}
			usageSeen = true
			usageEndpointKeys, usageModelKeys, usageFound, err = inspectLegacyUsageValue(decoder, budget)
			if err != nil {
				return legacyStreamShape{}, err
			}
		case "apis":
			if directAPIsSeen {
				return legacyStreamShape{}, duplicateLegacyStructuralKeyError("apis")
			}
			directAPIsSeen = true
			directEndpointKeys, directModelKeys, directAPIsFound, err = inspectLegacyAPIsValue(decoder, budget)
			if err != nil {
				return legacyStreamShape{}, err
			}
		case "event_hash":
			eventHashSeen = true
			eventHashNonEmpty, err = inspectExportedEventHashValue(decoder)
			if err != nil {
				return legacyStreamShape{}, err
			}
			if eventHashNonEmpty {
				return legacyStreamShape{}, nil
			}
		case "eventHash":
			camelEventHashSeen = true
			camelEventHashNonEmpty, err = inspectExportedEventHashValue(decoder)
			if err != nil {
				return legacyStreamShape{}, err
			}
			if camelEventHashNonEmpty {
				return legacyStreamShape{}, nil
			}
		default:
			if err := decoder.skipNextValue(); err != nil {
				return legacyStreamShape{}, err
			}
		}
	}
	if err := consumeJSONDelimiter(decoder.Decoder, '}'); err != nil {
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
		if err := ensureDecoderEOF(decoder.Decoder); err != nil {
			return legacyStreamShape{}, err
		}
	}
	return shape, nil
}

func inspectExportedEventHashValue(decoder *boundedJSONDecoder) (bool, error) {
	raw, err := decoder.decodeRaw()
	if err != nil {
		return false, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false, nil
	}
	if trimmed[0] != '"' {
		return true, nil
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return false, err
	}
	return strings.TrimSpace(value) != "", nil
}

func duplicateLegacyStructuralKeyError(key string) error {
	return fmt.Errorf("usage import legacy object contains duplicate structural key %q", key)
}

func inspectLegacyUsageValue(decoder *boundedJSONDecoder, budget *legacyStreamShapeBudget) ([]string, map[string][]string, bool, error) {
	token, err := decoder.nextToken()
	if err != nil {
		return nil, nil, false, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, nil, false, decoder.skipValueFromToken(token)
	}

	var endpointKeys []string
	var modelKeys map[string][]string
	found := false
	apisSeen := false
	for decoder.More() {
		key, err := nextBoundedJSONObjectKey(decoder)
		if err != nil {
			return nil, nil, false, err
		}
		if key != "apis" {
			if err := decoder.skipNextValue(); err != nil {
				return nil, nil, false, err
			}
			continue
		}
		if apisSeen {
			return nil, nil, false, duplicateLegacyStructuralKeyError("usage.apis")
		}
		apisSeen = true
		keys, models, ok, err := inspectLegacyAPIsValue(decoder, budget)
		if err != nil {
			return nil, nil, false, err
		}
		if ok {
			endpointKeys = keys
			modelKeys = models
			found = true
		}
	}
	if err := consumeBoundedJSONDelimiter(decoder, '}'); err != nil {
		return nil, nil, false, err
	}
	return endpointKeys, modelKeys, found, nil
}

func inspectLegacyAPIsValue(decoder *boundedJSONDecoder, budget *legacyStreamShapeBudget) ([]string, map[string][]string, bool, error) {
	token, err := decoder.nextToken()
	if err != nil {
		return nil, nil, false, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, nil, false, decoder.skipValueFromToken(token)
	}

	endpointSet := make(map[string]struct{})
	modelKeys := make(map[string][]string)
	for decoder.More() {
		endpoint, err := nextBoundedJSONObjectKey(decoder)
		if err != nil {
			return nil, nil, false, err
		}
		if err := budget.add(endpoint); err != nil {
			return nil, nil, false, err
		}
		if _, exists := endpointSet[endpoint]; exists {
			return nil, nil, false, duplicateLegacyStructuralKeyError("apis." + endpoint)
		}
		endpointSet[endpoint] = struct{}{}
		models, err := inspectLegacyEndpointValue(decoder, budget)
		if err != nil {
			return nil, nil, false, err
		}
		if models != nil {
			modelKeys[endpoint] = models
		}
	}
	if err := consumeBoundedJSONDelimiter(decoder, '}'); err != nil {
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

func inspectLegacyEndpointValue(decoder *boundedJSONDecoder, budget *legacyStreamShapeBudget) ([]string, error) {
	token, err := decoder.nextToken()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, decoder.skipValueFromToken(token)
	}

	var modelKeys []string
	modelsSeen := false
	for decoder.More() {
		key, err := nextBoundedJSONObjectKey(decoder)
		if err != nil {
			return nil, err
		}
		if key != "models" {
			if err := decoder.skipNextValue(); err != nil {
				return nil, err
			}
			continue
		}
		if modelsSeen {
			return nil, duplicateLegacyStructuralKeyError("models")
		}
		modelsSeen = true
		keys, err := inspectLegacyModelsValue(decoder, budget)
		if err != nil {
			return nil, err
		}
		modelKeys = keys
	}
	if err := consumeBoundedJSONDelimiter(decoder, '}'); err != nil {
		return nil, err
	}
	return modelKeys, nil
}

func inspectLegacyModelsValue(decoder *boundedJSONDecoder, budget *legacyStreamShapeBudget) ([]string, error) {
	token, err := decoder.nextToken()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, decoder.skipValueFromToken(token)
	}

	modelSet := make(map[string]struct{})
	for decoder.More() {
		model, err := nextBoundedJSONObjectKey(decoder)
		if err != nil {
			return nil, err
		}
		if err := budget.add(model); err != nil {
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
	if err := consumeBoundedJSONDelimiter(decoder, '}'); err != nil {
		return nil, err
	}
	modelKeys := make([]string, 0, len(modelSet))
	for model := range modelSet {
		modelKeys = append(modelKeys, model)
	}
	return modelKeys, nil
}

func inspectLegacyModelValue(decoder *boundedJSONDecoder) error {
	token, err := decoder.nextToken()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return decoder.skipValueFromToken(token)
	}

	detailsSeen := false
	for decoder.More() {
		key, err := nextBoundedJSONObjectKey(decoder)
		if err != nil {
			return err
		}
		if key == "details" {
			if detailsSeen {
				return duplicateLegacyStructuralKeyError("details")
			}
			detailsSeen = true
			if err := inspectLegacyDetailsValue(decoder); err != nil {
				return err
			}
			continue
		}
		if err := decoder.skipNextValue(); err != nil {
			return err
		}
	}
	return consumeBoundedJSONDelimiter(decoder, '}')
}

func inspectLegacyDetailsValue(decoder *boundedJSONDecoder) error {
	token, err := decoder.nextToken()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '[' {
		return decoder.skipValueFromToken(token)
	}
	for decoder.More() {
		if _, err := decoder.decodeRaw(); err != nil {
			return err
		}
	}
	return consumeBoundedJSONDelimiter(decoder, ']')
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
	decoder := newBoundedJSONDecoder(reader)
	nowMS := time.Now().UnixMilli()
	if err := expectJSONDelimiter(decoder.Decoder, '{'); err != nil {
		return result, err
	}
	decoder.resetLimit()
	processed := false
	for decoder.More() {
		key, err := nextBoundedJSONObjectKey(decoder)
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
		if err := decoder.skipNextValue(); err != nil {
			return result, err
		}
	}
	if err := consumeBoundedJSONDelimiter(decoder, '}'); err != nil {
		return result, err
	}
	if err := ensureDecoderEOF(decoder.Decoder); err != nil {
		return result, err
	}
	if !processed || batcher.total == 0 {
		return result, ErrLegacyUsageNoDetails
	}
	return result, batcher.flush()
}

func streamLegacyUsageValue(
	decoder *boundedJSONDecoder,
	shape legacyStreamShape,
	batcher *importBatcher,
	result *ImportStreamResult,
	nowMS int64,
) (bool, error) {
	token, err := decoder.nextToken()
	if err != nil {
		return false, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return false, decoder.skipValueFromToken(token)
	}

	processed := false
	for decoder.More() {
		key, err := nextBoundedJSONObjectKey(decoder)
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
		if err := decoder.skipNextValue(); err != nil {
			return false, err
		}
	}
	if err := consumeBoundedJSONDelimiter(decoder, '}'); err != nil {
		return false, err
	}
	return processed, nil
}

func streamLegacyAPIsValue(
	decoder *boundedJSONDecoder,
	shape legacyStreamShape,
	batcher *importBatcher,
	result *ImportStreamResult,
	nowMS int64,
) (bool, error) {
	token, err := decoder.nextToken()
	if err != nil {
		return false, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return false, decoder.skipValueFromToken(token)
	}

	for decoder.More() {
		endpoint, err := nextBoundedJSONObjectKey(decoder)
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
	if err := consumeBoundedJSONDelimiter(decoder, '}'); err != nil {
		return false, err
	}
	return true, nil
}

func streamLegacyEndpointValue(
	decoder *boundedJSONDecoder,
	endpoint string,
	endpointIndex int,
	modelRanks map[string]int,
	batcher *importBatcher,
	result *ImportStreamResult,
	nowMS int64,
) error {
	token, err := decoder.nextToken()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		result.Failed++
		return decoder.skipValueFromToken(token)
	}

	modelsFound := false
	for decoder.More() {
		key, err := nextBoundedJSONObjectKey(decoder)
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
		if err := decoder.skipNextValue(); err != nil {
			return err
		}
	}
	if err := consumeBoundedJSONDelimiter(decoder, '}'); err != nil {
		return err
	}
	if !modelsFound {
		result.Failed++
	}
	return nil
}

func streamLegacyModelsValue(
	decoder *boundedJSONDecoder,
	endpoint string,
	endpointIndex int,
	modelRanks map[string]int,
	batcher *importBatcher,
	result *ImportStreamResult,
	nowMS int64,
) (bool, error) {
	token, err := decoder.nextToken()
	if err != nil {
		return false, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return false, decoder.skipValueFromToken(token)
	}

	for decoder.More() {
		model, err := nextBoundedJSONObjectKey(decoder)
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
	if err := consumeBoundedJSONDelimiter(decoder, '}'); err != nil {
		return false, err
	}
	return true, nil
}

func streamLegacyModelValue(
	decoder *boundedJSONDecoder,
	endpoint string,
	model string,
	endpointIndex int,
	modelIndex int,
	batcher *importBatcher,
	result *ImportStreamResult,
	nowMS int64,
) error {
	token, err := decoder.nextToken()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		result.Failed++
		return decoder.skipValueFromToken(token)
	}

	detailsFound := false
	for decoder.More() {
		key, err := nextBoundedJSONObjectKey(decoder)
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
		if err := decoder.skipNextValue(); err != nil {
			return err
		}
	}
	if err := consumeBoundedJSONDelimiter(decoder, '}'); err != nil {
		return err
	}
	if !detailsFound {
		result.Unsupported++
	}
	return nil
}

func streamLegacyDetailsValue(
	decoder *boundedJSONDecoder,
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
	token, err := decoder.nextToken()
	if err != nil {
		return false, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '[' {
		return false, decoder.skipValueFromToken(token)
	}

	detailIndex := 0
	for decoder.More() {
		raw, err := decoder.decodeRaw()
		if err != nil {
			return false, err
		}
		var detail map[string]any
		if err := json.Unmarshal(raw, &detail); err != nil || detail == nil {
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
	if err := consumeBoundedJSONDelimiter(decoder, ']'); err != nil {
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
	decoder := newBoundedJSONDecoder(reader)
	token, err := decoder.Token()
	if err != nil {
		return result, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
		return result, ErrUnsupportedImportFormat
	}
	for decoder.More() {
		item, err := decoder.decodeRaw()
		if err != nil {
			return result, err
		}
		event, err := eventFromJSONRecord(item)
		if err != nil {
			if isFatalImportRecordError(err) {
				return result, err
			}
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
	if err := ensureDecoderEOF(decoder.Decoder); err != nil {
		return result, err
	}
	return result, batcher.flush()
}

func streamJSONObjectOrJSONLImport(
	reader *bufio.Reader,
	batcher *importBatcher,
	firstLinePrefixBytes int64,
) (returnResult ImportStreamResult, returnErr error) {
	first, err := captureJSONObject(reader)
	if err != nil {
		return ImportStreamResult{Format: ImportFormatJSONL}, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, first.Close())
	}()

	var parsed ImportParseResult
	firstRecordBytes := firstLinePrefixBytes + first.Size()
	if firstRecordBytes <= MaxJSONLRecordBytes {
		payload, readErr := first.Bytes()
		if readErr != nil {
			return ImportStreamResult{Format: ImportFormatJSONL}, readErr
		}
		parsed, err = parseJSONObjectImport(payload)
	} else {
		// A legacy export is a structured object whose details are streamed by
		// the legacy parser. Inspect it from the replayable temporary file so a
		// large legacy payload remains compatible without putting the whole
		// object in memory. Non-legacy top-level objects are still bounded by the
		// JSONL record limit.
		legacyReader, readerErr := first.Reader()
		if readerErr != nil {
			return ImportStreamResult{Format: ImportFormatJSONL}, readerErr
		}
		shape, inspectErr := inspectLegacyStreamShape(legacyReader)
		if inspectErr != nil {
			return ImportStreamResult{Format: ImportFormatJSONL}, inspectErr
		}
		if shape.format == "" {
			return ImportStreamResult{Format: ImportFormatJSONL}, jsonlRecordTooLargeError()
		}
		if _, seekErr := legacyReader.Seek(0, io.SeekStart); seekErr != nil {
			return ImportStreamResult{Format: shape.format}, seekErr
		}
		result, streamErr := streamLegacyObjectImport(legacyReader, shape, batcher)
		if streamErr != nil {
			return result, streamErr
		}
		if whitespaceErr := ensureOnlyWhitespace(reader); whitespaceErr != nil {
			return result, whitespaceErr
		}
		return result, nil
	}

	result := ImportStreamResult{
		Format:      parsed.Format,
		Failed:      parsed.Failed,
		Unsupported: parsed.Unsupported,
		Warnings:    parsed.Warnings,
	}
	if parsed.Format == ImportFormatLegacyExport || parsed.Format == ImportFormatLegacyPayload {
		if err != nil {
			return result, err
		}
		if whitespaceErr := ensureOnlyWhitespace(reader); whitespaceErr != nil {
			return result, whitespaceErr
		}
		for _, event := range parsed.Events {
			if addErr := batcher.add(event); addErr != nil {
				return result, addErr
			}
		}
		return result, batcher.flush()
	}
	if err != nil {
		return result, err
	}
	if tailErr := consumeJSONObjectJSONLRecordTail(reader, firstRecordBytes); tailErr != nil {
		return result, tailErr
	}
	for _, event := range parsed.Events {
		if addErr := batcher.add(event); addErr != nil {
			return result, addErr
		}
	}
	if streamErr := streamJSONLImport(reader, batcher, &result); streamErr != nil {
		return result, streamErr
	}
	return result, nil
}

const importCaptureMemoryLimit = MaxJSONLRecordBytes

type jsonObjectCaptureLimits struct {
	memoryBytes int64
	maxBytes    int64
	tempDir     string
}

type capturedJSONObject struct {
	buffer *bytes.Buffer
	file   *os.File
	writer *bufio.Writer
	size   int64
}

func captureJSONObject(reader *bufio.Reader) (*capturedJSONObject, error) {
	return captureJSONObjectWithLimits(reader, jsonObjectCaptureLimits{
		memoryBytes: importCaptureMemoryLimit,
		maxBytes:    maxCapturedJSONObjectBytes,
	})
}

func captureJSONObjectWithLimits(reader *bufio.Reader, limits jsonObjectCaptureLimits) (captured *capturedJSONObject, returnErr error) {
	if reader == nil || limits.memoryBytes <= 0 || limits.maxBytes < limits.memoryBytes {
		return nil, errors.New("usage import JSON object capture limits are invalid")
	}
	captured = &capturedJSONObject{
		buffer: bytes.NewBuffer(make([]byte, 0, minInt64(limits.memoryBytes, 64*1024))),
	}
	captureToClose := captured
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, captureToClose.Close())
			captured = nil
		}
	}()

	first, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	if first != '{' {
		return nil, ErrUnsupportedImportFormat
	}
	if err := captured.append([]byte{first}, limits); err != nil {
		return nil, err
	}
	depth := 1
	inString := false
	escaped := false
	for depth > 0 {
		chunk, readErr := reader.ReadSlice('}')
		if len(chunk) > 0 {
			if err := captured.append(chunk, limits); err != nil {
				return nil, err
			}
		}
		for _, value := range chunk {
			if inString {
				if escaped {
					escaped = false
				} else if value == '\\' {
					escaped = true
				} else if value == '"' {
					inString = false
				}
				continue
			}
			switch value {
			case '"':
				inString = true
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		if depth == 0 {
			break
		}
		switch {
		case readErr == nil:
			continue
		case errors.Is(readErr, bufio.ErrBufferFull):
			continue
		case errors.Is(readErr, io.EOF):
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, readErr
		}
	}
	if err := captured.rewind(); err != nil {
		return nil, err
	}
	return captured, nil
}

func (c *capturedJSONObject) append(payload []byte, limits jsonObjectCaptureLimits) error {
	if c == nil {
		return errors.New("captured JSON object is nil")
	}
	if int64(len(payload)) > limits.maxBytes-c.size {
		return fmt.Errorf("%w: maximum is %d bytes", ErrImportObjectTooLarge, limits.maxBytes)
	}
	if c.file == nil && c.size+int64(len(payload)) <= limits.memoryBytes {
		written, err := c.buffer.Write(payload)
		c.size += int64(written)
		if err != nil {
			return err
		}
		if written != len(payload) {
			return io.ErrShortWrite
		}
		return nil
	}
	if c.file == nil {
		file, err := os.CreateTemp(limits.tempDir, "cpamp-usage-import-*")
		if err != nil {
			return err
		}
		c.file = file
		c.writer = bufio.NewWriterSize(file, 64*1024)
		if err := file.Chmod(0o600); err != nil {
			return err
		}
		written, err := c.writer.Write(c.buffer.Bytes())
		if err != nil {
			return err
		}
		if written != c.buffer.Len() {
			return io.ErrShortWrite
		}
		c.buffer = nil
	}
	written, err := c.writer.Write(payload)
	c.size += int64(written)
	if err != nil {
		return err
	}
	if written != len(payload) {
		return io.ErrShortWrite
	}
	return nil
}

func (c *capturedJSONObject) rewind() error {
	if c == nil || c.file == nil {
		return nil
	}
	if c.writer != nil {
		if err := c.writer.Flush(); err != nil {
			return err
		}
	}
	_, err := c.file.Seek(0, io.SeekStart)
	return err
}

func (c *capturedJSONObject) Size() int64 {
	if c == nil {
		return 0
	}
	return c.size
}

func (c *capturedJSONObject) Bytes() ([]byte, error) {
	if c == nil || c.buffer == nil {
		return nil, errors.New("captured JSON object is not in memory")
	}
	return c.buffer.Bytes(), nil
}

func (c *capturedJSONObject) Reader() (io.ReadSeeker, error) {
	if c == nil {
		return nil, errors.New("captured JSON object is nil")
	}
	if c.file != nil {
		if err := c.rewind(); err != nil {
			return nil, err
		}
		return c.file, nil
	}
	return bytes.NewReader(c.buffer.Bytes()), nil
}

func (c *capturedJSONObject) Close() error {
	if c == nil {
		return nil
	}
	var cleanupErr error
	if c.writer != nil {
		cleanupErr = errors.Join(cleanupErr, c.writer.Flush())
		c.writer = nil
	}
	if c.file != nil {
		path := c.file.Name()
		cleanupErr = errors.Join(cleanupErr, c.file.Close())
		c.file = nil
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	c.buffer = nil
	return cleanupErr
}

func minInt64(left, right int64) int {
	if left < right {
		return int(left)
	}
	return int(right)
}

func streamJSONLImport(reader io.Reader, batcher *importBatcher, result *ImportStreamResult) error {
	scanner := newJSONLScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		event, err := eventFromJSONRecord([]byte(line))
		if err != nil {
			if isFatalImportRecordError(err) {
				return err
			}
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

func newJSONLScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	// The extra capacity lets the split function distinguish an exact-limit
	// record followed by CRLF from a record that is one byte too large.
	scanner.Buffer(make([]byte, 64*1024), MaxJSONLRecordBytes+3)
	scanner.Split(splitBoundedJSONLRecord)
	return scanner
}

func splitBoundedJSONLRecord(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if index := bytes.IndexByte(data, '\n'); index >= 0 {
		line := dropTrailingCarriageReturn(data[:index])
		if len(line) > MaxJSONLRecordBytes {
			return 0, nil, jsonlRecordTooLargeError()
		}
		return index + 1, line, nil
	}
	if atEOF {
		if len(data) == 0 {
			return 0, nil, nil
		}
		// A bare CR at EOF is record whitespace, not the CRLF terminator that
		// the size contract excludes.
		line := data
		if len(line) > MaxJSONLRecordBytes {
			return 0, nil, jsonlRecordTooLargeError()
		}
		return len(data), line, nil
	}
	if len(data) > MaxJSONLRecordBytes+1 ||
		(len(data) > MaxJSONLRecordBytes && data[len(data)-1] != '\r') {
		return 0, nil, jsonlRecordTooLargeError()
	}
	return 0, nil, nil
}

func dropTrailingCarriageReturn(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\r' {
		return data[:len(data)-1]
	}
	return data
}

func jsonlRecordTooLargeError() error {
	return fmt.Errorf("%w: maximum is %d bytes", ErrJSONLRecordTooLarge, MaxJSONLRecordBytes)
}

func consumeJSONObjectJSONLRecordTail(reader *bufio.Reader, recordBytes int64) error {
	pendingCarriageReturn := false
	for {
		value, err := reader.ReadByte()
		if errors.Is(err, io.EOF) {
			if pendingCarriageReturn {
				recordBytes++
				if recordBytes > MaxJSONLRecordBytes {
					return jsonlRecordTooLargeError()
				}
			}
			return nil
		}
		if err != nil {
			return err
		}
		if pendingCarriageReturn {
			if value == '\n' {
				return nil
			}
			recordBytes++
			if recordBytes > MaxJSONLRecordBytes {
				return jsonlRecordTooLargeError()
			}
			pendingCarriageReturn = false
		}
		switch value {
		case '\r':
			pendingCarriageReturn = true
		case '\n':
			return nil
		case ' ', '\t':
			recordBytes++
			if recordBytes > MaxJSONLRecordBytes {
				return jsonlRecordTooLargeError()
			}
		default:
			return errors.New("usage JSONL record contains trailing non-whitespace data")
		}
	}
}

func (b *importBatcher) add(event Event) error {
	eventBytes := retainedImportEventBytes(event)
	if len(b.batch) > 0 && eventBytes > maxImportBatchRetainedBytes-b.batchBytes {
		if err := b.flush(); err != nil {
			return err
		}
	}
	b.batch = append(b.batch, event)
	b.batchBytes += eventBytes
	b.total++
	if len(b.batch) < b.batchSize && b.batchBytes < maxImportBatchRetainedBytes {
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
	clear(b.batch)
	b.batch = b.batch[:0]
	b.batchBytes = 0
	return nil
}

func retainedImportEventBytes(event Event) int64 {
	retained := int64(512)
	for _, value := range []string{
		event.RequestID,
		event.EventHash,
		event.Timestamp,
		event.Provider,
		event.ExecutorType,
		event.Model,
		event.AnalyticsModel,
		event.RequestedModel,
		event.ResolvedModel,
		event.Endpoint,
		event.Method,
		event.Path,
		event.ClientIP,
		event.XForwardedFor,
		event.UserAgent,
		event.AuthType,
		event.AuthIndex,
		event.Source,
		event.SourceHash,
		event.APIKeyHash,
		event.AccountSnapshot,
		event.AuthLabelSnapshot,
		event.AuthFileSnapshot,
		event.AuthProviderSnapshot,
		event.AuthProjectIDSnapshot,
		event.ReasoningEffort,
		event.ServiceTier,
		event.RequestServiceTier,
		event.ResponseServiceTier,
		event.CacheInputMode,
		event.FailSummary,
		event.FailBody,
		event.ResponseMetadataJSON,
		event.HeaderQuotaPlanType,
		event.HeaderErrorKind,
		event.HeaderErrorCode,
		event.HeaderTraceID,
		event.RawJSON,
	} {
		retained += int64(len(value))
	}
	if event.ResponseMetadata != nil {
		// ResponseMetadataJSON contains the same nested strings serialized for
		// persistence, so count it again as a conservative estimate of the live
		// metadata object retained beside the serialized copy.
		retained += int64(len(event.ResponseMetadataJSON))
	}
	return retained
}

func peekNonWhitespaceByte(reader *bufio.Reader) (byte, int64, error) {
	var linePrefixBytes int64
	for {
		value, err := reader.ReadByte()
		if err != nil {
			return 0, 0, err
		}
		switch value {
		case '\n':
			linePrefixBytes = 0
			continue
		case ' ', '\t', '\r':
			linePrefixBytes++
			continue
		default:
			if err := reader.UnreadByte(); err != nil {
				return 0, 0, err
			}
			return value, linePrefixBytes, nil
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
		if err != nil && bytes.Contains(trimmed, []byte{'\n'}) &&
			!errors.Is(err, ErrLegacyUsageNoDetails) &&
			!isFatalImportRecordError(err) {
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
			if isFatalImportRecordError(err) {
				return result, err
			}
			result.Failed++
			continue
		}
		result.Events = append(result.Events, event)
	}
	return result, nil
}

func parseJSONLImport(data []byte) (ImportParseResult, error) {
	result := ImportParseResult{Format: ImportFormatJSONL}
	scanner := newJSONLScanner(bytes.NewReader(data))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		event, err := eventFromJSONRecord([]byte(line))
		if err != nil {
			if isFatalImportRecordError(err) {
				return result, err
			}
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
	archiveRecord, err := validateArchiveSchemaVersion(record)
	if err != nil {
		return Event{}, true, err
	}
	if archiveRecord {
		if err := validateArchiveRequiredFields(record); err != nil {
			return Event{}, true, err
		}
	}
	eventHash := readString(record, "event_hash", "eventHash")
	if archiveRecord {
		eventHash, _ = readArchiveString(record, "event_hash")
	}
	if eventHash == "" {
		if archiveRecord {
			return Event{}, true, fmt.Errorf("%w: event_hash is required", ErrInvalidArchiveRecord)
		}
		return Event{}, false, nil
	}

	timestampMS := readInt(record, "timestamp_ms", "timestampMs")
	timestamp := readString(record, "timestamp")
	if archiveRecord {
		timestampMS, _ = readArchiveInt(record, "timestamp_ms")
		timestamp, _ = readArchiveString(record, "timestamp")
	} else if timestampMS <= 0 || timestamp == "" {
		parsedMS, parsedTimestamp := readTimestamp(record)
		if timestampMS <= 0 {
			timestampMS = parsedMS
		}
		if timestamp == "" {
			timestamp = parsedTimestamp
		}
	}

	inputTokens, outputTokens, reasoningTokens, cachedTokens, cacheTokens, cacheReadTokens, cacheCreationTokens, totalTokens := readTokenFields(record)
	if archiveRecord {
		inputTokens, outputTokens, reasoningTokens, cachedTokens, cacheTokens, cacheReadTokens, cacheCreationTokens, totalTokens, err = archiveTokenFieldsFromRecord(record)
		if err != nil {
			return Event{}, true, err
		}
	}
	failStatusCode, failBody := readFailFields(record)
	failSummary := readString(record, "fail_summary", "failSummary")
	if archiveRecord {
		failStatusCode, err = readArchiveInt(record, "fail_status_code")
		if err != nil {
			return Event{}, true, err
		}
		failBody, err = readArchiveString(record, "fail_body")
		if err != nil {
			return Event{}, true, err
		}
		failSummary, err = readArchiveString(record, "fail_summary")
		if err != nil {
			return Event{}, true, err
		}
	} else if failSummary == "" {
		failSummary = FailSummaryFromBody(failBody)
	}

	requestedModel := readString(record, "requested_model", "requestedModel")
	resolvedModel := readString(record, "resolved_model", "resolvedModel")
	model := readString(record, "model")
	if archiveRecord {
		model, _ = readArchiveString(record, "model")
	} else {
		if model == "" {
			model = requestedModel
		}
		if model == "" {
			model = resolvedModel
		}
		if model == "" {
			model = "-"
		}
	}
	provider := readString(record, "provider")
	executorType := readString(record, "executor_type", "executorType")
	providerSnapshot := readString(record, "auth_provider_snapshot", "authProviderSnapshot")
	rawJSON := importCacheAccountingRawJSON(record)
	if archiveRecord {
		rawJSON, err = readArchiveString(record, "raw_json")
		if err != nil {
			return Event{}, true, err
		}
	}
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
	headerQuotaRecoverAtMS := readInt(record, "header_quota_recover_at_ms", "headerQuotaRecoverAtMs")
	headerQuotaUsedPercent := readOptionalFloat(record, "header_quota_used_percent", "headerQuotaUsedPercent")
	headerQuotaPlanType := readString(record, "header_quota_plan_type", "headerQuotaPlanType")
	headerErrorKind := readString(record, "header_error_kind", "headerErrorKind")
	headerErrorCode := readString(record, "header_error_code", "headerErrorCode")
	headerTraceID := readString(record, "header_trace_id", "headerTraceId")
	archiveResponseMetadataJSON := ""
	archiveResponseMetadataJSONPresent := false
	if archiveRecord {
		var archiveErr error
		accounting, totalTokens, headerQuotaRecoverAtMS, headerQuotaUsedPercent, archiveErr = archiveDerivedFieldsFromRecord(record)
		if archiveErr != nil {
			return Event{}, true, archiveErr
		}
		archiveResponseMetadataJSON, archiveResponseMetadataJSONPresent, archiveErr = responseMetadataJSONFromArchiveRecord(record)
		if archiveErr != nil {
			return Event{}, true, archiveErr
		}
	}
	if !archiveRecord {
		if totalTokens <= 0 && rawHints.HasExplicitTotal {
			totalTokens = rawHints.ExplicitTotal
		}
		if totalTokens <= 0 {
			totalTokens = accounting.TotalInputTokens + maxInt64(outputTokens, 0) + maxInt64(reasoningTokens, 0)
		}
	}

	event := Event{
		RequestID:                     readString(record, "request_id", "requestId"),
		EventHash:                     eventHash,
		TimestampMS:                   timestampMS,
		Timestamp:                     timestamp,
		Provider:                      provider,
		ExecutorType:                  executorType,
		Model:                         model,
		AnalyticsModel:                usageidentity.AnalyticsModelForRequest(model, requestedModel),
		RequestedModel:                requestedModel,
		ResolvedModel:                 resolvedModel,
		Endpoint:                      readString(record, "endpoint"),
		Method:                        readString(record, "method"),
		Path:                          readString(record, "path"),
		ClientIP:                      readString(record, "client_ip", "clientIp"),
		XForwardedFor:                 readString(record, "x_forwarded_for", "xForwardedFor"),
		UserAgent:                     readString(record, "user_agent", "userAgent"),
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
		PreserveArchiveDerivedFields:  archiveRecord,
		LatencyMS:                     readOptionalInt(record, "latency_ms", "latencyMs"),
		TTFTMS:                        readOptionalInt(record, "ttft_ms", "ttftMs", "time_to_first_token_ms", "timeToFirstTokenMs"),
		Failed:                        readBool(record, "failed", "is_failed", "isFailed"),
		FailStatusCode:                int(failStatusCode),
		FailSummary:                   failSummary,
		FailBody:                      failBody,
		HeaderQuotaRecoverAtMS:        headerQuotaRecoverAtMS,
		HeaderQuotaUsedPercent:        headerQuotaUsedPercent,
		HeaderQuotaPlanType:           headerQuotaPlanType,
		HeaderErrorKind:               headerErrorKind,
		HeaderErrorCode:               headerErrorCode,
		HeaderTraceID:                 headerTraceID,
		RawJSON:                       rawJSON,
		CreatedAtMS:                   readInt(record, "created_at_ms", "createdAtMs"),
	}
	if !archiveRecord {
		event.ServiceTier = EffectiveServiceTier(CacheInputContext{
			ExecutorType:     event.ExecutorType,
			Provider:         event.Provider,
			ProviderSnapshot: event.AuthProviderSnapshot,
			AuthType:         event.AuthType,
		}, event.RequestServiceTier, event.ServiceTier, event.ResponseServiceTier)
		if event.Endpoint == "" {
			event.Endpoint = "-"
		}
	}
	AttachResponseHeaderMetadata(&event, ResponseHeaderMetadataFromRecord(record, time.UnixMilli(timestampMS)))
	if archiveRecord {
		// The typed metadata remains available to readers, but persisted flattened
		// columns are part of the archive contract and may reflect semantics from a
		// newer CPAMP version. Do not let the current parser rewrite them.
		event.HeaderQuotaRecoverAtMS = headerQuotaRecoverAtMS
		event.HeaderQuotaUsedPercent = headerQuotaUsedPercent
		event.HeaderQuotaPlanType = headerQuotaPlanType
		event.HeaderErrorKind = headerErrorKind
		event.HeaderErrorCode = headerErrorCode
		event.HeaderTraceID = headerTraceID
		if archiveResponseMetadataJSONPresent {
			event.ResponseMetadataJSON = archiveResponseMetadataJSON
		}
	}
	if !archiveRecord && event.CreatedAtMS <= 0 {
		event.CreatedAtMS = time.Now().UnixMilli()
	}
	return event, true, nil
}

func validateArchiveSchemaVersion(record map[string]any) (bool, error) {
	raw, present := record["_cpamp_archive_schema_version"]
	if !present {
		return false, nil
	}
	number, ok := raw.(json.Number)
	if !ok {
		return true, fmt.Errorf("%w: expected integer", ErrUnsupportedArchiveSchema)
	}
	version, err := number.Int64()
	if err != nil || version != ArchiveSchemaVersion {
		return true, fmt.Errorf("%w: %s", ErrUnsupportedArchiveSchema, number.String())
	}
	return true, nil
}

func validateArchiveRequiredFields(record map[string]any) error {
	for _, field := range []struct {
		name     string
		positive bool
	}{
		{name: "_cpamp_archive_event_id", positive: true},
		{name: "timestamp_ms", positive: true},
		{name: "input_tokens"},
		{name: "output_tokens"},
		{name: "reasoning_tokens"},
		{name: "cached_tokens"},
		{name: "cache_tokens"},
		{name: "cache_read_tokens"},
		{name: "cache_creation_tokens"},
		{name: "failed"},
		{name: "fail_status_code"},
		{name: "created_at_ms"},
	} {
		value, err := readArchiveInt(record, field.name)
		if err != nil {
			return err
		}
		if field.positive && value <= 0 {
			return fmt.Errorf("%w: %s must be greater than zero", ErrInvalidArchiveRecord, field.name)
		}
		if field.name == "failed" && value != 0 && value != 1 {
			return fmt.Errorf("%w: failed must be 0 or 1", ErrInvalidArchiveRecord)
		}
	}
	for _, key := range []string{"event_hash", "timestamp", "model"} {
		raw, present := record[key]
		value, ok := raw.(string)
		if !present || !ok {
			return fmt.Errorf("%w: %s is required and must be a string", ErrInvalidArchiveRecord, key)
		}
		if key == "event_hash" && strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: event_hash is required", ErrInvalidArchiveRecord)
		}
	}
	return nil
}

func readArchiveString(record map[string]any, key string) (string, error) {
	raw, present := record[key]
	if !present {
		return "", fmt.Errorf("%w: %s is required", ErrInvalidArchiveRecord, key)
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%w: %s must be a string", ErrInvalidArchiveRecord, key)
	}
	return value, nil
}

func archiveTokenFieldsFromRecord(record map[string]any) (int64, int64, int64, int64, int64, int64, int64, int64, error) {
	values := make([]int64, 0, 8)
	for _, key := range []string{
		"input_tokens",
		"output_tokens",
		"reasoning_tokens",
		"cached_tokens",
		"cache_tokens",
		"cache_read_tokens",
		"cache_creation_tokens",
		"total_tokens",
	} {
		value, err := readArchiveInt(record, key)
		if err != nil {
			return 0, 0, 0, 0, 0, 0, 0, 0, err
		}
		values = append(values, value)
	}
	return values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7], nil
}

func archiveDerivedFieldsFromRecord(record map[string]any) (CacheAccounting, int64, int64, *float64, error) {
	rawMode, present := record["cache_input_mode"]
	mode, ok := rawMode.(string)
	mode = normalizeCacheInputMode(mode)
	if !present || !ok || (mode != CacheInputModeIncluded && mode != CacheInputModeSeparate) {
		return CacheAccounting{}, 0, 0, nil, fmt.Errorf("%w: cache_input_mode is required and must be supported", ErrInvalidArchiveRecord)
	}
	read := func(key string) (int64, error) {
		value, err := readArchiveInt(record, key)
		if err != nil {
			return 0, err
		}
		if value < 0 {
			return 0, fmt.Errorf("%w: %s must not be negative", ErrInvalidArchiveRecord, key)
		}
		return value, nil
	}
	uncached, err := read("normalized_uncached_input_tokens")
	if err != nil {
		return CacheAccounting{}, 0, 0, nil, err
	}
	totalInput, err := read("normalized_total_input_tokens")
	if err != nil {
		return CacheAccounting{}, 0, 0, nil, err
	}
	cacheRead, err := read("normalized_cache_read_tokens")
	if err != nil {
		return CacheAccounting{}, 0, 0, nil, err
	}
	cacheCreation, err := read("normalized_cache_creation_tokens")
	if err != nil {
		return CacheAccounting{}, 0, 0, nil, err
	}
	totalTokens, err := read("total_tokens")
	if err != nil {
		return CacheAccounting{}, 0, 0, nil, err
	}
	quotaRecoverAtMS, err := read("header_quota_recover_at_ms")
	if err != nil {
		return CacheAccounting{}, 0, 0, nil, err
	}
	quotaUsedPercent, err := readArchiveOptionalFloat(record, "header_quota_used_percent")
	if err != nil {
		return CacheAccounting{}, 0, 0, nil, err
	}
	return CacheAccounting{
		Mode:                mode,
		UncachedInputTokens: uncached,
		TotalInputTokens:    totalInput,
		CacheReadTokens:     cacheRead,
		CacheCreationTokens: cacheCreation,
	}, totalTokens, quotaRecoverAtMS, quotaUsedPercent, nil
}

func readArchiveInt(record map[string]any, key string) (int64, error) {
	raw, present := record[key]
	if !present {
		return 0, fmt.Errorf("%w: %s is required", ErrInvalidArchiveRecord, key)
	}
	number, ok := raw.(json.Number)
	if !ok {
		return 0, fmt.Errorf("%w: %s must be an integer", ErrInvalidArchiveRecord, key)
	}
	value, err := number.Int64()
	if err != nil {
		return 0, fmt.Errorf("%w: %s must be an integer", ErrInvalidArchiveRecord, key)
	}
	return value, nil
}

func readArchiveOptionalFloat(record map[string]any, key string) (*float64, error) {
	raw, present := record[key]
	if !present {
		// SQLite json_patch removes object members whose patch value is null, so
		// nullable archive fields are represented by either null or absence.
		return nil, nil
	}
	if raw == nil {
		return nil, nil
	}
	number, ok := raw.(json.Number)
	if !ok {
		return nil, fmt.Errorf("%w: %s must be numeric or null", ErrInvalidArchiveRecord, key)
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	if err != nil {
		return nil, fmt.Errorf("%w: %s must be numeric or null", ErrInvalidArchiveRecord, key)
	}
	return &value, nil
}

func responseMetadataJSONFromArchiveRecord(record map[string]any) (string, bool, error) {
	raw, present := record["response_metadata_json"]
	if !present {
		return "", false, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", true, fmt.Errorf("%w: response_metadata_json must be a string", ErrInvalidArchiveRecord)
	}
	if value != "" && !json.Valid([]byte(value)) {
		return "", true, fmt.Errorf("%w: response_metadata_json must contain valid JSON", ErrInvalidArchiveRecord)
	}
	return value, true, nil
}

func isFatalImportRecordError(err error) bool {
	return errors.Is(err, ErrUnsupportedArchiveSchema) || errors.Is(err, ErrInvalidArchiveRecord)
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
	displayModel := model
	if requestedModel != "" {
		displayModel = requestedModel
	}
	accounting := NormalizeCacheAccounting(CacheInputContext{
		ExplicitMode:     cacheInputModeFromRecord(detail),
		ExecutorType:     executorType,
		Provider:         provider,
		ProviderSnapshot: providerSnapshot,
		ResolvedModel:    resolvedModel,
		RequestedModel:   requestedModel,
		DisplayModel:     displayModel,
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
		Model:                         displayModel,
		AnalyticsModel:                usageidentity.AnalyticsModelForRequest(displayModel, requestedModel),
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
	// Compatible exports historically derive their synthetic event hash from
	// the aggregate model key. Keep that identity stable even when a newer
	// payload also carries the full requested model for audit/display purposes.
	hashEvent := event
	hashEvent.Model = model
	event.EventHash = buildEventHash(hashEvent)
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
