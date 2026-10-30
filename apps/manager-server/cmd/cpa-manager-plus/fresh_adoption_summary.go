package main

import (
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
)

// adoptionSummaryUnavailable is the only code the read route answers with when
// there is no committed adoption to describe. It is a bounded code rather than a
// bare 404 so the browser can tell "this installation is not an adoption" from a
// lost route.
const adoptionSummaryUnavailable = "adopt_summary_unavailable"

// adoptionSummaryItem is one per-item migration result as the completion screen
// reads it.
//
// It is deliberately narrower than the durable `freshstore.AdoptionItem`: the
// record also carries the source and destination SHA-256 digests, and those are
// evidence for the Manager's own bookkeeping rather than something a browser
// needs. Dropping them here is what makes the reply a statement about progress
// only — it can never leak a credential byte, because the record never holds one
// and the digests are not emitted either.
type adoptionSummaryItem struct {
	Item        string `json:"item"`
	Kind        string `json:"kind"`
	Outcome     string `json:"outcome"`
	Reason      string `json:"reason,omitempty"`
	Instruction string `json:"instruction,omitempty"`
	// Retryable is the record's own rule, not a second opinion: an item failed
	// before anything was written, so only a replay can copy it.
	Retryable bool `json:"retryable"`
}

// adoptionSummaryCounts is the per-outcome tally the progress screen shows.
// `absent_at_source` is reported because it is a settled result the operator may
// want to see, and it is not a failure.
type adoptionSummaryCounts struct {
	Migrated       int `json:"migrated"`
	Manual         int `json:"manual"`
	Failed         int `json:"failed"`
	AbsentAtSource int `json:"absent_at_source"`
}

// adoptionSummaryReply is the read face of the Adopt migration: everything the
// completion screen needs to show progress and per-item results, and nothing
// else.
//
// `running` is the live single-flight flag rather than a recorded value, so a
// poll that arrives between two checkpoints still sees the truth: the copy is
// either in flight right now or it is not. `finished` and `planned` are the
// durable run markers, so a browser that reloads mid-run resumes from the same
// facts the completion path decides on.
type adoptionSummaryReply struct {
	Items     []adoptionSummaryItem `json:"items"`
	Planned   int                   `json:"planned"`
	Finished  bool                  `json:"finished"`
	Running   bool                  `json:"running"`
	Retryable int                   `json:"retryable"`
	Counts    adoptionSummaryCounts `json:"counts"`
}

// adoptionSummary serves `GET installation/adoption`.
//
// It exists because the per-item summary is durable but was only readable by the
// completion decision: the browser could ask whether the adoption finished, and
// nothing else. A migration that ends with retryable items, a reload in the
// middle of a run, and the "what did it actually copy" question all need the
// same recorded items, so the record itself is what this route answers with.
//
// The route is read-only and states only recorded facts plus the in-flight flag.
// A non-adoption, or an adoption that has not committed yet, has no summary to
// describe and is refused with its own code.
func (a *freshApplication) adoptionSummary(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	if op.Strategy.Strategy != bootstrap.AdoptExisting || op.Strategy.Stage != "committed" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		writeFreshJSON(w, map[string]string{"code": adoptionSummaryUnavailable})
		return
	}
	record, err := a.store.Load(r.Context())
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		writeFreshJSON(w, map[string]string{"code": adoptionSummaryUnavailable})
		return
	}
	reply := adoptionSummaryFrom(record.AdoptionSummary)
	reply.Running = a.adoptMigrationInFlight()
	writeFreshJSON(w, reply)
}

// adoptionSummaryFrom projects the durable summary into the reply. The order is
// the migration's own order, which is the plan order the run fixed before its
// first write.
func adoptionSummaryFrom(summary *freshstore.AdoptionSummary) adoptionSummaryReply {
	reply := adoptionSummaryReply{Items: []adoptionSummaryItem{}}
	if summary == nil {
		return reply
	}
	reply.Planned = summary.Planned
	reply.Finished = summary.Finished
	for _, item := range summary.Items {
		retryable := summary.Retryable(item.Item)
		if retryable {
			reply.Retryable++
		}
		switch item.Outcome {
		case freshstore.AdoptionMigrated:
			reply.Counts.Migrated++
		case freshstore.AdoptionManual:
			reply.Counts.Manual++
		case freshstore.AdoptionFailed:
			reply.Counts.Failed++
		case freshstore.AdoptionAbsentAtSource:
			reply.Counts.AbsentAtSource++
		}
		reply.Items = append(reply.Items, adoptionSummaryItem{
			Item:        item.Item,
			Kind:        string(item.Kind),
			Outcome:     string(item.Outcome),
			Reason:      item.Reason,
			Instruction: item.Instruction,
			Retryable:   retryable,
		})
	}
	return reply
}
