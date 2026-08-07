"""播放 API: /api/redirect 302 直链端点(STRM 文件内 URL 指向此处, 免鉴权)。"""
from __future__ import annotations

from fastapi import APIRouter, HTTPException
from fastapi.responses import RedirectResponse

from ..services.playback import playback

router = APIRouter(tags=["playback"])


@router.get("/api/redirect")
def redirect_play(key: str, t: str = ""):
    """解析 STRM key -> 302 到网盘直链。

    安全: token 必须非空且 key 必须存在于 FileIndex(防随意调用);
    URL 签名(防猜解)留待后续版本。
    """
    resolved = playback.resolve_redirect(key, token=t)
    if resolved is None:
        raise HTTPException(status_code=404, detail="资源不存在或凭据无效")
    url, _disposition = resolved
    response = RedirectResponse(url, status_code=302)
    response.headers["Cache-Control"] = "max-age=600"
    response.headers["Referrer-Policy"] = "no-referrer"
    return response
