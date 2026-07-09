"""Automatic retry module for a fault-tolerant distributed Chromium build system.

Differentiates transient failures (retry) from permanent failures (don't retry)
and implements exponential backoff with jitter.
"""

import asyncio
import logging
import random
import subprocess
from typing import Any, Optional, Set, Tuple

__all__ = [
    "MaxRetriesExceeded",
    "PermanentFailure",
    "RetryConfig",
    "is_transient_failure",
    "should_retry_aws",
    "should_retry_github",
    "retry",
    "RetryContext",
]

logger = logging.getLogger(__name__)


class MaxRetriesExceeded(Exception):
    """Raised when all retry attempts have been exhausted."""
    pass


class PermanentFailure(Exception):
    """Raised when a non-transient failure is encountered
    and the operation should not be retried."""
    pass


class RetryConfig:
    """Configuration for retry behaviour.

    Attributes
    ----------
    max_attempts : int
        Maximum number of attempts (default 3).
    base_delay_seconds : float
        Initial delay before the first retry (default 5.0).
    max_delay_seconds : float
        Upper bound for the exponential-backoff delay (default 120.0).
    backoff_multiplier : float
        Factor by which the delay is multiplied each attempt (default 3.0
        yields 5s, 15s, 45s).
    jitter : bool
        When True, adds ±25 % random jitter to each computed delay.
    retryable_exceptions : Tuple[type, ...]
        Exception types considered transient (default: ``ConnectionError``,
        ``TimeoutError``, ``OSError``, ``subprocess.CalledProcessError``).
    retryable_exit_codes : Set[int]
        Exit codes considered transient (default: 125, 127, 137, 139,
        143, 255 — SIGKILL, SIGSEGV, timeout, etc.).
    """

    def __init__(
        self,
        max_attempts: int = 3,
        base_delay_seconds: float = 5.0,
        max_delay_seconds: float = 120.0,
        backoff_multiplier: float = 3.0,
        jitter: bool = True,
        retryable_exceptions: Optional[Tuple[type, ...]] = None,
        retryable_exit_codes: Optional[Set[int]] = None,
    ) -> None:
        self.max_attempts = max_attempts
        self.base_delay_seconds = base_delay_seconds
        self.max_delay_seconds = max_delay_seconds
        self.backoff_multiplier = backoff_multiplier
        self.jitter = jitter
        self.retryable_exceptions = (
            retryable_exceptions
            if retryable_exceptions is not None
            else (ConnectionError, TimeoutError, OSError, subprocess.CalledProcessError)
        )
        self.retryable_exit_codes = (
            retryable_exit_codes
            if retryable_exit_codes is not None
            else {125, 127, 137, 139, 143, 255}
        )


# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------

def _compute_delay(attempt: int, config: RetryConfig) -> float:
    """Exponential backoff delay for the *attempt*-th retry (1-indexed)."""
    delay = min(
        config.base_delay_seconds * (config.backoff_multiplier ** (attempt - 1)),
        config.max_delay_seconds,
    )
    if config.jitter:
        delay *= random.uniform(0.75, 1.25)
    return delay


def _extract_exit_code(exception: Exception) -> Optional[int]:
    """Pull the exit / return code from a known exception, if present."""
    if isinstance(exception, subprocess.CalledProcessError):
        return exception.returncode
    if hasattr(exception, "exit_code"):
        return exception.exit_code
    return None


def _extract_stderr(exception: Exception) -> Optional[str]:
    """Pull stderr text from a known exception, if present."""
    if isinstance(exception, subprocess.CalledProcessError):
        raw = exception.stderr
        if isinstance(raw, str):
            return raw
        if isinstance(raw, bytes):
            return raw.decode("utf-8", errors="replace")
    if hasattr(exception, "stderr"):
        raw = exception.stderr
        if isinstance(raw, str):
            return raw
        if isinstance(raw, bytes):
            return raw.decode("utf-8", errors="replace")
    return None


# ---------------------------------------------------------------------------
# Public helpers
# ---------------------------------------------------------------------------

def is_transient_failure(
    exception: Exception,
    exit_code: Optional[int] = None,
    stderr: Optional[str] = None,
    config: Optional[RetryConfig] = None,
) -> bool:
    """Return True when *exception* (and optional *exit_code* / *stderr*)
    indicate a transient failure that may succeed on retry.

    Parameters
    ----------
    exception :
        The exception that was raised.
    exit_code :
        Process exit code, if applicable.
    stderr :
        Stderr text captured from the failed process, if available.
    config :
        Retry configuration; uses defaults when ``None``.
    """
    if config is None:
        config = RetryConfig()

    # -- Permanent programming errors ---------------------------------------
    if isinstance(exception, (SyntaxError, ValueError, KeyError)):
        return False

    # -- Check exception type -----------------------------------------------
    if isinstance(exception, config.retryable_exceptions):
        return True

    # -- Check explicit exit code -------------------------------------------
    ec = exit_code if exit_code is not None else _extract_exit_code(exception)
    if ec is not None and ec in config.retryable_exit_codes:
        return True

    # -- Heuristics based on stderr for exit code 1 -------------------------
    s = stderr if stderr is not None else _extract_stderr(exception)
    if ec == 1 and s:
        s_lower = s.lower()
        # GN gen failures
        if "error at" in s_lower and ("gn" in s_lower or "args.gn" in s_lower):
            return False
        # Patch failures
        if "patch failed" in s_lower or "git apply" in s_lower:
            return False
        # Compiler errors (exit code 1 from ninja — NOT signals)
        if "error:" in s_lower and ("ninja" in s_lower or "compilation" in s_lower):
            return False

    return False


def should_retry_aws(exit_code: int, stderr: str) -> bool:
    """AWS CLI / SDK specific heuristics.

    Parameters
    ----------
    exit_code :
        Process exit code (may reflect an HTTP status or CLI error).
    stderr :
        Full stderr output from the command.

    Returns
    -------
    True if the failure is likely transient and should be retried.
    """
    if exit_code in {400, 403, 404}:
        return False

    if exit_code in {429, 500, 502, 503}:
        return True

    transient_keywords = [
        "requesttimeout",
        "slowdown",
        "internalerror",
        "serviceunavailable",
        "connection reset",
        "connection refused",
        "timeout",
        "throttl",
        "too many requests",
    ]
    s_lower = stderr.lower()
    for kw in transient_keywords:
        if kw in s_lower:
            return True

    return False


def should_retry_github(exit_code: int, stderr: str) -> bool:
    """GitHub API specific heuristics.

    Parameters
    ----------
    exit_code :
        Process exit code (may reflect an HTTP status or CLI error).
    stderr :
        Full stderr output from the command.

    Returns
    -------
    True if the failure is likely transient and should be retried.
    """
    if exit_code in {401, 403, 422}:
        return False

    if exit_code in {429, 500, 502, 503}:
        return True

    transient_keywords = [
        "rate limit",
        "abuse detection",
        "server error",
        "service unavailable",
        "connection reset",
        "connection refused",
        "timeout",
        "internal server error",
        "bad gateway",
    ]
    s_lower = stderr.lower()
    for kw in transient_keywords:
        if kw in s_lower:
            return True

    return False


# ---------------------------------------------------------------------------
# Async retry
# ---------------------------------------------------------------------------

async def retry(
    callable,
    *args,
    config: Optional[RetryConfig] = None,
    **kwargs,
) -> Any:
    """Execute *callable* with automatic retry on transient failures.

    Parameters
    ----------
    callable :
        An async callable to execute.
    config :
        Retry configuration; uses ``RetryConfig()`` defaults when ``None``.

    Raises
    ------
    MaxRetriesExceeded
        After all attempts are exhausted.
    PermanentFailure
        Propagated immediately when raised by the callable.
    * Any exception classified as non-transient is re-raised immediately.
    """
    if config is None:
        config = RetryConfig()

    last_error: Optional[Exception] = None

    for attempt in range(1, config.max_attempts + 1):
        try:
            return await callable(*args, **kwargs)
        except PermanentFailure:
            raise
        except Exception as exc:
            last_error = exc
            if not is_transient_failure(exc, config=config):
                raise

            if attempt < config.max_attempts:
                delay = _compute_delay(attempt, config)
                logger.warning(
                    "Attempt %d/%d failed: %s — retrying in %.1fs",
                    attempt,
                    config.max_attempts,
                    exc,
                    delay,
                )
                await asyncio.sleep(delay)
            else:
                logger.error(
                    "Attempt %d/%d failed: %s — no more retries",
                    attempt,
                    config.max_attempts,
                    exc,
                )

    raise MaxRetriesExceeded(
        f"After {config.max_attempts} attempts: {last_error}"
    )


# ---------------------------------------------------------------------------
# RetryContext
# ---------------------------------------------------------------------------

class RetryContext:
    """Context-manager wrapper for per-attempt retry logic and tracking.

    Use inside a caller-managed loop.  The context manager suppresses
    transient exceptions so the loop can continue; permanent exceptions
    and ``MaxRetriesExceeded`` propagate normally.

    Example
    -------
    ::

        ctx = RetryContext(config)
        while ctx.attempts < ctx.config.max_attempts:
            with ctx:
                result = await do_something()
                ctx.record_attempt(result)
                return result
            # Transient failure was suppressed — wait and retry.
            delay = _compute_delay(ctx.attempts, ctx.config)
            await asyncio.sleep(delay)
        raise MaxRetriesExceeded(
            f"After {ctx.config.max_attempts} attempts: {ctx.last_error}"
        )
    """

    def __init__(self, config: Optional[RetryConfig] = None) -> None:
        self.config = config if config is not None else RetryConfig()
        self.attempts: int = 0
        self.last_error: Optional[Exception] = None

    def __enter__(self) -> "RetryContext":
        self.attempts += 1
        logger.debug("RetryContext: attempt %d/%d", self.attempts, self.config.max_attempts)
        return self

    def __exit__(
        self,
        exc_type: Optional[type],
        exc_val: Optional[Exception],
        exc_tb: Optional[object],
    ) -> bool:
        if exc_val is None:
            return False

        self.last_error = exc_val

        if isinstance(exc_val, PermanentFailure):
            return False

        if is_transient_failure(exc_val, config=self.config):
            if self.attempts < self.config.max_attempts:
                logger.warning(
                    "Attempt %d/%d transient failure: %s",
                    self.attempts,
                    self.config.max_attempts,
                    exc_val,
                )
                return True

            logger.error(
                "Attempt %d/%d exhausted: %s",
                self.attempts,
                self.config.max_attempts,
                exc_val,
            )

        return False

    def record_attempt(self, result_or_exception: Any) -> None:
        """Track the outcome of one attempt.

        Parameters
        ----------
        result_or_exception :
            Pass the result on success or the exception instance on failure.
        """
        self.attempts += 1
        if isinstance(result_or_exception, Exception):
            self.last_error = result_or_exception
        else:
            self.last_error = None
