"""扫码登录 API: 生成二维码(SVG data URI) / 轮询状态。

- 115: 新 API(uid/time/sign + app), confirmed 后自动建户并拉取账号信息。
- 123: uniID 轮询(loginStatus), confirmed 后自动建户(token)。
"""
from __future__ import annotations

import logging

import secrets

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel

from ..services.account import AccountService
from ..services.qrcode import p123_qrcode_login, qrcode_login
from .auth import require_user

_log = logging.getLogger("strmhub.api")


router = APIRouter(prefix="/api/accounts/qrcode", tags=["qrcode"])
_accounts = AccountService()


class StartIn(BaseModel):
    driver_type: str


class PollIn(BaseModel):
    driver_type: str = "p115"
    uid: str = ""
    time: str = ""
    sign: str = ""
    app: str = "web"
    uni_id: str = ""


def _auto_upsert_account(driver_type: str, cookies: str, app: str = "web") -> dict:
    """扫码确认后: 拉取账号信息并自动建户/更新(单账号模式), 返回账户 dict。"""
    info = qrcode_login.fetch_account_info(driver_type, cookies)
    if info:
        info["device"] = qrcode_login._app_label(app)  # 登录设备中文名(如 微信小程序)
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
    """生成二维码(需对应网盘 SDK 支持)。"""
    try:
        if body.driver_type == "p123":
            return p123_qrcode_login.start()
        return qrcode_login.start(body.driver_type)
    except (ValueError, RuntimeError) as exc:
        raise HTTPException(status_code=400, detail=str(exc))
    except Exception as exc:  # 未知异常: 返回具体信息便于排查
        _log.exception("请求处理异常")
        raise HTTPException(status_code=500, detail=f"二维码生成失败: {exc}")


@router.post("/poll")
def poll_qrcode(body: PollIn, _: str = Depends(require_user)):
    """轮询扫码状态; confirmed 时自动创建账户并返回 account(含账号信息)。"""
    try:
        if body.driver_type == "p123":
            result = p123_qrcode_login.poll(body.uni_id)
        else:
            result = qrcode_login.poll(body.driver_type, body.uid, body.time,
                                       body.sign, body.app)
    except (ValueError, RuntimeError) as exc:
        raise HTTPException(status_code=400, detail=str(exc))
    except Exception as exc:
        _log.exception("请求处理异常")
        raise HTTPException(status_code=500, detail=f"轮询失败: {exc}")
    if result["status"] == "confirmed":
        try:
            result["account"] = _auto_upsert_account(
                body.driver_type, result["cookies"], body.app)
        except Exception as exc:
            _log.exception("请求处理异常")
            result["status"] = "error"
            result["error"] = f"自动创建账户失败: {exc}"
    return result
