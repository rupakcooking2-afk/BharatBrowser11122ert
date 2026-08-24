"""Regression tests for the nightly-build workflow checkpoint configuration.

These pin the two invariants that make checkpoint persistence possible:

1. the build job declares ``contents: write`` (minimum permission needed
   for gh release create/upload/delete — nothing more),
2. every step invoking resume_build.py exports GH_TOKEN (gh exits with
   code 4 when no token is available, which is how this whole class of
   failure started).
"""
from pathlib import Path

WORKFLOW = (
    Path(__file__).resolve().parents[3] / ".github" / "workflows" / "nightly-build.yml"
)


def _text() -> str:
    return WORKFLOW.read_text(encoding="utf-8")


def _code(text: str) -> str:
    """Drop full-line YAML comments so prose cannot trip assertions."""
    return "\n".join(
        line for line in text.splitlines() if not line.lstrip().startswith("#")
    )


def test_workflow_file_exists():
    assert WORKFLOW.is_file()


def test_top_level_permissions_stay_read_only():
    top = _code(_text().split("jobs:", 1)[0])
    assert "contents: read" in top
    # No elevated scope may leak to every job via the top-level block.
    for scope in ("contents: write", "id-token: write", "packages: write"):
        assert scope not in top


def test_build_job_has_minimum_contents_write():
    build_job = _code(_text()).split("  build:", 1)[1]
    job_header = build_job.split("steps:", 1)[0]
    assert "contents: write" in job_header
    # Minimum-permission guarantee: no extra scopes on the build job.
    for scope in ("id-token: write", "packages: write", "security-events: write"):
        assert scope not in job_header


def test_compiling_step_exports_gh_token():
    compiling = _code(_text()).split('"State: COMPILING', 1)[1].split("- name:", 1)[0]
    assert "GH_TOKEN: ${{ github.token }}" in compiling


def test_clear_step_exports_gh_token():
    """The PATCHING/CONFIGURING step runs `resume_build.py clear` (force mode)
    which deletes the checkpoint release — it needs credentials too."""
    patching = _code(_text()).split('"State: PATCHING/CONFIGURING', 1)[1].split("- name:", 1)[0]
    assert "GH_TOKEN: ${{ github.token }}" in patching


def test_compiling_step_fails_fast_without_token():
    text = _text()
    assert '[ -z "${GH_TOKEN:-}" ]' in text
