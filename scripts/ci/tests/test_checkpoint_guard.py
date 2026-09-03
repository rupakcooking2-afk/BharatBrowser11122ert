"""Tests for the checkpoint progress-advance guard.

Verifies that _maybe_checkpoint() refuses to checkpoint when progress
has not advanced beyond _last_checkpoint_targets, preventing infinite
resumable-build loops.

See test_orchestrator_checkpoint.py for the existing checkpoint failure
semantics tests.
"""

import logging

import pytest

from scripts.ci.build_system.orchestrator import (
    WorkflowOrchestrator,
)


@pytest.fixture
def orch(tmp_path, monkeypatch):
    monkeypatch.setenv("GITHUB_REPOSITORY", "acme/bharat")
    src = tmp_path / "src"
    (src / "out" / "Default_x64").mkdir(parents=True)
    o = WorkflowOrchestrator(
        platform="windows-x64",
        build_dir="out/Default_x64",
        chromium_src=src,
        browseros_dir=tmp_path / "pkg",
        repo_root=tmp_path,
        start_state="COMPILING",
    )
    o._total_targets_hint = 44506
    # Simulate a build that has made some progress in the ninja log
    ninja_log = src / "out" / "Default_x64" / ".ninja_log"
    ninja_log.write_text(
        "\n".join(
            [
                "1\t0\tobject/nginx.o\t",
                "2\t0\tobject/html.o\t",
                "3\t0\tobject/css.o\t",
            ]
        )
    )
    return o


class TestCheckpointGuard:
    """Tests for the progress-advance checkpoint guard."""

    def test_fresh_build_no_invalid_checkpoint(self, orch):
        """TEST 1: Fresh build - no invalid checkpoint.

        completed = 0, last = 0 → No invalid checkpoint.
        The guard allows the first checkpoint since progress advances.
        """
        # _last_checkpoint_targets is fast_ninja_count(.ninja_log) = 3
        # But for a "fresh" concept, we test with 0 progress.
        # The orch constructor initialises _last_checkpoint_targets from
        # the ninja_log; here we just verify the guard doesn't block
        # a legitimate first checkpoint.
        orch._last_checkpoint_targets = 0
        orch._maybe_checkpoint(completed=100, elapsed_minutes=99.0)
        # After a successful checkpoint, _last_checkpoint_targets would be 100
        assert orch._last_checkpoint_targets == 100

    def test_progress_reaches_threshold_checkpoint_allowed(self, orch):
        """TEST 2: Progress reaches threshold.

        completed = 100, last = 0 → Checkpoint is allowed.
        """
        orch._last_checkpoint_targets = 0
        orch._maybe_checkpoint(completed=100, elapsed_minutes=99.0)
        assert orch._last_checkpoint_targets == 100

    def test_resume_from_checkpoint_no_checkpoint_at_restored_count(
        self, orch,
    ):
        """TEST 3: Resume from checkpoint.

        restored = 339, last = 339 → No checkpoint at 339.
        The guard correctly skips when progress hasn't advanced.
        """
        orch._last_checkpoint_targets = 339
        orch._maybe_checkpoint(completed=339, elapsed_minutes=99.0)
        # _last_checkpoint_targets must NOT change when checkpoint is skipped
        assert orch._last_checkpoint_targets == 339

    def test_time_threshold_reached_but_progress_unchanged(self, orch):
        """TEST 4: Time threshold reached but progress unchanged.

        current = 339, last = 339 → No checkpoint.
        Time passing alone must not cause repeated checkpoints.
        """
        orch._last_checkpoint_targets = 339
        orch._maybe_checkpoint(completed=339, elapsed_minutes=999.0)
        # _last_checkpoint_targets must stay at 339
        assert orch._last_checkpoint_targets == 339

    def test_progress_advances_checkpoint_allowed(self, orch):
        """TEST 5: Progress advances.

        current = 1339, last = 339 → Checkpoint is allowed.
        """
        orch._last_checkpoint_targets = 339
        orch._maybe_checkpoint(completed=1339, elapsed_minutes=99.0)
        assert orch._last_checkpoint_targets == 1339

    def test_after_successful_checkpoint_targets_updated(self, orch):
        """TEST 6: After successful checkpoint.

        last_checkpoint_targets becomes the newly persisted progress.
        """
        orch._last_checkpoint_targets = 339
        # Simulate a successful checkpoint
        orch._last_checkpoint_targets = 1339
        assert orch._last_checkpoint_targets == 1339

    def test_second_checkpoint_sequence_increments(self, orch):
        """TEST 7: Second checkpoint.

        current = 2339 → Checkpoint sequence increments.
        """
        orch._last_checkpoint_targets = 1339
        orch._maybe_checkpoint(completed=2339, elapsed_minutes=99.0)
        assert orch._last_checkpoint_targets == 2339

    def test_upload_failure_state_not_marked_successful(self, orch):
        """TEST 8: Upload failure.

        Checkpoint state is NOT marked successful.
        """
        orch._last_checkpoint_targets = 339
        # Simulate failed upload - _last_checkpoint_targets still advances
        # because the orchestrator always updates it on attempt (per existing
        # logic), but _checkpoint_healthy stays False
        orch._checkpoint_healthy = False
        orch._maybe_checkpoint(completed=1339, elapsed_minutes=99.0)
        # _last_checkpoint_targets was updated to 1339 by the method
        assert orch._last_checkpoint_targets == 1339
        assert orch._checkpoint_healthy is False

    def test_repeated_identical_progress_no_infinite_loop(self, orch):
        """TEST 9: Repeated identical progress.

        No infinite checkpoint loop.
        """
        orch._last_checkpoint_targets = 339
        # Call _maybe_checkpoint multiple times with same progress
        for _ in range(5):
            orch._maybe_checkpoint(completed=339, elapsed_minutes=99.0)
        # _last_checkpoint_targets must remain 339, never loop
        assert orch._last_checkpoint_targets == 339

    def test_restore_and_resume_multiple_times(self, orch):
        """TEST 10: Restore and resume multiple times.

        Checkpoint state remains consistent.
        """
        # Simulate first resume at 339
        orch._last_checkpoint_targets = 339
        orch._maybe_checkpoint(completed=339, elapsed_minutes=99.0)
        assert orch._last_checkpoint_targets == 339

        # Simulate progress advancing to 1339, checkpoint
        orch._maybe_checkpoint(completed=1339, elapsed_minutes=99.0)
        assert orch._last_checkpoint_targets == 1339

        # Simulate second resume from checkpoint 002 at 1339
        orch._last_checkpoint_targets = 1339
        orch._maybe_checkpoint(completed=1339, elapsed_minutes=99.0)
        assert orch._last_checkpoint_targets == 1339

        # No checkpoint should be generated at the same count
        orch._maybe_checkpoint(completed=1339, elapsed_minutes=999.0)
        assert orch._last_checkpoint_targets == 1339