#!/usr/bin/env python3
"""Regression tests for Phase 2 review blockers."""

from __future__ import annotations

import unittest

import test_protocol_fixtures as protocol_validator


class Phase2ReviewFixesTests(unittest.TestCase):
    def test_schema_driven_validation_surface_exists(self) -> None:
        self.assertTrue(
            hasattr(protocol_validator, "validate_instance_against_schema"),
            "protocol validator must expose schema-driven fixture validation",
        )

    def test_runtime_redaction_module_is_used(self) -> None:
        self.assertTrue(
            hasattr(protocol_validator, "sanitize_log"),
            "protocol validator must use shared runtime redaction sanitizer",
        )

        sanitizer = protocol_validator.sanitize_log
        self.assertEqual(
            sanitizer.__module__,
            "log_redaction",
            "sanitize_log should be imported from shared runtime module",
        )
        sanitized = sanitizer(
            {"Authorization": "Bearer demo_token_value_123456"},
            "authorization=Bearer demo_token_value_123456",
        )
        self.assertNotIn("demo_token_value_123456", sanitized)
        self.assertIn("[REDACTED]", sanitized)


if __name__ == "__main__":
    unittest.main()
