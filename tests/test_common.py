"""公共工具测试: 限流器频率/阻塞/重置, 封控特征识别。"""
from __future__ import annotations

import threading
import time

import pytest

from app.drivers.common import AccountGate, RateLimiter, is_blocked_response


class TestRateLimiter:
    def test_allows_up_to_max(self):
        limiter = RateLimiter(max_calls=3, window=1.0)
        t0 = time.monotonic()
        for _ in range(3):
            limiter.wait()
        # 3 次内不应阻塞
        assert time.monotonic() - t0 < 0.5

    def test_exceeding_blocks(self):
        limiter = RateLimiter(max_calls=2, window=0.3)
        for _ in range(2):
            limiter.wait()
        t0 = time.monotonic()
        limiter.wait()  # 应阻塞约 0.3s
        assert time.monotonic() - t0 >= 0.25

    def test_thread_safety(self):
        limiter = RateLimiter(max_calls=5, window=0.5)
        errors = []

        def worker():
            try:
                for _ in range(5):
                    limiter.wait()
            except Exception as exc:  # pragma: no cover
                errors.append(exc)

        threads = [threading.Thread(target=worker) for _ in range(4)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        assert not errors

    def test_reset(self):
        limiter = RateLimiter(max_calls=1, window=10)
        limiter.wait()
        t0 = time.monotonic()
        limiter.wait()  # 阻塞
        assert time.monotonic() - t0 >= 9
        limiter.reset()
        t1 = time.monotonic()
        limiter.wait()  # 重置后立即放行
        assert time.monotonic() - t1 < 0.5


class TestBlockedRecognition:
    def test_markers(self):
        assert is_blocked_response("您的访问被阻断, 请稍后再试")
        assert is_blocked_response("<html><body>potential threats</body></html>")
        assert is_blocked_response("操作频繁, 请验证")
        assert is_blocked_response("<!DOCTYPE html>")
        assert not is_blocked_response('{"data": [], "state": true}')
        assert not is_blocked_response("")


class TestAccountGate:
    def test_cooldown(self):
        gate = AccountGate(max_calls=10, window=0.1, cooldown=0.3)
        gate.wait()
        assert not gate.is_blocked()
        gate.report_blocked()
        assert gate.is_blocked()
        t0 = time.monotonic()
        gate.wait()  # 冷却期等待
        assert time.monotonic() - t0 >= 0.25
        assert not gate.is_blocked()
