"""Fixtures of the SDK suite: clients built as the gateway guide says, and the control server.

The scripts hold no canary of their own: the harness passes the values in the environment.
"""

import json
import os
import time
import urllib.parse
import urllib.request

import anthropic
import openai
import pytest

BASE_URL = os.environ["PANDOKOS_BASE_URL"]
KEY = os.environ["PANDOKOS_KEY"]
CONTROL_URL = os.environ["PANDOKOS_CONTROL_URL"]
CANARY_PROMPT = os.environ["CANARY_PROMPT"]
CANARY_COMPLETION = os.environ["CANARY_COMPLETION"]

REQUEST_ID_HEADER = "x-pandokos-request-id"

OPENAI_BASE = f"{BASE_URL}/openai/v1"
ANTHROPIC_BASE = f"{BASE_URL}/anthropic"
COMPAT_BASE = f"{BASE_URL}/compat/v1"


def openai_client(base_url=OPENAI_BASE, api_key=KEY):
    return openai.OpenAI(base_url=base_url, api_key=api_key, max_retries=0, timeout=20)


def anthropic_client(base_url=ANTHROPIC_BASE, api_key=KEY, **kwargs):
    return anthropic.Anthropic(base_url=base_url, api_key=api_key, max_retries=0, timeout=20, **kwargs)


@pytest.fixture
def oai():
    return openai_client()


@pytest.fixture
def compat():
    return openai_client(COMPAT_BASE)


@pytest.fixture
def ant():
    return anthropic_client()


def _get(path):
    with urllib.request.urlopen(CONTROL_URL + path, timeout=20) as resp:
        return json.loads(resp.read())


def records(request_id, want=1):
    """The records of a request, polling the control server for at most 5 seconds."""
    deadline = time.monotonic() + 5
    while True:
        found = _get("/records?request_id=" + urllib.parse.quote(request_id))
        if len(found) >= want or time.monotonic() > deadline:
            return found
        time.sleep(0.02)


def one_record(request_id):
    found = records(request_id)
    assert len(found) == 1, f"{len(found)} records for {request_id}"
    return found[0]


def upstream_last():
    return _get("/upstream/last")


def upstream_cancelled(wait="5s"):
    return _get("/upstream/cancelled?wait=" + wait)["cancelled"]


def request_id_of(headers):
    value = headers.get(REQUEST_ID_HEADER)
    assert value, f"no {REQUEST_ID_HEADER} header"
    return value
