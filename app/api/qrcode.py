"""扫码登录 API: 生成二维码(SVG data URI) / 轮询状态(新 API: uid/time/sign + app)。

扫码确认后自动创建账户并拉取账号信息(昵称/容量/头像等), 无需前端手动建户。
"""
from __future__ import annotations

import secrets

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel

from ..services.account import AccountService
from ..services.qrcode import qrcode_login
from .auth import require_user

router = APIRouter(prefix="/api/accounts/qrcode", tags=["qrcode"])
_accounts = AccountService()


class StartIn(BaseModel):
    driver_type: str


class PollIn(BaseModel):
    driver_type: str
    uid: str
    time: str
    sign: str
    app: str = "web"


def _auto_upsert_account(driver_type: str, cookies: str, app: str = "web") -> dict:
    """扫码确认后: 拉取账号信息并自动建户/更新(单账号模式), 返回账户 dict。"""
    info = qrcode_login.fetch_account_info(driver_type, cookies)
    info["device"] = app  # 登录设备(扫码时选择)
    nickname = (info.get("nickname") or "").strip()
    name = nickname or f"{driver_type}-{secrets.token_hex(3)}"
    try:
        acc, created = _accounts.upsert(driver_type, name, cookies, info)
    except ValueError:  # 名字重复(昵称撞车): 加随机后缀
        name = f"{name}-{secrets.token_hex(3)}"
        acc, created = _accounts.upsert(driver_type, name, cookies, info)
    result = _accounts.to_dict(acc)
    result["action"] = "created" if created else "updated"
    return result


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
    """轮询扫码状态; confirmed 时自动创建账户并返回 account(含账号信息)。"""
    try:
        result = qrcode_login.poll(body.driver_type, body.uid, body.time,
                                   body.sign, body.app)
    except (ValueError, RuntimeError) as exc:
        raise HTTPException(status_code=400, detail=str(exc))
    except Exception as exc:
        import traceback
        traceback.print_exc()
        raise HTTPException(status_code=500, detail=f"轮询失败: {exc}")
    if result["status"] == "confirmed":
        try:
            result["account"] = _auto_upsert_account(
                body.driver_type, result["cookies"], body.app)
        except Exception as exc:
            import traceback
            traceback.print_exc()
            result["status"] = "error"
            result["error"] = f"自动创建账户失败: {exc}"
    return result
