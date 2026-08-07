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
