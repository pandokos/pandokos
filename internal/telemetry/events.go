package telemetry

import "time"

// The attribute keys of the event instruments (M52 and M56) and their closed values (sprint 6.4,
// section 5.15).
const (
	// AttrDeliveryOutcome is how a delivery request ended (M52).
	AttrDeliveryOutcome = "pandokos.delivery.outcome"
	// AttrDestinationState is the state of the enabled and disabled destinations (M56).
	AttrDestinationState = "pandokos.destination.state"
)

// The values of AttrDestinationState: failing is a subset of enabled.
const (
	DestinationEnabled  = "enabled"
	DestinationFailing  = "failing"
	DestinationDisabled = "disabled"
)

// The two kinds that retention and the exports gain: M44 counts the events and the limit events it
// removes, M46 the exports of events.
const (
	RetentionOutboxEvent = "outbox_event"
	RetentionLimitEvent  = "limit_event"
	ExportEvent          = "event"
	ExportPlatformEvent  = "platform_event"
)

// DeliveryOutcomes are the values of AttrDeliveryOutcome, in the order of the metrics reference.
var DeliveryOutcomes = []string{"delivered", "status", "redirect", "timeout", "connection", "tls", "blocked"}

// DestinationStates are the values of AttrDestinationState.
var DestinationStates = []string{DestinationEnabled, DestinationFailing, DestinationDisabled}

// EventsStats is the relay of events (M52 and M54 to M58). Nothing in it names a tenant, a
// destination or a URL. The duration of a request (M53) is recorded as it happens, with
// ObserveEventDelivery. This is the stub of the tests of part B of the plan; step C10 writes the
// instruments.
type EventsStats struct {
	Deliveries   map[string]int64 // requests by outcome, one of DeliveryOutcomes (M52)
	DeadLetters  int64            // events parked (M54)
	Expired      int64            // events removed by retention before an enabled destination passed them (M55)
	Destinations map[string]int64 // destinations by state, one of DestinationStates (M56)
	Lag          time.Duration    // the age of the oldest event an enabled destination has not settled; 0 when none (M57)
	Passes       map[string]int64 // passes of the relay by result: ok, failed (M58)
}

// ObserveEventDelivery records the time of one delivery request (M53) with its result: ok for a
// 2xx answer, failed for anything else.
func (m *Metrics) ObserveEventDelivery(result string, d time.Duration) {
	_, _ = result, d
}
