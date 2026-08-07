"""整理 API: 计划-预览-执行 + 三目录整理(账户)。"""
from __future__ import annotations

import logging

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel

from ..services.organize import OrganizeService, organize
from .auth import require_user

_log = logging.getLogger("strmhub.api")


router = APIRouter(prefix="/api/organize", tags=["organize"])
_organize = OrganizeService()


class RunIn(BaseModel):
    account_id: int


class RenderIn(BaseModel):
    template: str
    sample: str = "movie_file"  # movie_folder|movie_file|tv_folder|season_folder|episode_file


# 示例文件名(供模板实时预览)
_SAMPLE_NAMES = {
    "movie_folder": "蜘蛛侠.2016.2160p.BluRay.x265.10bit.HDR.TrueHD.7.1-TnT.mkv",
    "movie_file": "蜘蛛侠.2016.2160p.BluRay.x265.10bit.HDR.TrueHD.7.1-TnT.mkv",
    "tv_folder": "权力的游戏.2011.S01E01.1080p.WEB-DL.x264-TnT.mkv",
    "season_folder": "权力的游戏.2011.S01E01.1080p.WEB-DL.x264-TnT.mkv",
    "episode_file": "权力的游戏.2011.S01E01.1080p.WEB-DL.x264-TnT.mkv",
}


@router.post("/render")
def render_template_preview(body: RenderIn, _: str = Depends(require_user)):
    """用示例数据渲染命名模板(实时预览)。"""
    from ..services.organize import build_context, parse_filename, render_template
    name = _SAMPLE_NAMES.get(body.sample, _SAMPLE_NAMES["movie_file"])
    parsed = parse_filename(name)
    if parsed is None:
        raise HTTPException(status_code=400, detail="示例解析失败")
    ctx = build_context(parsed, name)
    ctx["tmdb_id"] = "1726"
    return {"rendered": render_template(body.template, ctx), "sample": name}


@router.post("/run")
def run_organize(body: RunIn, _: str = Depends(require_user)):
    """三目录整理: 扫描账户的等待整理目录 -> 识别 -> 分类移动。

    目录与重命名模板取该账户已保存的规则(organize_dirs / rename_*)。
    """
    try:
        return organize.run(body.account_id)
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))
    except Exception as exc:
        import traceback
        _log.exception("请求处理异常")
        raise HTTPException(status_code=500, detail=f"整理失败: {exc}")


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
