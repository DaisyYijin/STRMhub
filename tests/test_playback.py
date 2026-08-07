"""播放链路测试: /api/redirect 302 + 缓存 + 同目录预缓存。"""
from __future__ import annotations

import re
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from app.main import app
from app.services.playback import playback


@pytest.fixture(scope="module")
def client():
    with TestClient(app) as c:
        yield c


def _login(client):
    r = client.post("/api/auth/login", json={"password": "testpass"})
    assert r.status_code == 200
    return r.json()["token"]


def _auth(token):
    return {"Authorization": f"Bearer {token}"}


def _setup_task(client, tmp_path: Path, files: list[str]) -> dict:
    """创建账户+任务+运行, 返回 (task, src, out)。"""
    token = _login(client)
    src = tmp_path / "media"
    src.mkdir()
    for name in files:
        (src / name).write_bytes(b"x")
    out = tmp_path / "strm"

    acc = client.post("/api/accounts", headers=_auth(token), json={
        "name": f"acc_{tmp_path.name}", "driver_type": "local",
    }).json()
    task = client.post("/api/tasks", headers=_auth(token), json={
        "account_id": acc["id"], "name": "playback task",
        "remote_path": str(src), "local_output": str(out),
        "base_url": "http://hub:6060",
    }).json()
    res = client.post(f"/api/tasks/{task['id']}/run", headers=_auth(token))
    assert res.status_code == 200, res.text
    return {"token": token, "task": task, "src": src, "out": out}


def _parse_strm_url(strm_path: Path) -> tuple[str, str]:
    """从 strm 内容解析 (key, t)。"""
    url = strm_path.read_text(encoding="utf-8").strip()
    m = re.search(r"key=([^&]+)&t=([^&\s]+)", url)
    assert m, f"无法解析 strm URL: {url}"
    return m.group(1), m.group(2)


class TestRedirect:
    def test_full_chain_302(self, client, tmp_path: Path):
        setup = _setup_task(client, tmp_path, ["a.mp4"])
        strm_file = setup["out"] / "a.strm"
        assert strm_file.exists()
        key, t = _parse_strm_url(strm_file)

        r = client.get("/api/redirect", params={"key": key, "t": t},
                     follow_redirects=False)
        assert r.status_code == 302, r.text
        # 本地驱动直链 = 源文件 file:// URI
        assert r.headers["location"] == (setup["src"] / "a.mp4").as_uri()
        assert r.headers.get("referrer-policy") == "no-referrer"
        assert "max-age" in r.headers.get("cache-control", "")

    def test_unknown_key_404(self, client):
        r = client.get("/api/redirect", params={"key": "bm90LWV4aXN0", "t": "x"})
        assert r.status_code == 404

    def test_missing_token_404(self, client, tmp_path: Path):
        setup = _setup_task(client, tmp_path, ["b.mp4"])
        key, _t = _parse_strm_url(setup["out"] / "b.strm")
        r = client.get("/api/redirect", params={"key": key})
        assert r.status_code == 404

    def test_bad_key_404(self, client):
        r = client.get("/api/redirect", params={"key": "!!!not-base64!!!", "t": "x"})
        assert r.status_code == 404

    def test_cache_hit(self, client, tmp_path: Path):
        setup = _setup_task(client, tmp_path, ["c.mp4"])
        key, t = _parse_strm_url(setup["out"] / "c.strm")
        r1 = client.get("/api/redirect", params={"key": key, "t": t},
                         follow_redirects=False)
        assert r1.status_code == 302
        assert key in playback.cache  # 已入缓存
        r2 = client.get("/api/redirect", params={"key": key, "t": t},
                         follow_redirects=False)
        assert r2.status_code == 302
        assert r2.headers["location"] == r1.headers["location"]

    def test_sibling_precache(self, client, tmp_path: Path):
        """同目录预缓存: 命中一个后, 同目录其余文件直链被预热。"""
        setup = _setup_task(client, tmp_path, ["e01.mp4", "e02.mp4", "e03.mp4"])
        key1, t = _parse_strm_url(setup["out"] / "e01.strm")
        playback.cache.clear()
        r = client.get("/api/redirect", params={"key": key1, "t": t},
                       follow_redirects=False)
        assert r.status_code == 302
        # e02/e03 已被预缓存(本地驱动直链=源路径)
        assert key1 in playback.cache
        cached_keys = set(playback.cache.keys())
        assert len(cached_keys) >= 3, f"预缓存未生效: {cached_keys}"
