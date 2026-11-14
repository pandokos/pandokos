"""T-561: Pandokos errors are the SDKs' own exceptions; the provider's errors pass unchanged."""

import anthropic
import openai
import pytest

from conftest import (
    ANTHROPIC_BASE,
    BASE_URL,
    CANARY_PROMPT,
    anthropic_client,
    openai_client,
)

MESSAGES = [{"role": "user", "content": CANARY_PROMPT}]


def test_t561_openai_authentication_error():
    with pytest.raises(openai.AuthenticationError) as info:
        openai_client(api_key="synthetic-not-the-key").chat.completions.create(model="fake-basic", messages=MESSAGES)
    assert info.value.response.headers["x-pandokos-error"] == "unauthenticated"
    assert info.value.status_code == 401


def test_t561_openai_permission_denied(oai):
    with pytest.raises(openai.PermissionDeniedError) as info:
        oai.chat.completions.create(model="synthetic-no-such-model", messages=MESSAGES)
    assert info.value.response.headers["x-pandokos-error"] == "model_not_allowed"


def test_t561_openai_not_found():
    with pytest.raises(openai.NotFoundError) as info:
        openai_client(f"{BASE_URL}/nope/v1").chat.completions.create(model="fake-basic", messages=MESSAGES)
    assert info.value.response.headers["x-pandokos-error"] == "endpoint_not_supported"


def test_t561_openai_provider_rate_limit_is_not_a_pandokos_error(oai):
    with pytest.raises(openai.RateLimitError) as info:
        oai.chat.completions.create(model="fake-error-429", messages=MESSAGES)
    assert "x-pandokos-error" not in info.value.response.headers


def test_t561_anthropic_authentication_error():
    with pytest.raises(anthropic.AuthenticationError) as info:
        anthropic_client(api_key="synthetic-not-the-key").messages.create(model="fake-basic", max_tokens=16, messages=MESSAGES)
    assert info.value.response.headers["x-pandokos-error"] == "unauthenticated"
    assert info.value.body["error"]["type"] == "authentication_error"


def test_t561_anthropic_permission_denied(ant):
    with pytest.raises(anthropic.PermissionDeniedError) as info:
        ant.messages.create(model="synthetic-no-such-model", max_tokens=16, messages=MESSAGES)
    assert info.value.response.headers["x-pandokos-error"] == "model_not_allowed"
    assert info.value.body["error"]["type"] == "permission_error"


def test_t561_anthropic_not_found():
    with pytest.raises(anthropic.NotFoundError) as info:
        anthropic_client(f"{BASE_URL}/nope").messages.create(model="fake-basic", max_tokens=16, messages=MESSAGES)
    assert info.value.response.headers["x-pandokos-error"] == "endpoint_not_supported"
    assert info.value.body["error"]["type"] == "not_found_error"


def test_t561_anthropic_provider_rate_limit_is_not_a_pandokos_error(ant):
    with pytest.raises(anthropic.RateLimitError) as info:
        ant.messages.create(model="fake-error-429", max_tokens=16, messages=MESSAGES)
    assert "x-pandokos-error" not in info.value.response.headers
    assert info.value.body["error"]["type"] == "rate_limit_error"


def test_t561_anthropic_mid_stream_error_event_raises_the_streams_error(ant):
    with pytest.raises(anthropic.APIStatusError):
        with ant.messages.stream(model="fake-stream-error", max_tokens=16, messages=MESSAGES) as stream:
            for _ in stream:
                pass


def test_t561_openai_mid_stream_error_event_raises_the_streams_error(oai):
    with pytest.raises(openai.APIError):
        for _ in oai.chat.completions.create(model="fake-stream-error", messages=MESSAGES, stream=True):
            pass
