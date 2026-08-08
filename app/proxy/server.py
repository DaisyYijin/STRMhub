"""Emby 302 反代(吸收 embyExternalUrl/MediaWarp 思路)。

设计来源: embyreverseproxy proxy_app.py + emby2Alist emby.js + MediaWarp。

核心链路:
- PlaybackInfo 拦截: 改 Supports 字段/删转码/改 DirectStreamUrl 强制直链,
  并后台预热(strm -> 最终直链)写入缓存;
- stream 请求: 命中预热缓存直接 302 最终直链(秒开), 未命中按需解析;
- final_url 重定向链跟踪: strm 内容是 HTTP 地址时手动跟踪 3xx(10 跳防循环),
  解决"strm 存内网地址、公网播放"问题;
- UA 屏蔽: 命中 PROXY_BLOCK_UA 列表的播放器返回 403;
- 失败一律回源转发(播放成功率优先)。

配置环境变量:
- EMBY_HOST(默认 http://127.0.0.1:8096)、EMBY_API_KEY
- PROXY_BLOCK_UA(逗号分隔的子串, 命中即 403 屏蔽)
"""
from __future__ import annotations

import asyncio
import os
import re
import time
from pathlib import Path
from urllib.parse import parse_qs, urlparse

import httpx
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse, RedirectResponse, Response

# 可被测试替换的 HTTP 客户端
_client: httpx.AsyncClient | None = None

# ---- 预热缓存(item_id|media_source_id -> 最终直链) ----
_link_cache: dict[str, tuple[str, float]] = {}
_LINK_CACHE_MAX = 512
_LINK_TTL = 1800.0  # 30 分钟


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


# ---- 预热缓存 ----

def _cache_get(key: str) -> str | None:
    entry = _link_cache.get(key)
    if entry is None:
        return None
    url, expires = entry
    if expires < time.time():
        _link_cache.pop(key, None)
        return None
    return url


def _cache_put(key: str, url: str) -> None:
    if len(_link_cache) >= _LINK_CACHE_MAX:
        # 简单淘汰: 清掉已过期的, 还不够就清一半最旧的
        now = time.time()
        expired = [k for k, (_, e) in _link_cache.items() if e < now]
        for k in expired:
            _link_cache.pop(k, None)
        if len(_link_cache) >= _LINK_CACHE_MAX:
            for k in list(_link_cache)[: len(_link_cache) // 2]:
                _link_cache.pop(k, None)
    _link_cache[key] = (url, time.time() + _LINK_TTL)


def _cache_clear() -> None:
    _link_cache.clear()


# ---- 解析辅助 ----

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


async def _track_redirects(url: str, depth: int = 0) -> str:
    """手动跟踪 3xx 重定向链(最多 10 跳, 防循环), 返回最终可播 URL。

    embyExternalUrl/MediaWarp 同款思路: strm 里存的是内网地址时,
    由本服务一路跟随到最终外网可达地址再 302 给客户端。
    """
    if depth >= 10:
        return url
    parsed = urlparse(url)
    if parsed.scheme not in ("http", "https"):
        return url
    try:
        resp = await get_client().request("HEAD", url)
    except httpx.HTTPError:
        return url
    if resp.status_code in (301, 302, 303, 307, 308):
        loc = resp.headers.get("location")
        if not loc:
            return url
        next_url = str(httpx.URL(url).join(loc))
        if next_url == url:  # 防循环
            return url
        return await _track_redirects(next_url, depth + 1)
    return url


def _resolve_hub_token(content: str) -> str | None:
    """strm 内容为 hub redirect URL(/api/redirect?key=..&t=..) -> 解析成直链。

    复用 playback 服务的解析+缓存, 避免客户端再跳一次。
    """
    parsed = urlparse(content)
    if "/api/redirect" not in parsed.path:
        return None
    q = parse_qs(parsed.query)
    key = (q.get("key") or [""])[0]
    token = (q.get("t") or q.get("token") or [""])[0]
    if not key or not token:
        return None
    try:
        from ..services.playback import playback
        resolved = playback.resolve_redirect(key, token=token)
    except Exception:
        return None
    return resolved[0] if resolved else None


async def _resolve_media_url(item_id: str, media_source_id: str | None) -> str | None:
    """strm 路径 -> 最终可播 URL(直链/重定向链跟踪), 失败返回 None。"""
    path = await _fetch_media_path(item_id, media_source_id)
    if not path:
        return None
    if path.startswith("http://") or path.startswith("https://"):
        # strm 已是完整直链(或经 hub redirect)
        return await _track_redirects(path)
    content = _read_strm_content(path)
    if not content:
        return None
    content = content.strip()
    parsed = urlparse(content)
    if parsed.scheme in ("http", "https") and "/api/redirect" in parsed.path:
        # hub token 短链: 直接解析成最终直链(比客户端再跳一次更快更稳)
        url = _resolve_hub_token(content)
        if url:
            return url
        # 解析失败(索引未同步等): 回退原地址, 客户端访问时由 /api/redirect 处理
        return await _track_redirects(content)
    if content.startswith(("http://", "https://")):
        return await _track_redirects(content)
    return None


async def _prewarm(item_id: str, media_source_id: str) -> None:
    """后台预热: 解析 strm -> 最终直链并缓存(PlaybackInfo 时触发, 不阻塞响应)。"""
    key = f"{item_id}|{media_source_id or ''}"
    if _cache_get(key):
        return
    try:
        url = await _resolve_media_url(item_id, media_source_id)
        if url:
            _cache_put(key, url)
    except Exception:
        pass


async def _redirect_to_media(item_id: str, media_source_id: str | None) -> Response | None:
    """解析 strm/远程路径 -> 302; 失败返回 None(调用方回源)。

    优先命中预热缓存(秒开), 未命中再按需解析并写缓存。
    """
    key = f"{item_id}|{media_source_id or ''}"
    cached = _cache_get(key)
    if cached:
        return RedirectResponse(cached, status_code=302)
    url = await _resolve_media_url(item_id, media_source_id)
    if not url:
        return None
    _cache_put(key, url)
    return RedirectResponse(url, status_code=302)


async def _handle_playbackinfo(item_id: str, request: Request) -> Response:
    """转发 PlaybackInfo 并改写 MediaSources 强制直链; 触发预热。"""
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
        # 后台预热直链(不阻塞响应, 客户端首次 stream 即命中缓存秒开)
        if msid:
            try:
                asyncio.get_running_loop().create_task(_prewarm(item_id, msid))
            except RuntimeError:
                pass
    return JSONResponse(content=data)


def _ua_blocked(user_agent: str) -> bool:
    """UA 黑白名单: PROXY_BLOCK_UA(逗号分隔子串)命中即屏蔽(403)。"""
    blocks = [b.strip().lower() for b in
              os.environ.get("PROXY_BLOCK_UA", "").split(",") if b.strip()]
    if not blocks:
        return False
    ua = (user_agent or "").lower()
    return any(b in ua for b in blocks)


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
        if _ua_blocked(request.headers.get("user-agent", "")):
            return Response(content=b"blocked by proxy rules",
                            status_code=403)
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
