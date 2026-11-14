// Package genaiprices reads the open genai-prices dataset (format v2) and extracts the price
// versions of the models the catalogue names. It embeds a snapshot of the dataset, pinned to an
// upstream commit with its MIT licence (ADR 0011, ADR 0035, ADR 0036). It does no network or file
// I/O; the caller supplies the bytes.
package genaiprices
