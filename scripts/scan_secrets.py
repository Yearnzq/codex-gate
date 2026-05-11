#!/usr/bin/env python3
"""Standalone secret scan wrapper."""

from validate import scan_for_secrets


if __name__ == "__main__":
    scan_for_secrets()
    print("OK: no obvious secrets found")
