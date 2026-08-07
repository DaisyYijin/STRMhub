"""扫码登录 API: 生成二维码 / 轮询状态 / 二维码图片代理。"""
from __future__ import annotations

import httpx
from fastapi import APIRouter, Depends, HTTPException
from fastapi.responses import Response
from pydantic import BaseModel

from ..services.qrcode import qrcode_login
from .auth import require_user

router = APIRouter(prefix="/api/accounts/qrcode", tags=["qrcode"])


class StartIn(BaseModel):
    driver_type: str


class PollIn(BaseModel):
    driver_type: str
    qr_token: str
    qr_uid: str


@router.post("/start")
def start_qrcode(body: StartIn, _: str = Depends(require_user)):
    """生成二维码(需对应网盘 SDK 支持)。"""
    try:
        return qrcode_login.start(body.driver_type)
    except (ValueError, RuntimeError) as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@router.post("/poll")
def poll_qrcode(body: PollIn, _: str = Depends(require_user)):
    """轮询扫码状态; confirmed 时返回 cookies(可直接创建账户)。"""
    try:
        return qrcode_login.poll(body.driver_type, body.qr_token, body.qr_uid)
    except (ValueError, RuntimeError) as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@router.get("/image")
def qrcode_image(url: str, _: str = Depends(require_user)):
    """代理二维码图片(避免 115 防盗链/混合内容限制)。"""
    try:
        resp = httpx.get(url, timeout=15)
    except httpx.HTTPError as exc:
        raise HTTPException(status_code=502, detail=f"图片获取失败: {exc}")
    if resp.status_code != 200:
        raise HTTPException(status_code=502, detail=f"图片获取失败: HTTP {resp.status_code}")
    return Response(content=resp.content, media_type=resp.headers.get(
        "content-type", "image/png"))
