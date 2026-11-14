package fakeprovider

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/pandokos/pandokos/test/fixtures"
)

// BigAnthropicEventBytes is the size of the event of ModelBigEvent on the Messages API: over
// the 1 MiB bound of the Anthropic stream reader.
const BigAnthropicEventBytes = 1<<20 + 1024

// anthropicAccepts reports whether the request presents an accepted key in X-Api-Key.
func (s *Server) anthropicAccepts(r *http.Request) bool {
	k := r.Header.Get("X-Api-Key")
	s.mu.Lock()
	defer s.mu.Unlock()
	return k != "" && s.accepted[k]
}

// handleAnthropic serves the Messages API operations.
func (s *Server) handleAnthropic(w http.ResponseWriter, r *http.Request, op string, body []byte) {
	h := w.Header()
	h.Set("Request-Id", "req_synthetic-"+strconv.FormatInt(s.nextReqID.Add(1), 10))
	if !s.anthropicAccepts(r) {
		writeJSON(w, http.StatusUnauthorized, fixtures.ReadAnthropic("error_401.json"))
		return
	}
	h.Set("Set-Cookie", "fake=1")
	h.Set("Server", "fake")
	h.Set("Via", "1.1 fake")
	h.Set("Anthropic-Ratelimit-Requests-Remaining", "99")
	h.Set("X-Pandokos-Error", "spoofed")

	var req struct {
		Model  string
		Stream bool
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"synthetic: invalid JSON"}}`))
		return
	}
	stream := req.Stream && op == pathMessages
	if op == pathMessages && r.Header.Get("Anthropic-Version") == "" {
		writeJSON(w, http.StatusBadRequest, []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"synthetic: anthropic-version header is required"}}`))
		return
	}
	if op == pathCountTokens {
		s.anthropicCount(w, r, req.Model)
		return
	}
	switch req.Model {
	case ModelBasic, ModelDetails:
		s.anthropicAnswer(w, r, stream, "message.json", "message_stream.sse", false)
	case ModelCache:
		s.anthropicAnswer(w, r, stream, "message_cache.json", "message_stream_cumulative.sse", false)
	case ModelGatedStream:
		s.anthropicAnswer(w, r, true, "message.json", "message_stream.sse", true)
	case ModelHang:
		select {
		case <-s.gate:
			s.anthropicAnswer(w, r, stream, "message.json", "message_stream.sse", false)
		case <-r.Context().Done():
			s.cancelled <- struct{}{}
		}
	case ModelError429:
		h.Set("Retry-After", "7")
		h.Set("X-Should-Retry", "true")
		writeJSON(w, http.StatusTooManyRequests, fixtures.ReadAnthropic("error_429.json"))
	case ModelError529:
		h.Set("X-Should-Retry", "true")
		writeJSON(w, 529, fixtures.ReadAnthropic("error_529.json"))
	case ModelError500Echo:
		msg := "upstream failure echo: " + fixtures.CanaryPrompt + " " + fixtures.CanaryCompletion
		b, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": msg}})
		writeJSON(w, http.StatusInternalServerError, b)
	case ModelRedirect:
		s.mu.Lock()
		location := s.redirect
		s.mu.Unlock()
		status := http.StatusTemporaryRedirect
		if v, err := strconv.Atoi(r.Header.Get("X-Fake-Status")); err == nil && v >= 300 && v < 400 {
			status = v
		}
		h.Set("Location", location)
		w.WriteHeader(status)
	case ModelEchoAuth:
		// A provider that quotes the credential it was given in its headers.
		h.Set("X-Echo-Api-Key", r.Header.Get("X-Api-Key"))
		h.Set("Www-Authenticate", r.Header.Get("X-Api-Key"))
		writeJSON(w, http.StatusOK, fixtures.ReadAnthropic("message.json"))
	case ModelClose:
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
	case ModelNoUsage, ModelStreamError, ModelKeepalive, ModelTools, ModelTinyWrites, ModelCut, ModelCRLF, ModelBigEvent:
		s.anthropicBehave(w, r, req.Model, stream)
	case ModelCutAfterStart, ModelStallAfterFirst:
		data := fixtures.ReadAnthropic("message.json")
		if stream {
			data = fixtures.ReadAnthropic("message_stream.sse")
		}
		s.firstThen(w, r, req.Model, data)
	default:
		writeJSON(w, http.StatusNotFound, []byte(`{"type":"error","error":{"type":"not_found_error","message":"synthetic: model: unknown"}}`))
	}
}

// anthropicCount answers messages/count_tokens: never a stream, no usage object.
func (s *Server) anthropicCount(w http.ResponseWriter, r *http.Request, model string) {
	switch model {
	case ModelError429:
		w.Header().Set("Retry-After", "7")
		writeJSON(w, http.StatusTooManyRequests, fixtures.ReadAnthropic("error_429.json"))
	case ModelError529:
		writeJSON(w, 529, fixtures.ReadAnthropic("error_529.json"))
	case ModelHang:
		select {
		case <-s.gate:
			writeJSON(w, http.StatusOK, fixtures.ReadAnthropic("count_tokens.json"))
		case <-r.Context().Done():
			s.cancelled <- struct{}{}
		}
	default:
		writeJSON(w, http.StatusOK, fixtures.ReadAnthropic("count_tokens.json"))
	}
}

// anthropicAnswer answers the usual JSON document or its stream.
func (s *Server) anthropicAnswer(w http.ResponseWriter, r *http.Request, stream bool, jsonName, streamName string, gated bool) {
	if !stream {
		writeJSON(w, http.StatusOK, fixtures.ReadAnthropic(jsonName))
		return
	}
	s.stream(w, r, fixtures.ReadAnthropic(streamName), gated)
}

// anthropicBehave serves the special behaviours of the 3.2 models for the Messages API.
func (s *Server) anthropicBehave(w http.ResponseWriter, r *http.Request, model string, stream bool) {
	data := fixtures.ReadAnthropic("message.json")
	if stream {
		data = fixtures.ReadAnthropic("message_stream.sse")
	}
	switch model {
	case ModelNoUsage:
		if stream {
			s.stream(w, r, dropUsageSSE(data), false)
			return
		}
		writeJSON(w, http.StatusOK, dropUsage(data))
	case ModelStreamError:
		if !stream {
			writeJSON(w, http.StatusInternalServerError, []byte(`{"type":"error","error":{"type":"api_error","message":"synthetic upstream failure"}}`))
			return
		}
		s.stream(w, r, fixtures.ReadAnthropic("message_stream_error.sse"), false)
	case ModelKeepalive:
		if !stream {
			writeJSON(w, http.StatusOK, data)
			return
		}
		var out []byte
		for _, ev := range splitEvents(data) {
			out = append(out, ": ping\n\n"...)
			out = append(out, ev...)
		}
		s.stream(w, r, out, false)
	case ModelTools:
		if !stream {
			writeJSON(w, http.StatusOK, data)
			return
		}
		s.stream(w, r, fixtures.ReadAnthropic("message_stream_tools.sse"), false)
	case ModelCRLF:
		if !stream {
			writeJSON(w, http.StatusOK, data)
			return
		}
		s.stream(w, r, fixtures.ReadAnthropic("message_stream_ping_crlf.sse"), false)
	case ModelTinyWrites:
		ct := "application/json"
		if stream {
			ct = "text/event-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		for i := range data {
			if _, err := w.Write(data[i : i+1]); err != nil {
				s.cancelled <- struct{}{}
				return
			}
			_ = rc.Flush()
		}
	case ModelCut:
		s.cutHalf(w, data, stream)
	case ModelBigEvent:
		if !stream {
			var doc map[string]any
			if json.Unmarshal(data, &doc) != nil {
				writeJSON(w, http.StatusOK, data)
				return
			}
			doc["pad"] = strings.Repeat("a", BigBodyBytes)
			b, _ := json.Marshal(doc)
			writeJSON(w, http.StatusOK, b)
			return
		}
		pad := strings.Repeat("a", BigAnthropicEventBytes)
		first := []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + pad + "\"}}\n\n")
		s.stream(w, r, append(first, data...), false)
	}
}

// cutHalf hijacks the connection, sends half of the answer and closes it.
func (s *Server) cutHalf(w http.ResponseWriter, data []byte, isStream bool) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	half := data[:len(data)/2]
	if isStream {
		_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"+strconv.FormatInt(int64(len(half)), 16)+"\r\n")
		_, _ = conn.Write(half)
		_, _ = io.WriteString(conn, "\r\n")
		return
	}
	_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: "+strconv.Itoa(len(data))+"\r\n\r\n")
	_, _ = conn.Write(half)
}

// base2 returns the usual chat or responses answer of an operation as one document or stream
// (the data fake-cut-after-start and fake-stall-after-first work on).
func base2(op string, stream bool) []byte {
	data, _ := base(op, stream)
	return data
}

// firstThen sends the first event of a stream and then closes the connection without ending the
// stream (ModelCutAfterStart) or waits for the cancellation of the request (ModelStallAfterFirst).
// A document that is not a stream is sent whole.
func (s *Server) firstThen(w http.ResponseWriter, r *http.Request, model string, data []byte) {
	if !bytes.Contains(data, []byte("data:")) {
		writeJSON(w, http.StatusOK, data)
		return
	}
	first := splitEvents(data)[0]
	if model == ModelStallAfterFirst {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(first)
		_ = http.NewResponseController(w).Flush()
		select {
		case <-r.Context().Done():
			s.cancelled <- struct{}{}
		case <-s.gate:
		}
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"+strconv.FormatInt(int64(len(first)), 16)+"\r\n")
	_, _ = conn.Write(first)
	_, _ = io.WriteString(conn, "\r\n")
}

// compat serves the compatible profiles: the chat wire differs, embeddings and responses are the
// usual ones.
func (s *Server) compat(w http.ResponseWriter, r *http.Request, op, model string, stream bool, body []byte) {
	if op != pathChat {
		s.answer(w, r, op, stream, false, false)
		return
	}
	var members map[string]json.RawMessage
	_ = json.Unmarshal(body, &members)
	_, hasOption := members["stream_options"]
	switch model {
	case ModelCompatUsageLastChunk:
		if stream {
			s.stream(w, r, fixtures.ReadCompat("chat_stream_usage_last_content_chunk.sse"), false)
			return
		}
		writeJSON(w, http.StatusOK, fixtures.Read("chat_completion.json"))
	case ModelCompatExtraUsage:
		if stream {
			s.stream(w, r, fixtures.ReadCompat("chat_stream_extra_usage.sse"), false)
			return
		}
		writeJSON(w, http.StatusOK, fixtures.ReadCompat("chat_completion_extra_usage.json"))
	case ModelCompatRefusesOption:
		if hasOption {
			writeJSON(w, http.StatusBadRequest, fixtures.ReadCompat("error_400_stream_options.json"))
			return
		}
		fallthrough
	case ModelCompatIgnoresOption:
		if stream {
			s.stream(w, r, fixtures.ReadCompat("chat_stream_no_usage.sse"), false)
			return
		}
		writeJSON(w, http.StatusOK, fixtures.Read("chat_completion.json"))
	}
}
