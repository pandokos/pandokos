"""T-556, T-557, T-558, T-564: the OpenAI SDK against the openai family and a compatible provider."""

import pytest

from conftest import (
    CANARY_COMPLETION,
    CANARY_PROMPT,
    one_record,
    request_id_of,
    upstream_last,
)

MESSAGES = [{"role": "user", "content": CANARY_PROMPT}]


def test_t556_chat_plain(oai):
    raw = oai.chat.completions.with_raw_response.create(model="fake-basic", messages=MESSAGES)
    completion = raw.parse()
    assert completion.choices[0].message.content == CANARY_COMPLETION
    assert (completion.usage.prompt_tokens, completion.usage.completion_tokens) == (9, 12)
    rec = one_record(request_id_of(raw.headers))
    assert (rec["provider"], rec["api_family"], rec["operation"], rec["outcome"]) == ("openai", "openai", "chat.completions", "ok")
    assert (rec["input_source"], rec["output_source"]) == ("provider", "provider")
    assert (rec["input_tokens"], rec["output_tokens"]) == (9, 12)
    assert rec["stream"] is False


def test_t556_chat_stream_without_include_usage_sees_no_usage_chunk(oai):
    with oai.chat.completions.with_streaming_response.create(model="fake-basic", messages=MESSAGES, stream=True) as resp:
        rid = request_id_of(resp.headers)
        chunks = list(resp.parse())
    assert chunks, "no chunk"
    assert all(len(c.choices) == 1 for c in chunks), "a usage-only chunk reached the caller"
    assert all(c.usage is None for c in chunks)
    text = "".join(c.choices[0].delta.content or "" for c in chunks)
    assert text == CANARY_COMPLETION + " end"
    rec = one_record(rid)
    assert rec["stream"] is True
    assert (rec["input_source"], rec["output_source"]) == ("provider", "provider")
    assert (rec["input_tokens"], rec["output_tokens"]) == (9, 12)


def test_t556_chat_stream_with_include_usage_sees_usage(oai):
    with oai.chat.completions.with_streaming_response.create(
        model="fake-basic", messages=MESSAGES, stream=True, stream_options={"include_usage": True}
    ) as resp:
        rid = request_id_of(resp.headers)
        chunks = list(resp.parse())
    last = chunks[-1]
    assert last.choices == [] and last.usage is not None
    assert (last.usage.prompt_tokens, last.usage.completion_tokens, last.usage.total_tokens) == (9, 12, 21)
    rec = one_record(rid)
    assert (rec["input_source"], rec["output_source"], rec["input_tokens"], rec["output_tokens"]) == ("provider", "provider", 9, 12)


def test_t557_responses_plain(oai):
    raw = oai.responses.with_raw_response.create(model="fake-basic", input=CANARY_PROMPT)
    response = raw.parse()
    assert response.output_text == CANARY_COMPLETION + " end"
    assert (response.usage.input_tokens, response.usage.output_tokens) == (20, 15)
    rec = one_record(request_id_of(raw.headers))
    assert (rec["operation"], rec["input_source"], rec["output_source"]) == ("responses", "provider", "provider")
    assert (rec["input_tokens"], rec["output_tokens"], rec["cache_read_tokens"]) == (20, 15, 5)


def test_t557_responses_stream(oai):
    with oai.responses.with_streaming_response.create(model="fake-basic", input=CANARY_PROMPT, stream=True) as resp:
        rid = request_id_of(resp.headers)
        text, completed = [], None
        for event in resp.parse():
            if event.type == "response.output_text.delta":
                text.append(event.delta)
            elif event.type == "response.completed":
                completed = event.response
    assert "".join(text) == CANARY_COMPLETION + " end"
    assert completed is not None and completed.usage.input_tokens == 20
    rec = one_record(rid)
    assert rec["operation"] == "responses" and rec["stream"] is True
    assert (rec["input_tokens"], rec["output_tokens"]) == (20, 15)


def test_t558_embeddings(oai):
    raw = oai.embeddings.with_raw_response.create(model="fake-basic", input=CANARY_PROMPT, encoding_format="float")
    result = raw.parse()
    assert len(result.data) == 1 and len(result.data[0].embedding) == 3
    assert result.usage.prompt_tokens == 8
    rec = one_record(request_id_of(raw.headers))
    assert (rec["operation"], rec["input_source"], rec["input_tokens"], rec["output_tokens"]) == ("embeddings", "provider", 8, 0)


@pytest.mark.parametrize("model", ["fake-basic", "compat-refuses-option", "compat-usage-last-chunk"])
def test_t564_compatible_chat_stream(compat, model):
    with compat.chat.completions.with_streaming_response.create(model=model, messages=MESSAGES, stream=True) as resp:
        rid = request_id_of(resp.headers)
        chunks = list(resp.parse())
    assert chunks
    last = upstream_last()
    assert last["path"] == "/compat-base/v1/chat/completions"
    assert last["body_has_stream_options"] is False, "the option was spliced into a provider set to omit"
    assert last["model"] == model
    rec = one_record(rid)
    assert (rec["provider"], rec["api_family"], rec["operation"], rec["outcome"]) == ("compat", "openai", "chat.completions", "ok")


def test_t564_compatible_usage_sources(compat):
    with compat.chat.completions.with_streaming_response.create(model="compat-ignores-option", messages=MESSAGES, stream=True) as resp:
        rid = request_id_of(resp.headers)
        list(resp.parse())
    rec = one_record(rid)
    assert (rec["input_source"], rec["output_source"]) == ("estimated", "estimated")
    assert rec["estimated_output_tokens"] >= 1
    with compat.chat.completions.with_streaming_response.create(model="compat-usage-last-chunk", messages=MESSAGES, stream=True) as resp:
        rid = request_id_of(resp.headers)
        list(resp.parse())
    rec = one_record(rid)
    assert (rec["input_source"], rec["input_tokens"], rec["output_tokens"]) == ("provider", 9, 12)


def test_t564_compatible_embeddings(compat):
    raw = compat.embeddings.with_raw_response.create(model="fake-basic", input=CANARY_PROMPT, encoding_format="float")
    assert len(raw.parse().data[0].embedding) == 3
    last = upstream_last()
    assert last["path"] == "/compat-base/v1/embeddings"
    rec = one_record(request_id_of(raw.headers))
    assert (rec["provider"], rec["api_family"], rec["operation"]) == ("compat", "openai", "embeddings")
