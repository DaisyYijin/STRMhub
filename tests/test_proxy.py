"""Emby 302 反代测试: stream 302 / PlaybackInfo 改写 / 回源降级 / 普通转发。

上游 Emby 用 httpx.MockTransport 模拟。
"""
from __future__ import annotations

import json
import os
from pathlib import Path

import httpx
import pytest
from fastapi.testclient import TestClient

from app.proxy import server as proxy_mod
from app.proxy.server import app as proxy_app


@pytest.fixture()
def fake_emby(tmp_path: Path, monkeypatch):
    """注入 mock 上游: Items 查询 + PlaybackInfo + 兜底转发。"""
    strm_dir = tmp_path / "strm"
    strm_dir.mkdir()
    strm_file = strm_dir / "movie.strm"
    strm_file.write_text("http://hub:6060/api/redirect?key=K1&t=T1",
                         encoding="utf-8")

    def handler(request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if path.startswith("/Items") and "Ids=" in str(request.url):
            return httpx.Response(200, json={
                "Items": [{
                    "Id": "1",
                    "MediaSources": [
                        {"Id": "ms1", "Path": str(strm_file), "Container": "mkv"},
                    ],
                }]})
        if path.lower().endswith("/playbackinfo"):
            return httpx.Response(200, json={
                "MediaSources": [{
                    "Id": "ms1", "Container": "mkv",
                    "SupportsDirectPlay": False, "SupportsTranscoding": True,
                    "TranscodingUrl": "/videos/1/master.m3u8",
                    "DirectStreamUrl": "/videos/1/stream.old",
                }]})
        if path.startswith("/System/Info"):
            return httpx.Response(200, json={"ServerName": "FakeEmby"})
        return httpx.Response(404, text="not found")

    transport = httpx.MockTransport(handler)
    proxy_mod.set_client(httpx.AsyncClient(transport=transport))
    monkeypatch.setenv("EMBY_HOST", "http://fake-emby:8096")
    monkeypatch.setenv("EMBY_API_KEY", "testkey")
    yield strm_file
    proxy_mod.set_client(None)


@pytest.fixture()
def client():
    with TestClient(proxy_app) as c:
        yield c


class TestProxy:
    def test_stream_302_to_strm_url(self, client, fake_emby):
        r = client.get("/videos/1/stream.mkv?MediaSourceId=ms1",
                       follow_redirects=False)
        assert r.status_code == 302
        assert r.headers["location"] == "http://hub:6060/api/redirect?key=K1&t=T1"
        assert r.headers.get("referrer-policy") == "no-referrer"

    def test_stream_fallback_when_no_media_source(self, client, fake_emby):
        # Path 缺失 -> 回源转发(兜底), 不应 500
        r = client.get("/videos/999/stream.mp4")
        assert r.status_code in (200, 302, 404)

    def test_playbackinfo_forced_direct_play(self, client, fake_emby):
        r = client.post("/Items/1/PlaybackInfo")
        assert r.status_code == 200
        data = r.json()
        src = data["MediaSources"][0]
        assert src["SupportsDirectPlay"] is True
        assert src["SupportsDirectStream"] is True
        assert src["SupportsTranscoding"] is False
        assert "TranscodingUrl" not in src
        assert src["DirectStreamUrl"].startswith("/videos/1/stream.mkv?")

    def test_plain_forward(self, client, fake_emby):
        r = client.get("/System/Info")
        assert r.status_code == 200
        assert r.json()["ServerName"] == "FakeEmby"

    def test_strm_remote_url_direct_302(self, client, fake_emby, tmp_path: Path):
        """strm 内是完整 http 直链时直接 302(无需 hub redirect)。"""
        remote_strm = tmp_path / "remote.strm"
        remote_strm.write_text("https://cdn.example.com/video.mkv", encoding="utf-8")

        def handler(request: httpx.Request) -> httpx.Response:
            if "Ids=" in str(request.url):
                return httpx.Response(200, json={
                    "Items": [{"Id": "2", "MediaSources": [
                        {"Id": "ms2", "Path": str(remote_strm)}]}]})
            return httpx.Response(404)

        proxy_mod.set_client(httpx.AsyncClient(
            transport=httpx.MockTransport(handler)))
        try:
            r = client.get("/videos/2/stream.mkv?MediaSourceId=ms2",
                           follow_redirects=False)
            assert r.status_code == 302
            assert r.headers["location"] == "https://cdn.example.com/video.mkv"
        finally:
            proxy_mod.set_client(None)

    def test_playbackinfo_upstream_error_passthrough(self, client, fake_emby):
        """上游非 200 时透传响应而不崩溃。"""
        def handler(request: httpx.Request) -> httpx.Response:
            return httpx.Response(500, text="upstream down")

        proxy_mod.set_client(httpx.AsyncClient(
            transport=httpx.MockTransport(handler)))
        try:
            r = client.post("/Items/1/PlaybackInfo")
            assert r.status_code == 500
        finally:
            proxy_mod.set_client(None)

    def test_forward_connect_error_returns_502(self, client, fake_emby, monkeypatch):
        """上游(Emby)连接失败 -> 502 + 可读错误提示(而非 500 traceback)。"""
        import httpx
        from app import proxy
        import app.proxy.server as proxy_mod

        def boom(request: httpx.Request) -> httpx.Response:
            raise httpx.ConnectError("All connection attempts failed")

        proxy_mod.set_client(httpx.AsyncClient(
            transport=httpx.MockTransport(boom)))
        try:
            r = client.get("/System/Info", follow_redirects=False)
            assert r.status_code == 502
            body = r.json()
            assert "无法连接 Emby" in body["error"]
            assert "host.docker.internal" in body["hint"]
        finally:
            proxy_mod.set_client(None)

class TestProxyPrewarm:
    """预热缓存 / 重定向链跟踪 / UA 屏蔽(吸收 embyExternalUrl/MediaWarp 思路)。"""

    def test_prewarm_then_stream_302_from_cache(self, client, fake_emby,
                                                monkeypatch):
        """PlaybackInfo 后台预热 -> stream 直接 302 缓存直链(不再解析)。"""
        from app.services.playback import playback

        monkeypatch.setattr(
            playback, "resolve_redirect",
            lambda key, token="": ("https://cdn.example/v.mp4", ""))
        proxy_mod._cache_clear()

        r = client.post("/items/1/playbackinfo")
        assert r.status_code == 200
        # 预热是后台任务, 给事件循环一点时间
        import time
        for _ in range(20):
            if proxy_mod._cache_get("1|ms1"):
                break
            time.sleep(0.05)
        cached = proxy_mod._cache_get("1|ms1")
        assert cached == "https://cdn.example/v.mp4"
        # stream 请求直接命中缓存
        r = client.get("/videos/1/stream.mkv?MediaSourceId=ms1",
                       follow_redirects=False)
        assert r.status_code == 302
        assert r.headers["location"] == "https://cdn.example/v.mp4"

    def test_final_url_tracking(self, client, fake_emby, monkeypatch):
        """strm 内容为重定向链: 302 前手动跟踪到最终地址(内网地址公网播放)。"""
        strm_file = fake_emby
        strm_file.write_text("http://internal:8080/a/redirect",
                             encoding="utf-8")
        seen = []

        async def handler(request: httpx.Request) -> httpx.Response:
            seen.append(request.url.path)
            if request.url.path.startswith("/Items"):
                return httpx.Response(200, json={
                    "Items": [{
                        "Id": "1",
                        "MediaSources": [
                            {"Id": "ms1", "Path": str(strm_file),
                             "Container": "mkv"},
                        ],
                    }]})
            if request.url.path == "/a/redirect":
                return httpx.Response(302, headers={
                    "location": "https://public-cdn.example/final.mp4"})
            if request.url.path == "/final.mp4":
                return httpx.Response(200, content=b"ok")
            return httpx.Response(404, text="nf")

        proxy_mod.set_client(httpx.AsyncClient(transport=httpx.MockTransport(handler)))
        proxy_mod._cache_clear()
        r = client.get("/videos/1/stream.mkv?MediaSourceId=ms1",
                       follow_redirects=False)
        assert r.status_code == 302
        assert r.headers["location"] == "https://public-cdn.example/final.mp4"
        assert "/a/redirect" in seen  # 反代主动跟踪了重定向链

    def test_ua_blocklist(self, client, fake_emby, monkeypatch):
        """PROXY_BLOCK_UA 命中的播放器被 403 屏蔽。"""
        monkeypatch.setenv("PROXY_BLOCK_UA", "badplayer,leech")
        r = client.get("/videos/1/stream.mkv?MediaSourceId=ms1",
                       headers={"User-Agent": "BadPlayer/1.0"})
        assert r.status_code == 403
        r2 = client.get("/videos/1/stream.mkv?MediaSourceId=ms1",
                        headers={"User-Agent": "EmbyClient/1.0"})
        assert r2.status_code in (302, 404)  # 正常播放器不受影响

    def test_track_redirect_loop_guard(self, monkeypatch):
        """重定向链防循环: A<->B 互相跳转不卡死。"""
        import asyncio

        async def handler(request: httpx.Request) -> httpx.Response:
            if request.url.path == "/a":
                return httpx.Response(302, headers={"location": "/b"})
            return httpx.Response(302, headers={"location": "/a"})

        async def run():
            proxy_mod.set_client(httpx.AsyncClient(
                transport=httpx.MockTransport(handler)))
            return await proxy_mod._track_redirects("http://x/a")

        url = asyncio.run(run())
        assert url == "http://x/b" or url.startswith("http://x/")
