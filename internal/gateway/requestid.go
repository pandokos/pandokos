package gateway

import "github.com/pandokos/pandokos/internal/requestid"

// NewRequestID returns a request ID: 16 random bytes in lower-case hexadecimal.
func NewRequestID() string { return requestid.New() }
