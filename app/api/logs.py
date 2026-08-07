"""日志 API: 轮询读取 + SSE 实时流(右上角日志面板用)。"""
from __future__ import annotations

import asyncio
import json

from fastapi import APIRouter, Depends, Query, Request
from fastapi.responses import StreamingResponse

from ..services import logbuf
from .auth import require_user, verify_token

router = APIRouter(prefix="/api/logs", tags=["logs"])


@router.get("")
def get_logs(tail: int = Query(200, ge=1, le=1000),
             _: str = Depends(require_user)):
    """返回最近 tail 条日志。"""
    return {"lines": logbuf.get_logs(tail)}


@router.get("/stream")
async def stream_logs(request: Request, token: str = ""):
    """SSE 实时日志流(EventSource 无法带 header, 用 query token 鉴权)。"""
    if not verify_token(token):
        return StreamingResponse(iter(["data: {\"error\":\"unauthorized\"}\n\n"]),
                                 media_type="text/event-stream")

    async def gen():
        last = 0
        lines = logbuf.get_logs()
        for line in lines[last:]:
            yield f"data: {json.dumps(line, ensure_ascii=False)}\n\n"
        last = len(lines)
        while True:
            if await request.is_disconnected():
                break
            await asyncio.sleep(1)
            lines = logbuf.get_logs()
            if len(lines) > last:
                for line in lines[last:]:
                    yield f"data: {json.dumps(line, ensure_ascii=False)}\n\n"
                last = len(lines)
            else:
                yield ": keepalive\n\n"  # 心跳, 防代理超时

    return StreamingResponse(gen(), media_type="text/event-stream",
                             headers={"Cache-Control": "no-cache",
                                      "X-Accel-Buffering": "no"})
