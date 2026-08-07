"""日志缓冲与日志 API 测试。"""
from __future__ import annotations

import logging

import pytest
from fastapi.testclient import TestClient

from app.main import app
from app.services.logbuf import RingBufferHandler, install


@pytest.fixture(scope="module")
def client():
    with TestClient(app) as c:
        yield c


def test_ring_buffer_capacity():
    h = RingBufferHandler(capacity=10)
    fmt = logging.Formatter("%(message)s")
    h.setFormatter(fmt)
    for i in range(15):
        rec = logging.LogRecord("t", logging.INFO, __file__, 1,
                                f"line-{i}", (), None)
        h.emit(rec)
    snap = h.snapshot()
    assert len(snap) == 10
    assert snap[0]["msg"] == "line-5"   # 环形淘汰最旧
    assert snap[-1]["msg"] == "line-14"
    assert len(h.snapshot(3)) == 3      # tail


def test_logs_api_returns_lines(client):
    install()
    logging.getLogger("strmhub.test").warning("测试日志行 你好")
    token = client.post("/api/auth/login",
                        json={"password": "testpass"}).json()["token"]
    h = {"Authorization": f"Bearer {token}"}
    r = client.get("/api/logs?tail=50", headers=h)
    assert r.status_code == 200
    lines = r.json()["lines"]
    assert any("测试日志行 你好" in ln["msg"] for ln in lines)
    assert all(ln["ts"] and ln["level"] and ln["msg"] for ln in lines)


def test_logs_api_requires_auth(client):
    assert client.get("/api/logs").status_code == 401
