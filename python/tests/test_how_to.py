"""Runs the example tool from the how-to, so its steps keep working."""

import os
import re
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
HOW_TO = REPO / "docs" / "how-to" / "write-a-python-tool.md"
# A Python block whose first line names its file, such as "# notes.py".
NAMED_BLOCK = re.compile(r"```python\n# (\S+\.py)\n(.*?)```", re.DOTALL)


class TestHowTo:
    """The page that walks a new tool author through one tool."""

    def test_example_tool_passes_its_own_tests(self, tmp_path):
        """The how-to's Python files work as written.

        Authors copy these blocks to start their first tool. A library
        change that breaks them should fail here, not in an author's broker.
        """
        for name, code in NAMED_BLOCK.findall(HOW_TO.read_text()):
            (tmp_path / name).write_text(code)
        subprocess.run(
            ["go", "build", "-o", tmp_path / "neurokit", "./cmd/neurokit"],
            cwd=REPO,
            check=True,
        )
        # serve starts the python3 on PATH, so put this test's Python, with
        # pydantic, there.
        python_bin = Path(sys.executable).parent
        env = {
            **os.environ,
            "PATH": f"{tmp_path}:{python_bin}:{os.environ['PATH']}",
            "PYTHONPATH": str(REPO / "python"),
        }
        subprocess.run(
            [
                sys.executable,
                "-m",
                "neurokit",
                "manifest",
                "notes",
                "manifest.json",
            ],
            cwd=tmp_path,
            env=env,
            check=True,
        )

        tests = subprocess.run(
            [sys.executable, "-m", "pytest", "test_notes.py"],
            cwd=tmp_path,
            env=env,
            capture_output=True,
            text=True,
        )

        assert tests.returncode == 0, tests.stdout + tests.stderr
