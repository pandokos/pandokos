"""T-566: each SDK is built with exactly the base URL the gateway guide gives for it.

The guide's table (docs/operations/gateway.md) has a column "Base URL"; the path of each cell is
appended to the gateway's address, with a provider other than openai and anthropic (the example
provider of the guide, or "<provider>") read as "compat", the compatible provider of the suite.
"""

import pathlib
import re
import urllib.parse

import pytest

from conftest import (
    ANTHROPIC_BASE,
    BASE_URL,
    CANARY_COMPLETION,
    CANARY_PROMPT,
    COMPAT_BASE,
    OPENAI_BASE,
    anthropic_client,
    openai_client,
)

GUIDE = pathlib.Path(__file__).resolve().parents[3] / "docs" / "operations" / "gateway.md"


def compat_name(path):
    """The path with its provider segment read as the suite's compatible provider."""
    first, _, rest = path.lstrip("/").partition("/")
    if first not in ("openai", "anthropic"):
        first = "compat"
    return "/" + first + ("/" + rest if rest else "")


def guide_base_paths():
    """The paths of the Base URL column of the guide's table."""
    lines = GUIDE.read_text().splitlines()
    for i, line in enumerate(lines):
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if line.lstrip().startswith("|") and "Base URL" in cells:
            column = cells.index("Base URL")
            paths = []
            for row in lines[i + 2 :]:
                if not row.lstrip().startswith("|"):
                    break
                cell = [c.strip() for c in row.strip().strip("|").split("|")][column].strip("`")
                paths.append(compat_name(urllib.parse.urlparse(cell).path))
            return paths
    raise AssertionError(f"{GUIDE} has no table with a Base URL column")


def test_t566_the_guide_gives_the_base_urls_the_suite_uses():
    paths = guide_base_paths()
    assert {"/openai/v1", "/anthropic", "/compat/v1"} <= set(paths), paths
    assert OPENAI_BASE == BASE_URL + "/openai/v1"
    assert ANTHROPIC_BASE == BASE_URL + "/anthropic"
    assert COMPAT_BASE == BASE_URL + "/compat/v1"


@pytest.mark.parametrize("path", ["/openai/v1", "/compat/v1"])
def test_t566_openai_sdk_with_the_documented_base_url(path):
    assert path in guide_base_paths()
    client = openai_client(BASE_URL + path)
    completion = client.chat.completions.create(model="fake-basic", messages=[{"role": "user", "content": CANARY_PROMPT}])
    assert completion.choices[0].message.content == CANARY_COMPLETION


def test_t566_anthropic_sdk_with_the_documented_base_url():
    assert "/anthropic" in guide_base_paths()
    client = anthropic_client(BASE_URL + "/anthropic")
    message = client.messages.create(model="fake-basic", max_tokens=16, messages=[{"role": "user", "content": CANARY_PROMPT}])
    assert message.content[0].text == CANARY_COMPLETION
