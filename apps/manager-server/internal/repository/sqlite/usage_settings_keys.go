package sqlite

// Durable settings keys shared across Fresh bootstrap, settings persistence,
// and identity usage provenance tracking.
const (
	FreshUsageCollectionStartedAtMSKey = "fresh_usage_collection_started_at_ms"
	FreshGatewayAdmittedAtMSKey        = "fresh_gateway_admitted_at_ms"
	FreshUsageMonitoringDisabledGapKey = "fresh_usage_monitoring_disabled_gap"
	FreshUsageMonitoringDisableSeqKey  = "fresh_usage_monitoring_disable_seq"
	FreshUsageCompletionDisableSeqKey  = "fresh_usage_completion_disable_seq"
	FreshUsageUnreplayedGapKey         = "fresh_usage_unreplayed_gap"
	FreshUsageLastObservedAtMSKey      = "fresh_usage_last_observed_at_ms"
	FreshUsageLastTransportKey         = "fresh_usage_last_transport"
	FreshUsageLastSourceIDKey          = "fresh_usage_last_source_id"
	FreshUsageLastRetentionMSKey       = "fresh_usage_last_retention_ms"
)
