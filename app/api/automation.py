"""Webhook 联动 API: 规则 CRUD + 免鉴权触发端点。"""
from __future__ import annotations

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel

from ..services.automation import automation
from .auth import require_user

router = APIRouter(prefix="/api/automation", tags=["automation"])


class RuleIn(BaseModel):
    name: str
    trigger: str = "webhook"
    action_chain: list[str] = []
    token: str = ""


@router.get("/rules")
def list_rules(_: str = Depends(require_user)):
    return automation.list_rules()


@router.post("/rules")
def create_rule(body: RuleIn, _: str = Depends(require_user)):
    try:
        rule = automation.create_rule(body.name, body.trigger,
                                      body.action_chain, body.token)
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))
    return automation.to_dict(rule)


@router.delete("/rules/{rule_id}")
def delete_rule(rule_id: int, _: str = Depends(require_user)):
    if not automation.delete_rule(rule_id):
        raise HTTPException(status_code=404, detail="规则不存在")
    return {"ok": True}


@router.post("/webhook/{token}")
async def webhook_trigger(token: str, body: dict | None = None):
    """免鉴权触发端点(供 QAS/CloudSaver 等转存工具回调)。

    body 支持 {delayTime: 秒} 延迟执行。
    """
    try:
        return automation.trigger(token, body)
    except KeyError as exc:
        raise HTTPException(status_code=404, detail=str(exc))
