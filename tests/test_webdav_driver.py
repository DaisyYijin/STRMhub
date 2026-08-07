"""WebDAV 驱动测试: PROPFIND 解析/认证/直链/MKCOL/删除。"""
from __future__ import annotations

import base64
import xml.etree.ElementTree as ET

import httpx
import pytest

from app.drivers.webdav.driver import WebDAVDriver


def _propfind_response(*entries):
    """构造 PROPFIND 200 响应体。entries: (href, name, size, is_dir)。"""
    body = ['<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">']
    for href, name, size, is_dir in entries:
        body.append(
            f'<d:response><d:href>{href}</d:href><d:propstat><d:prop>'
            f'<d:displayname>{name}</d:displayname>'
            f'<d:getcontentlength>{size}</d:getcontentlength>'
            f'<d:resourcetype>'
            f'{"<d:collection/>" if is_dir else ""}'
            f'</d:resourcetype>'
            f'</d:prop></d:propstat></d:response>')
    body.append("</d:multistatus>")
    return "".join(body).encode("utf-8")


class TestWebDAVDriver:
    def _driver(self, handler, credential="user:pass", base_url="https://dav.example.com/remote.php/dav/files/u"):
        transport = httpx.MockTransport(handler)
        return WebDAVDriver(
            credential=credential,
            config={"base_url": base_url},
            http=httpx.Client(transport=transport))

    def test_list_files(self):
        def handler(request: httpx.Request) -> httpx.Response:
            assert request.method == "PROPFIND"
            assert request.headers["depth"] == "1"
            assert request.headers["authorization"] == \
                "Basic " + base64.b64encode(b"user:pass").decode()
            return httpx.Response(207, content=_propfind_response(
                ("https://dav.example.com/remote.php/dav/files/u/", "u", 0, True),
                ("https://dav.example.com/remote.php/dav/files/u/movie.mkv", "movie.mkv", 1234, False),
                ("https://dav.example.com/remote.php/dav/files/u/TV Show", "TV Show", 0, True),
            ))

        driver = self._driver(handler)
        items = driver.list_files("")
        assert len(items) == 2
        f = items[0]
        assert f.id == "movie.mkv" and f.name == "movie.mkv" and f.size == 1234
        assert not f.is_dir
        d = items[1]
        assert d.is_dir and d.id == "TV Show"

    def test_list_nested(self):
        def handler(request: httpx.Request) -> httpx.Response:
            return httpx.Response(207, content=_propfind_response(
                ("https://dav.example.com/remote.php/dav/files/u/TV%20Show/", "TV Show", 0, True),
                ("https://dav.example.com/remote.php/dav/files/u/TV%20Show/S01E01.mkv", "S01E01.mkv", 99, False),
            ))

        driver = self._driver(handler)
        items = driver.list_files("TV Show")
        assert len(items) == 1
        assert items[0].id == "TV Show/S01E01.mkv"

    def test_auth_token(self):
        def handler(request: httpx.Request) -> httpx.Response:
            assert request.headers["authorization"] == "Bearer tok123"
            return httpx.Response(207, content=b"<d:multistatus xmlns:d='DAV:'/>")

        driver = self._driver(handler, credential="token:tok123")
        driver.list_files("")

    def test_auth_failure(self):
        def handler(request: httpx.Request) -> httpx.Response:
            return httpx.Response(401, text="unauthorized")

        driver = self._driver(handler)
        with pytest.raises(PermissionError, match="认证失败"):
            driver.list_files("")

    def test_resolve_download(self):
        driver = self._driver(lambda r: httpx.Response(207, content=b""))
        from app.drivers.base import FileItem
        url, _ = driver.resolve_download(FileItem(id="dir/movie.mkv", name="m"))
        assert url == "https://dav.example.com/remote.php/dav/files/u/dir/movie.mkv"

    def test_mkcol_and_delete(self):
        import urllib.parse
        calls = []

        def handler(request: httpx.Request) -> httpx.Response:
            calls.append((request.method, str(request.url)))
            return httpx.Response(201 if request.method == "MKCOL" else 204)

        driver = self._driver(handler)
        new_id = driver.create_folder("", "新建文件夹")
        assert new_id == "新建文件夹"
        decoded_calls = [(m, urllib.parse.unquote(u)) for m, u in calls]
        assert ("MKCOL", "https://dav.example.com/remote.php/dav/files/u/新建文件夹") in decoded_calls

        from app.drivers.base import FileItem
        assert driver.delete_file(FileItem(id="新建文件夹", name="n", is_dir=True)) is True
        decoded_calls = [(m, urllib.parse.unquote(u)) for m, u in calls]
        assert ("DELETE", "https://dav.example.com/remote.php/dav/files/u/新建文件夹") in decoded_calls

    def test_missing_base_url(self):
        with pytest.raises(ValueError, match="base_url"):
            WebDAVDriver(credential="")
