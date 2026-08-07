"""FastAPI 入口: 路由装配 + 生命周期。

启动流程: ensure_dirs -> init_db -> 注册路由。
"""
from __future__ import annotations

from contextlib import asynccontextmanager

from fastapi import Depends, FastAPI, Request
from pydantic import BaseModel

from . import config
from . import web
from .api import accounts, auth, automation, organize, playback, qrcode, scrape, tasks
from .db.session import init_db


@asynccontextmanager
async def lifespan(_app: FastAPI):
    config.ensure_dirs()
    init_db()
    yield


app = FastAPI(title="STRMhub", version="0.1.0", lifespan=lifespan)


class LoginIn(BaseModel):
    password: str


@app.get("/api/health")
def health():
    return {"status": "ok", "version": "0.1.0"}


@app.post("/api/auth/login")
def login(body: LoginIn, request: Request):
    return {"token": auth.check_login(body.password, request)}


@app.get("/api/me")
def me(user: str = Depends(auth.require_user)):
    return {"user": user}


# 业务路由
app.include_router(accounts.router)
app.include_router(tasks.router)
app.include_router(playback.router)
app.include_router(scrape.router)
app.include_router(organize.router)
app.include_router(automation.router)
app.include_router(qrcode.router)

# 前端静态托管(最后注册, 不影响 /api)
app.include_router(web.router)
web.mount_static(app)
