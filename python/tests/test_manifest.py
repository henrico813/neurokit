"""Tests for writing manifest.json from a tool's type hints."""

from typing import Annotated, NotRequired, TypedDict

import pytest
from pydantic import Field

from neurokit import Tool
from neurokit.manifest import build_manifest


class Slot(TypedDict):
    """One slot in a plan, as an object argument with an optional field."""

    day: Annotated[str, Field(max_length=10)]
    note: NotRequired[Annotated[str, Field(max_length=40)]]


def build_args(function):
    """Returns the manifest args that function's type hints become."""
    tool = Tool("t", broker_url_env="T_URL", max_handlers=1)
    tool.command(deadline_ms=1, max_body_bytes=1, max_response_bytes=1)(
        function
    )
    name = function.__name__.replace("_", "-")
    return build_manifest(tool, "t")["commands"][name]["args"]


class TestManifest:
    """How type hints become the limits the broker enforces."""

    def test_type_hints_become_argument_limits(self):
        """Each Field limit lands where the broker reads it.

        The broker enforces only what the manifest says, so a limit put
        under the wrong key, or a default not marked optional, would let
        bad input through or refuse good input.
        """

        def plan(
            name: Annotated[str, Field(max_length=20)],
            count: Annotated[int, Field(ge=1, le=50)],
            slots: Annotated[list[Slot], Field(max_length=7)],
            dry_run: bool = False,
        ): ...

        args = build_args(plan)

        assert args == {
            "name": {"type": "string", "max_length": 20},
            "count": {"type": "integer", "min": 1, "max": 50},
            "slots": {
                "type": "list",
                "items": {
                    "type": "object",
                    "fields": {
                        "day": {"type": "string", "max_length": 10},
                        "note": {
                            "type": "string",
                            "max_length": 40,
                            "optional": True,
                        },
                    },
                },
                "max_items": 7,
            },
            "dry_run": {"type": "boolean", "optional": True},
        }

    @pytest.mark.parametrize(
        ("hint", "error"),
        [
            pytest.param(
                Annotated[str, Field(min_length=1, max_length=5)],
                "the manifest cannot hold ['minLength']",
                id="limit-without-manifest-key",
            ),
            pytest.param(
                str,
                "needs a Field limit for ['maxLength']",
                id="string-without-limit",
            ),
            pytest.param(
                dict[str, str],
                "the manifest cannot hold ['additionalProperties']",
                id="dict-without-fields",
            ),
        ],
    )
    def test_refuses_hints_manifest_cannot_express(self, hint, error):
        """A limit with no place in the manifest stops the build.

        Dropping it would leave the author believing the broker enforces
        it, while any value gets through.
        """

        def plan(name): ...

        plan.__annotations__ = {"name": hint}

        with pytest.raises(ValueError) as raised:
            build_args(plan)

        assert str(raised.value) == f"plan(name): {error}"
