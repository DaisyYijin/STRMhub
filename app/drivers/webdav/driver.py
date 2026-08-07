"""WebDAV 网盘驱动。

- 标准协议: PROPFIND(depth=1) 列目录, 无需逆向;
- 认证: Basic(账号:密码) 或 Bearer token(credential 格式 "user:pass" 或 "token:xxx");
- 直链: 返回 base_url + 编码路径的 GET URL(带认证头由调用方透传);
- http client 可注入(测试用 MockTransport 模拟 PROPFIND 响应)。
"""
from __future__ import annotations

import base64
import re
import urllib.parse
import xml.etree.ElementTree as ET
from pathlib import Path

import httpx

from ..base import DriverMeta, FileItem
from ..common import AccountGate

_DAV_NS = "DAV:"
_NS = {
    "d": "DAV:",
    "lp": "http://www.litepan.top/ns",
}


def _propfind_body() -> str:
    return (f'<?xml version="1.0"?>\n<d:propfind xmlns:d="{_DAV_NS}">'
            f"<d:prop><d:resourcetype/><d:getcontentlength/>"
            f"<d:getlastmodified/><d:displayname/></d:prop></d:propfind>")


class WebDAVDriver:
    def __init__(self, credential: str = "", config: dict | None = None,
                 http: httpx.Client | None = None):
        config = config or {}
        self.base_url = str(config.get("base_url") or "").rstrip("/")
        if not self.base_url:
            raise ValueError("WebDAV 驱动需要配置 base_url")
        self.gate = AccountGate(
            max_calls=float(config.get("qps", 5.0)), window=1.0)
        self._auth = self._build_auth(credential)
        self.http = http or httpx.Client(timeout=30)

    def _headers(self, extra: dict | None = None) -> dict:
        headers = dict(extra or {})
        if self._auth:
            headers["Authorization"] = self._auth
        return headers

    @staticmethod
    def _build_auth(credential: str) -> str:
        if credential.startswith("token:"):
            return f"Bearer {credential[6:]}"
        if credential:
            raw = base64.b64encode(credential.encode("utf-8")).decode("ascii")
            return f"Basic {raw}"
        return ""

    def meta(self) -> DriverMeta:
        return DriverMeta(
            name="webdav",
            label="WebDAV",
            auth_type="token",
            capabilities=("download", "mkdir", "delete", "move"),
        )

    # ---- 工具 ----
    def _url(self, parent_id: str) -> str:
        """目录 ID(路径) -> 请求 URL。"""
        if parent_id in ("", "/"):
            return self.base_url + "/"
        path = parent_id if parent_id.startswith("/") else "/" + parent_id
        return self.base_url + path

    def _href_to_id(self, href: str, parent_id: str = "") -> str:
        """PROPFIND href -> 相对 base_url 的路径 ID(已含完整前缀, 勿再拼 parent)。"""
        parsed = urllib.parse.urlparse(href)
        path = urllib.parse.unquote(parsed.path)
        base_path = urllib.parse.urlparse(self.base_url).path
        if base_path and path.startswith(base_path):
            path = path[len(base_path):]
        return path.strip("/")

    # ---- Driver ----
    def list_files(self, parent_id: str) -> list[FileItem]:
        self.gate.wait()
        resp = self.http.request(
            "PROPFIND", self._url(parent_id),
            headers=self._headers({"Depth": "1",
                                   "Content-Type": "application/xml"}),
            content=_propfind_body())
        if resp.status_code in (401, 403):
            raise PermissionError(f"WebDAV 认证失败: HTTP {resp.status_code}")
        if resp.status_code >= 400:
            raise RuntimeError(f"WebDAV PROPFIND 失败: HTTP {resp.status_code}")
        root = ET.fromstring(resp.content)
        items: list[FileItem] = []
        for resp_el in root.findall(f"{{{_DAV_NS}}}response"):
            href = resp_el.findtext(f"{{{_DAV_NS}}}href") or ""
            # 跳过自身: href 是 URL 编码的, 与本地 URL 统一解码比较
            if not href or urllib.parse.unquote(href).rstrip("/") == \
                    self._url(parent_id).rstrip("/"):
                continue
            props = resp_el.find(f"{{{_DAV_NS}}}propstat/{{{_DAV_NS}}}prop")
            if props is None:
                continue
            res_type = props.find(f"{{{_DAV_NS}}}resourcetype")
            is_dir = res_type is not None and \
                res_type.find(f"{{{_DAV_NS}}}collection") is not None
            name = props.findtext(f"{{{_DAV_NS}}}displayname") or \
                urllib.parse.unquote(href.rstrip("/").rsplit("/", 1)[-1])
            size = props.findtext(f"{{{_DAV_NS}}}getcontentlength") or "0"
            items.append(FileItem(
                id=self._href_to_id(href, parent_id),
                name=name or "?",
                size=int(float(size or 0)),
                is_dir=is_dir,
            ))
        return items

    def resolve_download(self, item: FileItem) -> tuple[str, dict]:
        """WebDAV 直链: base_url + 编码路径, 认证头由调用方透传。"""
        path = item.id if item.id.startswith("/") else "/" + item.id
        return self.base_url + path, {}

    def create_folder(self, parent_id: str, name: str) -> str:
        self.gate.wait()
        parent = parent_id.strip("/")
        new_id = f"{parent}/{name}" if parent else name
        resp = self.http.request("MKCOL", self._url(new_id),
                                 headers=self._headers())
        if resp.status_code >= 400:
            raise RuntimeError(f"MKCOL 失败: HTTP {resp.status_code}")
        return new_id

    def delete_file(self, item: FileItem) -> bool:
        self.gate.wait()
        resp = self.http.request("DELETE", self._url(item.id),
                                 headers=self._headers())
        return resp.status_code < 400


def _factory(credential: str = "", config: dict | None = None) -> WebDAVDriver:
    return WebDAVDriver(credential=credential, config=config)


def register() -> None:
    from ..registry import register as _register

    _register(_factory, DriverMeta(
        name="webdav",
        label="WebDAV",
        auth_type="token",
        capabilities=("download", "mkdir", "delete", "move"),
    ))
