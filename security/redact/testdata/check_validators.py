#!/usr/bin/env python3
"""Mutation check: bypass each validator in a disposable copy of this module.

Run from any directory with Python 3 and Go installed. A mutant only counts as
killed when the public detector's negative tests fail, not on a build failure.
"""
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

module = Path(__file__).resolve().parents[1]
mutants = [
    ("pii_validators.go", "validEmail", "email"),
    ("pii_validators.go", "validSSN", "ssn"),
    ("pii_validators.go", "validNINO", ""),
    ("pii_validators.go", "validIBAN", "iban"),
    ("pii.go", "luhn", "card"),
]
for filename, validator, subtest in mutants:
    with tempfile.TemporaryDirectory(prefix="redact-validator-") as tmp:
        root = Path(tmp)
        for source in module.iterdir():
            if source.suffix == ".go" or source.name in ("go.mod", "go.sum"):
                shutil.copy2(source, root / source.name)
        target = root / filename
        text = target.read_text()
        pattern = rf"(func {validator}\(s string\) bool \{{).*?\n\}}"
        text, changed = re.subn(pattern, r"\1\n\treturn true\n}", text, count=1, flags=re.S)
        if not changed:
            raise SystemExit(f"Cannot locate {validator}")
        target.write_text(text)
        result = subprocess.run(
            ["go", "test", "-run", "^TestPIIValidators$", "-count=1"],
            cwd=root, env={**os.environ, "GOWORK": "off"},
            text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        )
        expected = "--- FAIL: TestPIIValidators"
        if subtest:
            expected += "/" + subtest
        if result.returncode == 0 or "[build failed]" in result.stdout or expected not in result.stdout:
            print(result.stdout)
            raise SystemExit(f"Validator mutation was not killed by negative cases: {validator}")
        print(f"KILLED {validator}: public detector negative cases failed")
