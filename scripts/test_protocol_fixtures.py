#!/usr/bin/env python3
"""Validate synthetic protocol fixtures for Phase 2."""

from __future__ import annotations

import json
from pathlib import Path

from log_redaction import sanitize_log


ROOT = Path(__file__).resolve().parents[1]
FIXTURES_DIR = ROOT / "fixtures"
INBOUND_DIR = FIXTURES_DIR / "anthropic-messages"
OUTBOUND_DIR = FIXTURES_DIR / "codex-responses"
REDACTION_DIR = FIXTURES_DIR / "redaction"
SCHEMAS_DIR = FIXTURES_DIR / "schemas"

INBOUND_CATEGORIES = {
    "agent_task_tools",
    "assistant_history_output_text",
    "auto_compact_history",
    "cache_control",
    "empty_text",
    "plain_text",
    "system_prompt",
    "max_tokens",
    "output_config",
    "stop_sequences",
    "tool_call_history",
    "tool_definitions",
    "tool_results",
    "multimodal_placeholder",
    "long_context",
    "invalid_field",
    "streaming_request",
}

OUTBOUND_CATEGORIES = {
    "normal_completion",
    "streamed_deltas",
    "tool_call",
    "tool_result",
    "refusal",
    "error_401",
    "error_403",
    "error_429",
    "timeout",
    "partial_stream_failure",
}

REQUIRED_SCHEMA_FILES = [
    "anthropic-message-fixture.schema.json",
    "codex-response-fixture.schema.json",
    "redaction-fixture.schema.json",
]


def load_json(path: Path) -> dict:
    with path.open("r", encoding="utf-8") as handle:
        data = json.load(handle)
    if not isinstance(data, dict):
        raise TypeError(f"{path} must contain a JSON object")
    return data


def list_json_files(path: Path) -> list[Path]:
    return sorted(path.glob("*.json"))


def ensure(condition: bool, message: str, errors: list[str]) -> None:
    if not condition:
        errors.append(message)


def is_json_integer(value: object) -> bool:
    return isinstance(value, int) and not isinstance(value, bool)


def matches_json_type(value: object, schema_type: str) -> bool:
    if schema_type == "object":
        return isinstance(value, dict)
    if schema_type == "array":
        return isinstance(value, list)
    if schema_type == "string":
        return isinstance(value, str)
    if schema_type == "integer":
        return is_json_integer(value)
    if schema_type == "number":
        return (isinstance(value, int) and not isinstance(value, bool)) or isinstance(
            value, float
        )
    if schema_type == "boolean":
        return isinstance(value, bool)
    if schema_type == "null":
        return value is None
    return True


def validate_instance_against_schema(
    instance: object,
    schema: dict[str, object],
    location: str,
    errors: list[str],
) -> None:
    schema_type = schema.get("type")
    if isinstance(schema_type, str) and not matches_json_type(instance, schema_type):
        errors.append(f"{location} expected type {schema_type}")
        return

    if isinstance(instance, dict):
        required = schema.get("required")
        if isinstance(required, list):
            for key in required:
                if isinstance(key, str) and key not in instance:
                    errors.append(f"{location} missing required field: {key}")

        properties = schema.get("properties")
        if isinstance(properties, dict):
            for key, value in instance.items():
                child_schema = properties.get(key)
                if isinstance(child_schema, dict):
                    validate_instance_against_schema(
                        value,
                        child_schema,
                        f"{location}.{key}",
                        errors,
                    )

            additional = schema.get("additionalProperties", True)
            if additional is False:
                allowed = {key for key in properties if isinstance(key, str)}
                for key in instance:
                    if key not in allowed:
                        errors.append(f"{location} has unexpected property: {key}")

    if isinstance(instance, list):
        items_schema = schema.get("items")
        if isinstance(items_schema, dict):
            for index, item in enumerate(instance):
                validate_instance_against_schema(
                    item,
                    items_schema,
                    f"{location}[{index}]",
                    errors,
                )


def load_required_schemas(errors: list[str]) -> dict[str, dict[str, object]]:
    schemas: dict[str, dict[str, object]] = {}
    ensure(SCHEMAS_DIR.exists(), "missing fixtures/schemas directory", errors)
    for schema_name in REQUIRED_SCHEMA_FILES:
        schema_path = SCHEMAS_DIR / schema_name
        ensure(schema_path.exists(), f"missing schema file: {schema_path}", errors)
        if not schema_path.exists():
            continue
        schema = load_json(schema_path)
        ensure("$schema" in schema, f"{schema_name} missing $schema", errors)
        ensure(schema.get("type") == "object", f"{schema_name} type must be object", errors)
        required = schema.get("required")
        ensure(
            isinstance(required, list) and len(required) >= 4,
            f"{schema_name} must define required keys",
            errors,
        )
        schemas[schema_name] = schema
    return schemas


def validate_fixtures_against_schema(
    directory: Path,
    schema: dict[str, object] | None,
    label: str,
    errors: list[str],
) -> None:
    ensure(directory.exists(), f"missing {directory} directory", errors)
    if schema is None:
        errors.append(f"missing schema for {label}")
        return
    for path in list_json_files(directory):
        payload = load_json(path)
        validate_instance_against_schema(payload, schema, path.name, errors)


def validate_inbound_fixtures(errors: list[str]) -> None:
    files = list_json_files(INBOUND_DIR)
    ensure(INBOUND_DIR.exists(), "missing fixtures/anthropic-messages directory", errors)
    ensure(len(files) >= 10, "need at least 10 inbound Anthropic fixtures", errors)

    seen_categories: set[str] = set()
    for path in files:
        payload = load_json(path)
        for field in ("fixture_id", "category", "description", "request"):
            ensure(field in payload, f"{path.name} missing {field}", errors)

        category = payload.get("category")
        ensure(category in INBOUND_CATEGORIES, f"{path.name} has unknown category", errors)
        if isinstance(category, str):
            seen_categories.add(category)

        request = payload.get("request")
        ensure(isinstance(request, dict), f"{path.name} request must be an object", errors)
        if not isinstance(request, dict):
            continue

        ensure(isinstance(request.get("model"), str), f"{path.name} request.model must be string", errors)
        ensure(isinstance(request.get("messages"), list), f"{path.name} request.messages must be list", errors)
        ensure(
            isinstance(request.get("max_tokens"), int) and request["max_tokens"] > 0,
            f"{path.name} request.max_tokens must be positive int",
            errors,
        )

        if category == "streaming_request":
            ensure(request.get("stream") is True, f"{path.name} must set stream=true", errors)
        if category == "stop_sequences":
            ensure(
                isinstance(request.get("stop_sequences"), list) and len(request["stop_sequences"]) > 0,
                f"{path.name} must include non-empty stop_sequences",
                errors,
            )
        if category == "tool_definitions":
            ensure(
                isinstance(request.get("tools"), list) and len(request["tools"]) > 0,
                f"{path.name} must include tools",
                errors,
            )
            ensure(isinstance(request.get("tool_choice"), dict), f"{path.name} missing tool_choice", errors)
        if category == "tool_results":
            messages = request.get("messages", [])
            flattened = []
            for msg in messages:
                if isinstance(msg, dict):
                    content = msg.get("content")
                    if isinstance(content, list):
                        flattened.extend(item for item in content if isinstance(item, dict))
            content_types = {item.get("type") for item in flattened}
            ensure("tool_use" in content_types, f"{path.name} must include tool_use content", errors)
            ensure("tool_result" in content_types, f"{path.name} must include tool_result content", errors)
        if category == "tool_call_history":
            messages = request.get("messages", [])
            flattened = []
            for msg in messages:
                if isinstance(msg, dict):
                    content = msg.get("content")
                    if isinstance(content, list):
                        flattened.extend(item for item in content if isinstance(item, dict))
            content_types = {item.get("type") for item in flattened}
            ensure("tool_use" in content_types, f"{path.name} must include assistant tool_use history", errors)
            ensure("tool_result" in content_types, f"{path.name} must include user tool_result history", errors)
        if category == "cache_control":
            encoded = json.dumps(request)
            ensure("cache_control" in encoded, f"{path.name} must include cache_control metadata", errors)
        if category == "output_config":
            ensure(isinstance(request.get("output_config"), dict), f"{path.name} must include output_config object", errors)
        if category == "empty_text":
            encoded = json.dumps(request)
            ensure('"text": ""' in encoded or '"text": " "' in encoded, f"{path.name} must include empty text history", errors)
        if category == "assistant_history_output_text":
            messages = request.get("messages", [])
            ensure(
                any(isinstance(msg, dict) and msg.get("role") == "assistant" for msg in messages),
                f"{path.name} must include assistant history",
                errors,
            )
        if category == "agent_task_tools":
            tools = request.get("tools")
            ensure(isinstance(tools, list), f"{path.name} must include tools", errors)
            names = {tool.get("name") for tool in tools if isinstance(tool, dict)}
            ensure({"Agent", "Task"}.issubset(names), f"{path.name} must include Agent and Task tool schemas", errors)
        if category == "auto_compact_history":
            encoded = json.dumps(request).lower()
            ensure("compact" in encoded or "summary" in encoded, f"{path.name} must include compacted history marker", errors)
        if category == "multimodal_placeholder":
            messages = request.get("messages", [])
            found_image = False
            for msg in messages:
                if not isinstance(msg, dict):
                    continue
                content = msg.get("content", [])
                if not isinstance(content, list):
                    continue
                for item in content:
                    if isinstance(item, dict) and item.get("type") == "image":
                        found_image = True
                        break
            ensure(found_image, f"{path.name} must include an image placeholder", errors)
        if category == "long_context":
            messages = request.get("messages", [])
            longest = 0
            for msg in messages:
                if not isinstance(msg, dict):
                    continue
                content = msg.get("content", [])
                if not isinstance(content, list):
                    continue
                for item in content:
                    if isinstance(item, dict) and isinstance(item.get("text"), str):
                        longest = max(longest, len(item["text"]))
            ensure(longest >= 2000, f"{path.name} long_context text is too short", errors)
        if category == "invalid_field":
            ensure(
                "unsupported_field" in request,
                f"{path.name} invalid_field fixture must include unsupported_field",
                errors,
            )
            ensure(
                isinstance(payload.get("expected_validation_error"), str),
                f"{path.name} must define expected_validation_error",
                errors,
            )

    missing = sorted(INBOUND_CATEGORIES - seen_categories)
    ensure(not missing, f"missing inbound categories: {', '.join(missing)}", errors)


def validate_outbound_fixtures(errors: list[str]) -> None:
    files = list_json_files(OUTBOUND_DIR)
    ensure(OUTBOUND_DIR.exists(), "missing fixtures/codex-responses directory", errors)
    ensure(len(files) >= 10, "need at least 10 outbound Codex fixtures", errors)

    seen_categories: set[str] = set()
    for path in files:
        payload = load_json(path)
        for field in ("fixture_id", "category", "description", "response"):
            ensure(field in payload, f"{path.name} missing {field}", errors)

        category = payload.get("category")
        ensure(category in OUTBOUND_CATEGORIES, f"{path.name} has unknown category", errors)
        if isinstance(category, str):
            seen_categories.add(category)

        response = payload.get("response")
        ensure(isinstance(response, dict), f"{path.name} response must be an object", errors)
        if not isinstance(response, dict):
            continue

        if category in {"error_401", "error_403", "error_429", "timeout"}:
            error_obj = response.get("error")
            ensure(isinstance(error_obj, dict), f"{path.name} missing response.error", errors)
            if isinstance(error_obj, dict):
                ensure(
                    isinstance(error_obj.get("http_status"), int),
                    f"{path.name} error.http_status must be int",
                    errors,
                )
                ensure(isinstance(error_obj.get("code"), str), f"{path.name} error.code must be string", errors)
            expected_status = {
                "error_401": 401,
                "error_403": 403,
                "error_429": 429,
                "timeout": 504,
            }[category]
            if isinstance(error_obj, dict):
                ensure(
                    error_obj.get("http_status") == expected_status,
                    f"{path.name} expected http_status {expected_status}",
                    errors,
                )
        if category in {"streamed_deltas", "partial_stream_failure"}:
            ensure(
                isinstance(response.get("stream"), list) and len(response["stream"]) > 0,
                f"{path.name} must include non-empty stream events",
                errors,
            )
            ensure(isinstance(response.get("final_status"), str), f"{path.name} missing final_status", errors)
        if category == "tool_call":
            output = response.get("output")
            ensure(isinstance(output, list), f"{path.name} tool_call response.output must be list", errors)
            if isinstance(output, list):
                ensure(
                    any(isinstance(item, dict) and item.get("type") == "tool_call" for item in output),
                    f"{path.name} must include tool_call output item",
                    errors,
                )
        if category == "tool_result":
            output = response.get("output")
            ensure(isinstance(output, list), f"{path.name} tool_result response.output must be list", errors)
            if isinstance(output, list):
                ensure(
                    any(isinstance(item, dict) and item.get("type") == "tool_result" for item in output),
                    f"{path.name} must include tool_result output item",
                    errors,
                )
        if category == "normal_completion":
            ensure(response.get("status") == "completed", f"{path.name} should be completed", errors)
        if category == "refusal":
            output = response.get("output")
            ensure(isinstance(output, list), f"{path.name} refusal response.output must be list", errors)
            if isinstance(output, list):
                ensure(
                    any(isinstance(item, dict) and item.get("type") == "refusal" for item in output),
                    f"{path.name} must include refusal output item",
                    errors,
                )

    missing = sorted(OUTBOUND_CATEGORIES - seen_categories)
    ensure(not missing, f"missing outbound categories: {', '.join(missing)}", errors)


def validate_redaction_fixtures(errors: list[str]) -> None:
    files = list_json_files(REDACTION_DIR)
    ensure(REDACTION_DIR.exists(), "missing fixtures/redaction directory", errors)
    ensure(len(files) >= 2, "need at least 2 redaction fixtures", errors)

    for path in files:
        payload = load_json(path)
        for field in ("fixture_id", "description", "input", "expected_absent", "expected_present"):
            ensure(field in payload, f"{path.name} missing {field}", errors)

        fixture_input = payload.get("input")
        ensure(isinstance(fixture_input, dict), f"{path.name} input must be object", errors)
        if not isinstance(fixture_input, dict):
            continue

        headers = fixture_input.get("headers")
        log_line = fixture_input.get("log_line")
        ensure(isinstance(headers, dict), f"{path.name} input.headers must be object", errors)
        ensure(isinstance(log_line, str), f"{path.name} input.log_line must be string", errors)
        if not isinstance(headers, dict) or not isinstance(log_line, str):
            continue

        sanitized = sanitize_log(headers, log_line)
        expected_absent = payload.get("expected_absent")
        expected_present = payload.get("expected_present")

        ensure(isinstance(expected_absent, list), f"{path.name} expected_absent must be list", errors)
        ensure(isinstance(expected_present, list), f"{path.name} expected_present must be list", errors)
        if isinstance(expected_absent, list):
            for token in expected_absent:
                ensure(
                    isinstance(token, str) and token not in sanitized,
                    f"{path.name} leaked sensitive token: {token}",
                    errors,
                )
        if isinstance(expected_present, list):
            for token in expected_present:
                ensure(
                    isinstance(token, str) and token in sanitized,
                    f"{path.name} missing expected redaction marker: {token}",
                    errors,
                )


def main() -> int:
    errors: list[str] = []
    schemas = load_required_schemas(errors)
    validate_fixtures_against_schema(
        INBOUND_DIR,
        schemas.get("anthropic-message-fixture.schema.json"),
        "anthropic-messages",
        errors,
    )
    validate_fixtures_against_schema(
        OUTBOUND_DIR,
        schemas.get("codex-response-fixture.schema.json"),
        "codex-responses",
        errors,
    )
    validate_fixtures_against_schema(
        REDACTION_DIR,
        schemas.get("redaction-fixture.schema.json"),
        "redaction",
        errors,
    )
    validate_inbound_fixtures(errors)
    validate_outbound_fixtures(errors)
    validate_redaction_fixtures(errors)

    if errors:
        print("FAIL: protocol fixture validation failed")
        for index, message in enumerate(errors, start=1):
            print(f"{index}. {message}")
        return 1

    inbound_count = len(list_json_files(INBOUND_DIR))
    outbound_count = len(list_json_files(OUTBOUND_DIR))
    redaction_count = len(list_json_files(REDACTION_DIR))
    print(
        "OK: protocol fixtures validated "
        f"(inbound={inbound_count}, outbound={outbound_count}, redaction={redaction_count})"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
