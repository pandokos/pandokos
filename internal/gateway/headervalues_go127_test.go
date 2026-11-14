//go:build go1.27

package gateway_test

import "net/http"

// allowManyHeaderValues lifts the limit on header values that net/http enforces from Go 1.27
// (Server.MaxHeaderValueCount, 500 by default). The tests send more than that on purpose to
// exercise the gateway's own bound on metadata headers; without this the server answers 431
// before the gateway runs, so no record is written.
func allowManyHeaderValues(s *http.Server) { s.MaxHeaderValueCount = 1 << 20 }
