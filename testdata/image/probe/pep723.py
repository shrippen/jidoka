#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
# The start line of a job script with inline metadata (PEP 723); it needs
# an env that knows -S.
print("pep723=ok")
