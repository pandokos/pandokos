package telemetry

// The metric SDK and its OTLP/HTTP exporter are used by the runtime of the next steps of
// sprint 6.1; this file keeps them in the module graph until then and is removed with the first
// of them.
import (
	_ "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	_ "go.opentelemetry.io/otel/sdk/metric"
)
