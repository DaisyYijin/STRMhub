"""Emby/Jellyfin 302 反向代理(监听 PROXY_PORT=6086)。

设计来源: embyreverseproxy proxy_app.py + emby2Alist emby.js ——
- 双层拦截: PlaybackInfo 诱导客户端选直链(改 Supports 字段/删转码/改 DirectStreamUrl),
  stream 请求真正 302 到网盘直链;
- 失败一律回源转发(播放成功率优先);
- strm 文件由本服务读取内容 -> 提取 URL -> 302。

配置: 环境变量 EMBY_HOST(默认 http://127.0.0.1:8096)、EMBY_API_KEY。
"""
from __future__ import annotations

import os
import re
from pathlib import Path

import httpx
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse, RedirectResponse, Response

# 可被测试替换的 HTTP 客户端
_client: httpx.AsyncClient | None = None


def get_client() -> httpx.AsyncClient:
    global _client
    if _client is None:
        _client = httpx.AsyncClient(follow_redirects=False, timeout=30)
    return _client


def set_client(client: httpx.AsyncClient | None) -> None:
    """测试注入(mock 上游)。"""
    global _client
    _client = client


def emby_host() -> str:
    return os.environ.get("EMBY_HOST", "http://127.0.0.1:8096").rstrip("/")


def emby_api_key() -> str:
    return os.environ.get("EMBY_API_KEY", "")


app = FastAPI(title="STRMhub Emby 302 反代")

_VIDEO_STREAM_RE = re.compile(
    r"^/(?:emby/)?videos/([^/]+)/(stream(?:\.[^/?#]*)?|original)(?:\?|$)")
_PLAYBACKINFO_RE = re.compile(
    r"^/(?:emby/)?items/([^/]+)/playbackinfo(?:\?|$)", re.IGNORECASE)


def _read_strm_content(path: str) -> str | None:
    """读取 .strm 文件首行(文件可能不存在/非 strm)。"""
    p = Path(path)
    if not p.exists() or not p.is_file():
        return None
    try:
        text = p.read_text(encoding="utf-8", errors="ignore")
    except OSError:
        return None
    for line in text.splitlines():
        line = line.strip().strip('"').strip("'")
        if line:
            return line
    return None


async def _fetch_media_path(item_id: str, media_source_id: str | None) -> str | None:
    """通过 Emby API 获取媒体文件的 Path(配合 api_key)。"""
    url = (f"{emby_host()}/Items?Ids={item_id}&Fields=Path,MediaSources"
           f"&Limit=1&api_key={emby_api_key()}")
    resp = await get_client().get(url)
    if resp.status_code != 200:
        return None
    data = resp.json()
    items = data.get("Items") or []
    if not items:
        return None
    sources = (items[0].get("MediaSources") or [])
    if media_source_id:
        for s in sources:
            if s.get("Id") == media_source_id and s.get("Path"):
                return s["Path"]
    return sources[0].get("Path") if sources else None


async def _redirect_to_media(item_id: str, media_source_id: str | None) -> Response | None:
    """解析 strm/远程路径 -> 302; 失败返回 None(调用方回源)。"""
    path = await _fetch_media_path(item_id, media_source_id)
    if not path:
        return None
    if path.startswith("http://") or path.startswith("https://"):
        # strm 内已是完整直链(或经 hub redirect)
        return RedirectResponse(path, status_code=302)
    content = _read_strm_content(path)
    if not content:
        return None
    return RedirectResponse(content, status_code=302)


async def _handle_playbackinfo(item_id: str, request: Request) -> Response:
    """转发 PlaybackInfo 并改写 MediaSources 强制直链。"""
    url = emby_host() + request.url.path + ("?" + request.url.query if request.url.query else "")
    headers = {k: v for k, v in request.headers.items() if k.lower() not in ("host", "content-length")}
    resp = await get_client().post(url, headers=headers,
                                   content=await request.body())
    if resp.status_code != 200:
        return Response(content=resp.content, status_code=resp.status_code,
                        media_type=resp.headers.get("content-type"))
    try:
        data = resp.json()
    except ValueError:
        return Response(content=resp.content, status_code=resp.status_code,
                        media_type=resp.headers.get("content-type"))
    key = emby_api_key()
    for src in data.get("MediaSources") or []:
        src["SupportsDirectPlay"] = True
        src["SupportsDirectStream"] = True
        src["SupportsTranscoding"] = False
        src.pop("TranscodingUrl", None)
        src.pop("TranscodingSubProtocol", None)
        src.pop("TranscodingContainer", None)
        container = (src.get("Container") or "mp4").lower()
        msid = src.get("Id") or ""
        src["DirectStreamUrl"] = (
            f"/videos/{item_id}/stream.{container}"
            f"?MediaSourceId={msid}&Static=true&api_key={key}")
    return JSONResponse(content=data)


async def _forward(request: Request) -> Response:
    """原样转发上游(回源/兜底)。媒体流通常已被 302 接管, 此处仅兜底。"""
    url = emby_host() + request.url.path + ("?" + request.url.query if request.url.query else "")
    headers = {k: v for k, v in request.headers.items()
               if k.lower() not in ("host", "content-length", "accept-encoding")}
    try:
        resp = await get_client().request(request.method, url, headers=headers,
                                          content=await request.body())
    except httpx.ConnectError as exc:
        # 常见原因: EMBY_HOST 配了 127.0.0.1(容器内指容器自身)或 Emby 未启动
        import json as _json
        return Response(
            content=_json.dumps({
                "error": "无法连接 Emby 服务器",
                "detail": f"{exc}",
                "hint": "请检查 EMBY_HOST 配置: Emby 在宿主机时应使用 "
                        "http://host.docker.internal:8096(而非 127.0.0.1)",
            }, ensure_ascii=False).encode(),
            status_code=502, media_type="application/json")
    except httpx.HTTPError as exc:
        return Response(
            content=str(exc).encode(), status_code=502,
            media_type="text/plain")
    return Response(
        content=resp.content,
        status_code=resp.status_code,
        headers={k: v for k, v in resp.headers.items()
                 if k.lower() not in ("content-length", "content-encoding")},
    )


@app.api_route("/{path:path}", methods=["GET", "POST", "HEAD", "OPTIONS"])
async def proxy_route(path: str, request: Request):
    m = _VIDEO_STREAM_RE.match("/" + path)
    if m:
        item_id, _name = m.group(1), m.group(2)
        msid = request.query_params.get("MediaSourceId")
        redirected = await _redirect_to_media(item_id, msid)
        if redirected is not None:
            redirected.headers["Referrer-Policy"] = "no-referrer"
            return redirected
        return await _forward(request)  # 回源降级

    m = _PLAYBACKINFO_RE.match("/" + path)
    if m:
        return await _handle_playbackinfo(m.group(1), request)

    return await _forward(request)
