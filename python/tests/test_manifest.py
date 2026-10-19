"""Tests for writing manifest.json from a tool's type hints."""

from typing import Annotated, NotRequired, TypedDict

import pytest
from pydantic import Field

from neurokit import Broker
from neurokit.manifest import build_manifest


class Slot(TypedDict):
    """One slot in a plan, as an object argument with an optional field."""

    day: Annotated[str, Field(max_length=10)]
    note: NotRequired[Annotated[str, Field(max_length=40)]]


def build_args(function):
    """Returns the manifest args that function's type hints become."""
    broker = Broker("t", broker_url_env="T_URL", max_handlers=1)
    broker.command(deadline_ms=1, max_body_bytes=1, max_response_bytes=1)(
        function
    )
    name = function.__name__.replace("_", "-")
    return build_manifest(broker, "t")["commands"][name]["args"]


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
            label: Annotated[str, Field(max_length=30)] | None = None,
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
            "label": {"type": "string", "max_length": 30, "optional": True},
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
            pytest.param(
                Annotated[str | None, Field(default="x", max_length=5)],
                "the manifest cannot hold ['default']",
                id="union-with-field-default",
            ),
        ],
    )
    def test_refuses_hints_manifest_cannot_express(self, hint, error):
        """A type hint the manifest cannot express is refused with a message
        naming the parameter.

        Writing the manifest anyway would drop a limit the author set, or
        leave out one the broker requires.
        """

        def plan(name): ...

        plan.__annotations__ = {"name": hint}

        with pytest.raises(ValueError) as raised:
            build_args(plan)

        assert str(raised.value) == f"plan(name): {error}"

    @pytest.mark.parametrize(
        ("hint", "defaults"),
        [
            pytest.param(
                Annotated[str, Field(max_length=5)] | None,
                None,
                id="optional-without-default",
            ),
            pytest.param(
                Annotated[str, Field(max_length=5)] | None,
                ("Ann",),
                id="optional-with-other-default",
            ),
            pytest.param(
                Annotated[str, Field(max_length=5)]
                | Annotated[int, Field(ge=0, le=9)],
                (None,),
                id="two-types-without-none",
            ),
        ],
    )
    def test_refuses_unions_except_none_default(self, hint, defaults):
        """Only ``X | None = None`` is accepted as a union.

        A manifest argument has one type, and the broker refuses null, so
        a required ``X | None``, or one with another default, could never
        reach the function's None case.
        """

        def plan(name): ...

        plan.__annotations__ = {"name": hint}
        plan.__defaults__ = defaults

        with pytest.raises(ValueError) as raised:
            build_args(plan)

        assert str(raised.value) == (
            "plan(name): a union must be X | None = None"
        )
