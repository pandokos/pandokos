package openai

import (
	"encoding/json"
	"net/http"

	"github.com/pandokos/pandokos/internal/family"
)

type errorBody struct {
	Error struct {
		Message string  `json:"message"`
		Type    string  `json:"type"`
		Param   *string `json:"param"`
		Code    string  `json:"code"`
	} `json:"error"`
}

// WriteError writes a Pandokos error in the OpenAI error shape. It sets Content-Type,
// X-Pandokos-Error and the status of the code. The request ID header is set by the gateway.
func WriteError(w http.ResponseWriter, code family.ErrorCode) {
	var b errorBody
	b.Error.Message = code.Message()
	b.Error.Type = "pandokos_error"
	b.Error.Code = string(code)
	body, err := json.Marshal(b)
	if err != nil { // cannot happen: the body holds constants
		body = []byte(`{"error":{"message":"Pandokos: internal error.","type":"pandokos_error","param":null,"code":"internal"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(family.HeaderError, string(code))
	w.WriteHeader(code.Status())
	_, _ = w.Write(body)
}
