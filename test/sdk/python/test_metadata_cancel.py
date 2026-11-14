"""T-562, T-563: metadata and trace headers; cancellation."""

import pytest

from conftest import (
    CANARY_PROMPT,
    one_record,
    request_id_of,
    upstream_cancelled,
    upstream_last,
)

MESSAGES = [{"role": "user", "content": CANARY_PROMPT}]
TRACE = "4bf92f3577b34da6a3ce929d0e0e4736"
TRACEPARENT = f"00-{TRACE}-00f067aa0ba902b7-01"
HEADERS = {
    "X-Pandokos-Metadata-Team": "alpha",
    "X-Pandokos-Metadata-Job": "nightly-7",
    "X-Pandokos-Metadata--bad": "dropped",
    "Traceparent": TRACEPARENT,
}


def _no_pandokos_header_upstream():
    names = [n.lower() for n in upstream_last()["header_names"]]
    assert not [n for n in names if n.startswith("x-pandokos")], names
    assert "traceparent" not in names and "tracestate" not in names, names


def test_t562_openai_metadata_and_trace(oai):
    raw = oai.chat.completions.with_raw_response.create(model="fake-basic", messages=MESSAGES, extra_headers=HEADERS)
    assert raw.parse().choices
    assert raw.headers["x-pandokos-metadata-dropped"] == "1"
    rec = one_record(request_id_of(raw.headers))
    assert rec["metadata"] == {"team": "alpha", "job": "nightly-7"}
    assert rec["metadata_dropped"] == 1
    assert rec["trace_id"] == TRACE
    _no_pandokos_header_upstream()


def test_t562_anthropic_metadata_and_trace(ant):
    raw = ant.messages.with_raw_response.create(model="fake-basic", max_tokens=16, messages=MESSAGES, extra_headers=HEADERS)
    assert raw.parse().content
    assert raw.headers["x-pandokos-metadata-dropped"] == "1"
    rec = one_record(request_id_of(raw.headers))
    assert rec["metadata"] == {"team": "alpha", "job": "nightly-7"}
    assert rec["metadata_dropped"] == 1
    assert rec["trace_id"] == TRACE
    _no_pandokos_header_upstream()


def test_t562_no_dropped_header_when_nothing_is_dropped(ant):
    raw = ant.messages.with_raw_response.create(
        model="fake-basic", max_tokens=16, messages=MESSAGES, extra_headers={"X-Pandokos-Metadata-Team": "alpha"}
    )
    assert "x-pandokos-metadata-dropped" not in raw.headers
    assert one_record(request_id_of(raw.headers))["metadata"] == {"team": "alpha"}


def test_t563_openai_stream_closed_after_the_first_event_is_cancelled(oai):
    with oai.chat.completions.with_streaming_response.create(model="fake-stall-after-first", messages=MESSAGES, stream=True) as resp:
        rid = request_id_of(resp.headers)
        stream = resp.parse()
        first = next(iter(stream))
        assert first is not None
        stream.close()
    assert upstream_cancelled("5s") is True
    rec = one_record(rid)
    assert rec["outcome"] == "cancelled"


def test_t563_anthropic_stream_closed_after_the_first_event_is_cancelled(ant):
    with ant.messages.stream(model="fake-stall-after-first", max_tokens=16, messages=MESSAGES) as stream:
        rid = request_id_of(stream.response.headers)
        first = next(iter(stream))
        assert first.type == "message_start"
    assert upstream_cancelled("5s") is True
    rec = one_record(rid)
    assert rec["outcome"] == "cancelled"
    assert rec["input_source"] == "provider" and rec["input_tokens"] == 12
    assert rec["output_source"] == "estimated"
