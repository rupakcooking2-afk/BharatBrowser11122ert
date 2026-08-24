"""Tests for GitHubReleaseStore failure classification, retry logic,
release lifecycle handling, and safe asset replacement (--clobber)."""
import json
import subprocess

import pytest

from scripts.ci.build_system import github_storage as gs
from scripts.ci.build_system.checkpoint import CheckpointManager
from scripts.ci.build_system.github_storage import (
    CATEGORY_ALREADY_EXISTS,
    CATEGORY_AUTH,
    CATEGORY_NOT_FOUND,
    CATEGORY_PERMISSION,
    CATEGORY_TRANSIENT,
    CATEGORY_UNKNOWN,
    GhCommandError,
    GitHubReleaseStore,
    classify_gh_failure,
)


def _gh_error(returncode: int, stderr: str = "") -> subprocess.CalledProcessError:
    """Build a CalledProcessError shaped like the ones _gh() produces."""
    exc = subprocess.CalledProcessError(returncode, ["gh"], "", stderr)
    return exc


@pytest.fixture
def store(tmp_path, monkeypatch):
    monkeypatch.setenv("GITHUB_REPOSITORY", "acme/bharat")
    return GitHubReleaseStore("windows-x64", "out/Default_x64", tmp_path / "src")


# ---------------------------------------------------------------------------
# classify_gh_failure
# ---------------------------------------------------------------------------


class TestClassify:
    def test_exit_code_4_is_auth_even_with_empty_stderr(self):
        assert classify_gh_failure(4, "") == CATEGORY_AUTH

    def test_auth_login_prompt(self):
        assert classify_gh_failure(
            1, "gh: To get started with GitHub CLI, please run: gh auth login"
        ) == CATEGORY_AUTH

    def test_bad_credentials(self):
        assert classify_gh_failure(1, "HTTP 401: Bad credentials") == CATEGORY_AUTH

    def test_permission_403(self):
        assert classify_gh_failure(
            1, "HTTP 403: Resource not accessible by integration"
        ) == CATEGORY_PERMISSION

    def test_not_found(self):
        assert classify_gh_failure(1, "release not found") == CATEGORY_NOT_FOUND

    def test_already_exists(self):
        assert classify_gh_failure(
            1, "HTTP 422: Validation Failed (code=already_exists)"
        ) == CATEGORY_ALREADY_EXISTS

    def test_transient_connection_reset(self):
        assert classify_gh_failure(1, "connection reset by peer") == CATEGORY_TRANSIENT

    def test_transient_http_500(self):
        assert classify_gh_failure(500, "internal server error") == CATEGORY_TRANSIENT

    def test_unknown_is_not_retried(self):
        cat = classify_gh_failure(64, "something completely unexpected")
        assert cat == CATEGORY_UNKNOWN
        assert not GhCommandError("x", cat).is_retryable


# ---------------------------------------------------------------------------
# _gh_retry
# ---------------------------------------------------------------------------


class TestGhRetry:
    def test_retries_transient_then_succeeds(self, store, monkeypatch):
        sleeps = []
        monkeypatch.setattr(gs.time, "sleep", lambda s: sleeps.append(s))
        calls = []

        def flaky(args, timeout=120):
            calls.append(args)
            if len(calls) < 3:
                raise _gh_error(1, "connection reset by peer")
            return "ok"

        monkeypatch.setattr(store, "_gh", flaky)
        assert store._gh_retry(["release", "view", "t"]) == "ok"
        assert len(calls) == 3
        assert len(sleeps) == 2

    def test_does_not_retry_auth_errors(self, store, monkeypatch):
        sleeps = []
        monkeypatch.setattr(gs.time, "sleep", lambda s: sleeps.append(s))
        calls = []

        def no_token(args, timeout=120):
            calls.append(args)
            raise _gh_error(4, "")

        monkeypatch.setattr(store, "_gh", no_token)
        with pytest.raises(GhCommandError) as exc_info:
            store._gh_retry(["release", "view", "t"])
        assert exc_info.value.category == CATEGORY_AUTH
        # exit code 4 (missing GH_TOKEN) must fail fast — retrying is pointless
        assert len(calls) == 1
        assert sleeps == []

    def test_does_not_retry_permission_errors(self, store, monkeypatch):
        monkeypatch.setattr(gs.time, "sleep", lambda s: None)
        monkeypatch.setattr(
            store, "_gh",
            lambda a, timeout=120: (_ for _ in ()).throw(
                _gh_error(1, "HTTP 403: Resource not accessible by integration")),
        )
        with pytest.raises(GhCommandError) as exc_info:
            store._gh_retry(["release", "create", "t"])
        assert exc_info.value.category == CATEGORY_PERMISSION

    def test_raises_gh_command_error_when_retries_exhausted(self, store, monkeypatch):
        monkeypatch.setattr(gs.time, "sleep", lambda s: None)
        monkeypatch.setattr(
            store, "_gh",
            lambda a, timeout=120: (_ for _ in ()).throw(
                _gh_error(1, "connection refused")),
        )
        with pytest.raises(GhCommandError) as exc_info:
            store._gh_retry(["release", "view", "t"])
        assert exc_info.value.category == CATEGORY_TRANSIENT


# ---------------------------------------------------------------------------
# ensure_release / release_exists
# ---------------------------------------------------------------------------


class TestEnsureRelease:
    def test_reuses_existing_release(self, store, monkeypatch):
        calls = []

        def view_ok(args, timeout=120, description=None):
            calls.append(list(args))
            return ""

        monkeypatch.setattr(store, "_gh_retry", view_ok)
        assert store.ensure_release() is True
        assert len(calls) == 1
        assert calls[0][:2] == ["release", "view"]

    def test_creates_release_when_missing(self, store, monkeypatch):
        calls = []

        def fake(args, timeout=120, description=None):
            args = list(args)
            calls.append(args)
            if args[:2] == ["release", "view"]:
                raise GhCommandError("nf", CATEGORY_NOT_FOUND)
            return ""

        monkeypatch.setattr(store, "_gh_retry", fake)
        assert store.ensure_release() is True
        create_calls = [c for c in calls if c[:2] == ["release", "create"]]
        assert len(create_calls) == 1
        assert "--latest=false" in create_calls[0]

    def test_concurrent_create_race_treated_as_success(self, store, monkeypatch):
        def fake(args, timeout=120, description=None):
            if list(args)[:2] == ["release", "view"]:
                raise GhCommandError("nf", CATEGORY_NOT_FOUND)
            raise GhCommandError("dup", CATEGORY_ALREADY_EXISTS)

        monkeypatch.setattr(store, "_gh_retry", fake)
        assert store.ensure_release() is True

    def test_auth_failure_returns_false_without_raising(self, store, monkeypatch):
        monkeypatch.setattr(
            store, "_gh_retry",
            lambda a, timeout=120, description=None: (_ for _ in ()).throw(
                GhCommandError("no token", CATEGORY_AUTH)),
        )
        assert store.ensure_release() is False


# ---------------------------------------------------------------------------
# Uploads: auth fast-fail + --clobber asset replacement
# ---------------------------------------------------------------------------


class TestUploads:
    def test_ninja_state_skips_upload_when_release_unavailable(self, store, monkeypatch):
        calls = []

        def fake(args, timeout=120, description=None):
            calls.append(list(args))
            raise GhCommandError("no token", CATEGORY_AUTH)

        monkeypatch.setattr(store, "_gh_retry", fake)
        assert store.upload_ninja_state() is False
        # Only the release *check* ran; no upload attempted after an auth error.
        assert all(c[0] != "upload" for c in calls)

    def test_obj_delta_upload_uses_clobber(self, store, tmp_path, monkeypatch):
        build_dir = tmp_path / "src" / "out" / "Default_x64" / "obj"
        build_dir.mkdir(parents=True)
        obj_file = build_dir / "foo.o"
        obj_file.write_bytes(b"\x00\x01")
        recorded = []

        def fake(args, timeout=120, description=None):
            recorded.append([str(a) for a in args])
            return ""

        monkeypatch.setattr(store, "_gh_retry", fake)
        store.upload_obj_delta(since_wall_time=0.0, seq=1)

        upload_calls = [c for c in recorded if c[1] == "upload"]
        assert len(upload_calls) == 1
        assert "--clobber" in upload_calls[0]
        assert any("obj-delta-001.tar.gz" in part for part in upload_calls[0])


# ---------------------------------------------------------------------------
# CheckpointManager counter semantics
# ---------------------------------------------------------------------------


class TestCheckpointCounter:
    @pytest.fixture
    def manager(self, tmp_path, monkeypatch):
        monkeypatch.setenv("GITHUB_REPOSITORY", "acme/bharat")
        build_dir = tmp_path / "src" / "out" / "Default_x64"
        build_dir.mkdir(parents=True)
        (build_dir / "build_state.json").write_text(json.dumps({"checkpoint_counter": 0}))
        return CheckpointManager("windows-x64", "out/Default_x64", tmp_path / "src")

    def test_failed_checkpoint_still_advances_counter(self, manager, monkeypatch):
        monkeypatch.setattr(manager.store, "upload_ninja_state", lambda: False)
        monkeypatch.setattr(manager.store, "upload_obj_delta", lambda s, q: True)
        seq, ok = manager.create_checkpoint(123.0)
        assert (seq, ok) == ("001", False)
        data = json.loads(
            (manager.chromium_src / "out/Default_x64/build_state.json").read_text()
        )
        assert data["checkpoint_counter"] == 1

    def test_successful_checkpoint_reports_ok(self, manager, monkeypatch):
        monkeypatch.setattr(manager.store, "upload_ninja_state", lambda: True)
        monkeypatch.setattr(manager.store, "upload_obj_delta", lambda s, q: True)
        seq, ok = manager.create_checkpoint(123.0)
        assert (seq, ok) == ("001", True)
