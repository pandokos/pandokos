//go:build !go1.27

package gateway_test

import "net/http"

// allowManyHeaderValues is a no-op before Go 1.27, whose net/http has no limit on header values.
func allowManyHeaderValues(*http.Server) {}
