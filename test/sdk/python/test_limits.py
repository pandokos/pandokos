"""T-689: Pandokos limit refusals are the SDKs' own rate-limit errors, for rate_limited and for quota_exceeded."""

import os

import anthropic
import openai
import pytest

from conftest import ANTHROPIC_BASE, CANARY_PROMPT, OPENAI_BASE, anthropic_client, openai_client

MESSAGES = [{"role": "user", "content": CANARY_PROMPT}]

CASES = [
    ("rate", "rate_limited"),
    ("quota", "quota_exceeded"),
]


def assert_refusal(error, code):
    assert error.status_code == 429
    headers = error.response.headers
    assert headers["x-pandokos-error"] == code
    assert headers["retry-after"].isdigit() and int(headers["retry-after"]) >= 1
    assert headers["x-pandokos-limit-metric"]


@pytest.mark.parametrize("kind,code", CASES)
def test_t689_openai_raises_rate_limit_error(kind, code):
    client = openai_client(OPENAI_BASE, os.environ[f"PANDOKOS_KEY_{kind.upper()}_OPENAI"])
    client.chat.completions.create(model="fake-basic", messages=MESSAGES)  # the first call is admitted
    with pytest.raises(openai.RateLimitError) as info:
        client.chat.completions.create(model="fake-basic", messages=MESSAGES)
    assert_refusal(info.value, code)
    assert info.value.code == code


@pytest.mark.parametrize("kind,code", CASES)
def test_t689_anthropic_raises_rate_limit_error(kind, code):
    client = anthropic_client(ANTHROPIC_BASE, os.environ[f"PANDOKOS_KEY_{kind.upper()}_ANTHROPIC"])
    client.messages.create(model="fake-basic", max_tokens=16, messages=MESSAGES)
    with pytest.raises(anthropic.RateLimitError) as info:
        client.messages.create(model="fake-basic", max_tokens=16, messages=MESSAGES)
    assert_refusal(info.value, code)
    assert info.value.body["error"]["type"] == "rate_limit_error"


def test_t689_a_quota_names_the_period_metric():
    client = openai_client(OPENAI_BASE, os.environ["PANDOKOS_KEY_QUOTA_OPENAI"])
    with pytest.raises(openai.RateLimitError) as info:
        client.chat.completions.create(model="fake-basic", messages=MESSAGES)
    assert info.value.response.headers["x-pandokos-limit-metric"] == "requests_per_day"
    assert info.value.response.headers["x-pandokos-limit-scope"] == "all"
