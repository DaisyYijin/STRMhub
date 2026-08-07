"""刮削 API: 触发刮削 + 海报墙查询。"""
from __future__ import annotations

import uuid

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel

from ..services.scrape.service import ScrapeService
from .auth import require_user

router = APIRouter(prefix="/api/scrape", tags=["scrape"])
_scrape = ScrapeService()


class ScrapeIn(BaseModel):
    strm_dir: str


@router.post("/run")
def run_scrape(body: ScrapeIn, _: str = Depends(require_user)):
    """刮削 strm 目录(需配置 TMDB_API_KEY 环境变量; 无 key 时仅建索引)。"""
    task_id = uuid.uuid4().hex[:12]
    result = _scrape.run(body.strm_dir, task_id)
    return {
        "task_id": task_id,
        "groups": result.groups,
        "matched": result.matched,
        "doubt": result.doubt,
        "none": result.none,
        "posters": result.posters,
        "error": result.error,
    }


@router.get("/items")
def list_items(task_id: str, _: str = Depends(require_user)):
    return _scrape.list_items(task_id)
