"""扫码登录 API: 生成二维码(SVG data URI) / 轮询状态(新 API: uid/time/sign + app)。"""
from __future__ import annotations

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel

from ..services.qrcode import qrcode_login
from .auth import require_user

router = APIRouter(prefix="/api/accounts/qrcode", tags=["qrcode"])


class StartIn(BaseModel):
    driver_type: str


class PollIn(BaseModel):
    driver_type: str
    uid: str
    time: str
    sign: str
    app: str = "web"


@router.post("/start")
def start_qrcode(body: StartIn, _: str = Depends(require_user)):
    """生成二维码(需对应网盘 SDK 支持)。

    返回 {driver_type, uid, time, sign, qr_image(SVG data URI), apps(设备列表)}。
    """
    try:
        return qrcode_login.start(body.driver_type)
    except (ValueError, RuntimeError) as exc:
        raise HTTPException(status_code=400, detail=str(exc))
    except Exception as exc:  # 未知异常: 返回具体信息便于排查
        import traceback
        traceback.print_exc()
        raise HTTPException(status_code=500, detail=f"二维码生成失败: {exc}")


@router.post("/poll")
def poll_qrcode(body: PollIn, _: str = Depends(require_user)):
    """轮询扫码状态; confirmed 时返回 cookies(可直接创建账户)。"""
    try:
        return qrcode_login.poll(body.driver_type, body.uid, body.time,
                                 body.sign, body.app)
    except (ValueError, RuntimeError) as exc:
        raise HTTPException(status_code=400, detail=str(exc))
    except Exception as exc:
        import traceback
        traceback.print_exc()
        raise HTTPException(status_code=500, detail=f"轮询失败: {exc}")
