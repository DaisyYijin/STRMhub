"""前端静态托管: 托管 frontend/dist, 根路径返回 index.html。

dist 不存在时(未构建)所有页面路由返回提示, API 不受影响。
"""
from __future__ import annotations

from pathlib import Path

from fastapi import APIRouter
from fastapi.responses import FileResponse, HTMLResponse
from fastapi.staticfiles import StaticFiles

from .config import PROJECT_ROOT

FRONTEND_DIST = PROJECT_ROOT / "frontend" / "dist"
INDEX_HTML = FRONTEND_DIST / "index.html"

router = APIRouter(tags=["web"])

_PLACEHOLDER = """<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="UTF-8"><title>STRMhub</title></head>
<body style="background:#0f172a;color:#e2e8f0;font-family:sans-serif;
display:flex;align-items:center;justify-content:center;height:100vh">
<div style="text-align:center">
<h1>STRMhub</h1>
<p>前端尚未构建。请在 frontend/ 目录执行 <code>npm install && npm run build</code>。</p>
<p>API 文档: <a href="/docs" style="color:#38bdf8">/docs</a></p>
</div></body></html>"""


def has_frontend() -> bool:
    return INDEX_HTML.exists()


@router.get("/", include_in_schema=False)
def index():
    if has_frontend():
        response = FileResponse(INDEX_HTML)
        # 防浏览器缓存旧版 index.html(JS 带 hash, 静态资源不受影响)
        response.headers["Cache-Control"] = "no-cache"
        return response
    return HTMLResponse(_PLACEHOLDER)


def mount_static(app) -> None:
    """挂载 /assets 静态资源(仅当构建产物存在)。"""
    assets = FRONTEND_DIST / "assets"
    if assets.is_dir():
        app.mount("/assets", StaticFiles(directory=assets), name="assets")
