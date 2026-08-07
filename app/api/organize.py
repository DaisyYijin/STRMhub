"""整理 API: 计划-预览-执行。"""
from __future__ import annotations

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel

from ..services.organize import OrganizeService
from .auth import require_user

router = APIRouter(prefix="/api/organize", tags=["organize"])
_organize = OrganizeService()


class PlanIn(BaseModel):
    path: str


class ExecuteIn(BaseModel):
    plan_json: str


@router.post("/plan")
def create_plan(body: PlanIn, _: str = Depends(require_user)):
    try:
        plan = _organize.create_plan(body.path)
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))
    return {
        "plan_id": plan.plan_id,
        "preview": _organize.preview(plan),
        "plan_json": _organize.dump(plan),
    }


@router.post("/execute")
def execute_plan(body: ExecuteIn, _: str = Depends(require_user)):
    try:
        plan = _organize.load(body.plan_json)
    except (ValueError, KeyError):
        raise HTTPException(status_code=400, detail="计划 JSON 无效")
    return _organize.execute(plan)
