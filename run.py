#!/usr/bin/env python3
"""Shortcut for `python -m medialib ...` that works from any directory: python run.py serve"""
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from medialib.cli import main  # noqa: E402

main()
