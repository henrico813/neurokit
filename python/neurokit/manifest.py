"""Builds manifest.json from a Tool, reading limits through pydantic.

Each parameter's type hint, such as ``Annotated[str, Field(max_length=200)]``,
becomes pydantic's JSON Schema, which maps onto a manifest argument. A limit
the manifest cannot hold, such as ``min_length``, is refused, never dropped.
"""

import inspect
import typing

from pydantic import TypeAdapter

# The manifest format version; serve refuses any other.
SPEC = 1
# The JSON Schema keys each type maps; any other key is refused.
MAPPED_KEYS = {
    "string": {"maxLength"},
    "integer": {"minimum", "maximum"},
    "boolean": set(),
    "array": {"items", "maxItems"},
    "object": {"properties", "required"},
}
REQUIRED_LIMITS = {
    "string": {"maxLength"},
    "integer": {"minimum", "maximum"},
    "array": {"maxItems"},
}
# Keys that limit nothing. A ``default`` is refused, because the broker
# never fills one in.
IGNORED_KEYS = {"type", "title", "description", "$defs"}


def build_manifest(tool, module_name):
    """Returns the manifest as a dict, telling serve to import module_name.

    Raises:
        ValueError: A parameter has no manifest form. The message is one
            line, such as ``f(name): the manifest cannot hold ['minLength']``.
    """
    runner = ["python3", "-m", "neurokit"]
    return {
        "name": tool.name,
        "spec": SPEC,
        "broker_url_env": tool.broker_url_env,
        "max_handlers": tool.max_handlers,
        "program": [*runner, "handle", module_name],
        "check": [*runner, "check", module_name],
        "commands": {
            name: build_command_entry(command)
            for name, command in sorted(tool.commands.items())
        },
    }


def build_command_entry(command):
    """Returns one command's manifest entry, with args from its type hints."""
    # Settings left at their defaults stay out, as in a hand-written manifest.
    entry = {
        key: value
        for key, value in command.settings.items()
        if value is not None and value is not False and value != []
    }
    hints = typing.get_type_hints(command.function, include_extras=True)
    args = {}
    for param in inspect.signature(command.function).parameters.values():
        where = f"{command.function.__name__}({param.name})"
        if param.name not in hints:
            raise ValueError(f"{where}: needs a type hint")
        schema = TypeAdapter(hints[param.name]).json_schema()
        args[param.name] = to_manifest_arg(
            schema, schema.get("$defs", {}), where
        )
        if param.default is not param.empty:
            args[param.name]["optional"] = True
    if args:
        entry["args"] = args
    return entry


def to_manifest_arg(schema, defs, where):
    """Returns the manifest argument for one JSON Schema.

    Raises:
        ValueError: The schema has no manifest form; the message starts
            with where, such as ``plan(name)``.
    """
    if "$ref" in schema:
        schema = defs[schema["$ref"].rsplit("/", 1)[-1]]
    kind = schema.get("type")
    if kind not in MAPPED_KEYS:
        raise ValueError(f"{where}: the manifest has no type for {schema}")
    unmapped = set(schema) - MAPPED_KEYS[kind] - IGNORED_KEYS
    if unmapped:
        raise ValueError(
            f"{where}: the manifest cannot hold {sorted(unmapped)}"
        )
    missing = REQUIRED_LIMITS.get(kind, set()) - set(schema)
    if missing:
        raise ValueError(f"{where}: needs a Field limit for {sorted(missing)}")
    if kind == "string":
        return {"type": "string", "max_length": schema["maxLength"]}
    if kind == "integer":
        return {
            "type": "integer",
            "min": schema["minimum"],
            "max": schema["maximum"],
        }
    if kind == "boolean":
        return {"type": "boolean"}
    if kind == "array":
        return {
            "type": "list",
            "items": to_manifest_arg(schema["items"], defs, where),
            "max_items": schema["maxItems"],
        }
    required = set(schema.get("required", []))
    fields = {}
    for name, field in schema["properties"].items():
        fields[name] = to_manifest_arg(field, defs, f"{where}.{name}")
        if name not in required:
            fields[name]["optional"] = True
    return {"type": "object", "fields": fields}
