"""T-559, T-560, T-565: the Anthropic SDK against the anthropic family."""

import anthropic
import pytest

from conftest import (
    ANTHROPIC_BASE,
    CANARY_COMPLETION,
    CANARY_PROMPT,
    KEY,
    anthropic_client,
    one_record,
    request_id_of,
)

MESSAGES = [{"role": "user", "content": CANARY_PROMPT}]


def test_t559_messages_plain(ant):
    raw = ant.messages.with_raw_response.create(model="fake-basic", max_tokens=16, messages=MESSAGES)
    message = raw.parse()
    assert message.content[0].text == CANARY_COMPLETION
    assert (message.usage.input_tokens, message.usage.output_tokens) == (12, 7)
    rec = one_record(request_id_of(raw.headers))
    assert (rec["provider"], rec["api_family"], rec["operation"], rec["outcome"]) == ("anthropic", "anthropic", "messages", "ok")
    assert (rec["input_source"], rec["output_source"]) == ("provider", "provider")
    assert (rec["input_tokens"], rec["output_tokens"], rec["stream"]) == (12, 7, False)


def test_t559_messages_stream_with_the_sdk_helper(ant):
    with ant.messages.stream(model="fake-basic", max_tokens=16, messages=MESSAGES) as stream:
        rid = request_id_of(stream.response.headers)
        text = "".join(stream.text_stream)
        final = stream.get_final_message()
    assert text == CANARY_COMPLETION
    assert (final.usage.input_tokens, final.usage.output_tokens) == (12, 7)
    rec = one_record(rid)
    assert rec["stream"] is True
    assert (rec["input_source"], rec["output_source"]) == ("provider", "provider")
    assert (rec["input_tokens"], rec["output_tokens"]) == (12, 7)
    assert rec["cost_status"] in ("known", "unknown", "none")


def test_t559_messages_stream_with_cache_usage_is_inclusive(ant):
    with ant.messages.stream(model="fake-cache", max_tokens=16, messages=MESSAGES) as stream:
        rid = request_id_of(stream.response.headers)
        final = stream.get_final_message()
    assert final.usage.cache_read_input_tokens == 2000 and final.usage.output_tokens == 50
    rec = one_record(rid)
    # The provider's input_tokens is 10 beside two cache counts: the record's input includes both.
    assert (rec["input_tokens"], rec["cache_read_tokens"], rec["cache_write_tokens"], rec["output_tokens"]) == (2310, 2000, 300, 50)
    assert rec["other_units"].get("cache_write_1h_tokens") == 200


def test_t560_count_tokens(ant):
    raw = ant.messages.with_raw_response.count_tokens(model="fake-basic", messages=MESSAGES)
    assert raw.parse().input_tokens == 42
    rec = one_record(request_id_of(raw.headers))
    assert rec["operation"] == "messages.count_tokens"
    assert (rec["input_source"], rec["output_source"], rec["cost_status"]) == ("none", "none", "none")
    assert (rec["input_tokens"], rec["output_tokens"]) == (0, 0)


def test_t565_bearer_api_key_authenticates():
    client = anthropic_client(api_key=KEY)
    raw = client.messages.with_raw_response.create(model="fake-basic", max_tokens=16, messages=MESSAGES)
    assert raw.parse().content[0].text == CANARY_COMPLETION


def test_t565_x_api_key_header_without_api_key_authenticates():
    client = anthropic.Anthropic(
        base_url=ANTHROPIC_BASE, api_key=None, auth_token=None, default_headers={"x-api-key": KEY}, max_retries=0, timeout=20
    )
    raw = client.messages.with_raw_response.create(model="fake-basic", max_tokens=16, messages=MESSAGES)
    assert raw.parse().content[0].text == CANARY_COMPLETION
    rec = one_record(request_id_of(raw.headers))
    assert rec["operation"] == "messages" and rec["outcome"] == "ok"


def test_t565_a_wrong_key_is_an_authentication_error():
    for client in (
        anthropic_client(api_key="synthetic-not-the-key"),
        anthropic.Anthropic(
            base_url=ANTHROPIC_BASE, api_key=None, auth_token=None, default_headers={"x-api-key": "synthetic-not-the-key"}, max_retries=0, timeout=20
        ),
    ):
        with pytest.raises(anthropic.AuthenticationError) as info:
            client.messages.create(model="fake-basic", max_tokens=16, messages=MESSAGES)
        assert info.value.response.headers["x-pandokos-error"] == "unauthenticated"
