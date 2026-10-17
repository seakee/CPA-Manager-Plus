package protocol

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
)

const destinationPath = "/v1/runtime/operations/stage-destination"

type DestinationExecutor interface {
	StageDestination(context.Context, lifecycle.DestinationRequest) (journal.Operation, error)
}

func (h *handler) submitDestination(w http.ResponseWriter, r *http.Request) {
	var request lifecycle.DestinationRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	opening, err := d.Token()
	if err != nil || opening != json.Delim('{') {
		writeError(w, 400, "invalid_request", "invalid destination request")
		return
	}
	fields := map[string]any{"operationId": &request.OperationID, "expectedRuntimeIdentity": &request.ExpectedRuntimeIdentity, "expectedRuntimeGeneration": &request.ExpectedRuntimeGeneration, "expectedActiveArtifactId": &request.ExpectedActiveArtifactID, "sourceIdentity": &request.SourceIdentity, "sourceClass": &request.SourceClass, "apiKeys": &request.APIKeys, "managementKey": &request.ManagementKey}
	seen := map[string]bool{}
	for d.More() {
		token, e := d.Token()
		name, ok := token.(string)
		if e != nil || !ok || fields[name] == nil || seen[name] || d.Decode(fields[name]) != nil {
			writeError(w, 400, "invalid_request", "invalid destination request")
			return
		}
		seen[name] = true
	}
	closing, err := d.Token()
	var trailing any
	if err != nil || closing != json.Delim('}') || d.Decode(&trailing) != io.EOF || request.Validate() != nil {
		writeError(w, 400, "invalid_request", "invalid destination request")
		return
	}
	if h.destination == nil {
		writeError(w, 400, "unsupported_operation", "destination staging is not configured")
		return
	}
	op, err := h.destination.StageDestination(r.Context(), request)
	if err != nil {
		writeMutationError(w, err, "stage-destination")
		return
	}
	writeOperationResponse(w, op, "stage-destination")
}
