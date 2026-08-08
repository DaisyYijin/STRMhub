"""115 生活事件监控测试(MP 插件思路: 事件推送式增量)。

mock iter_life_behavior_once + fs_info + fs_files,
验证: 上传文件事件生成 strm / 删除事件移除 strm / 目录事件递归 / 游标持久化。
"""
from __future__ import annotations

import json
import uuid

import pytest
from fastapi.testclient import TestClient

from app.main import app


class FakeLifeClient:
    """模拟 p115client: fs_info(路径反查) + fs_files(目录列表)。"""

    # cid -> (name, parent_id); file 也存(文件名/id)
    NODES = {
        0: ("", 0),
        100: ("电影", 0),          # 监控根目录 remote_path
        101: ("复仇者联盟", 100),   # 子目录
        200: ("a.mkv", 101),       # 文件(id=pick_code)
        201: ("b.mp4", 100),       # 文件
        300: ("旧片.mkv", 100),     # 待删除文件
    }

    def fs_info(self, payload, **kw):
        fid = payload.get("file_id") if isinstance(payload, dict) else payload
        name, pid = self.NODES.get(int(fid), ("未知", 0))
        return {"data": {"file_id": fid, "file_name": name, "parent_id": pid}}

    def fs_files(self, payload, **kw):
        cid = payload.get("cid")
        if cid == 101:
            return {"data": {"data": [
                {"fid": "200", "n": "a.mkv", "s": 1024, "pick_code": "200"},
            ]}}
        if cid == 100:
            return {"data": {"data": [
                {"cid": "101", "n": "复仇者联盟"},
                {"fid": "201", "n": "b.mp4", "s": 2048, "pick_code": "201"},
                {"fid": "300", "n": "旧片.mkv", "s": 512, "pick_code": "300"},
            ]}}
        return {"data": {"data": []}}


def _ev(eid, etype, file_id, name, parent=0, cat=1):
    return {"id": eid, "update_time": eid, "type": etype, "file_id": file_id,
            "file_name": name, "parent_id": parent, "file_category": cat,
            "pick_code": file_id}


@pytest.fixture()
def env(tmp_path, monkeypatch):
    """p115 账户 + 任务 + 注入 fake 客户端。"""
    from app.services import life_event as life_mod
    out = tmp_path / "strm"
    out.mkdir()
    monkeypatch.setattr(
        life_mod.LifeEventMonitor, "_client",
        lambda self, account: FakeLifeClient())
    with TestClient(app) as c:
        h = {"Authorization": "Bearer " + c.post(
            "/api/auth/login", json={"password": "testpass"}).json()["token"]}
        acc = c.post("/api/accounts", headers=h, json={
            "name": f"life账户-{uuid.uuid4().hex[:6]}", "driver_type": "p115",
            "credential": "UID=1; CID=2"}).json()
        task = c.post("/api/tasks", headers=h, json={
            "account_id": acc["id"], "name": "电影任务",
            "remote_path": "100", "local_output": str(out),
            "scan_mode": "incremental_missing",
            "extensions": [".mkv", ".mp4"],
            "base_url": "http://hub:6060", "token": "tok1"}).json()
        yield c, h, out, task, acc


def test_upload_file_event_generates_strm(env, monkeypatch):
    """上传文件事件 -> 生成 strm + FileIndex + 游标推进。"""
    c, h, out, task, acc = env
    from app.services import life_event as life_mod
    from app.services.life_event import LifeEventMonitor

    events = [_ev(1, 2, "201", "b.mp4", parent=100)]
    monkeypatch.setattr(life_mod, "_life_iter",
                        lambda *a, **kw: iter(events))
    m = LifeEventMonitor(task["id"])
    stats = m.once()
    assert stats["created"] == 1
    assert (out / "b.mp4.strm").exists(), "strm 未生成"
    content = (out / "b.mp4.strm").read_text(encoding="utf-8")
    assert "tok1" in content and "key" in content
    # 游标持久化
    got = c.get(f"/api/tasks/{task['id']}/life", headers=h).json()
    assert got["from_id"] == 1
    assert got["processed"] == 1


def test_delete_event_removes_strm(env, monkeypatch):
    """删除事件 -> 移除 strm + 索引。"""
    c, h, out, task, acc = env
    from app.services import life_event as life_mod
    from app.services.life_event import LifeEventMonitor

    # 先造一个已存在的 strm(手动写 + 索引由上传事件建立)
    events = [_ev(1, 2, "300", "旧片.mkv", parent=100)]
    monkeypatch.setattr(life_mod, "_life_iter",
                        lambda *a, **kw: iter(events))
    LifeEventMonitor(task["id"]).once()
    assert (out / "旧片.mkv.strm").exists()
    # 删除事件
    events = [_ev(2, 22, "300", "旧片.mkv", parent=100)]
    monkeypatch.setattr(life_mod, "_life_iter",
                        lambda *a, **kw: iter(events))
    stats = LifeEventMonitor(task["id"]).once()
    assert stats["removed"] == 1
    assert not (out / "旧片.mkv.strm").exists(), "strm 未删除"


def test_folder_event_recurses(env, monkeypatch):
    """新建目录事件 -> 递归生成目录下视频 strm。"""
    c, h, out, task, acc = env
    from app.services import life_event as life_mod
    from app.services.life_event import LifeEventMonitor

    events = [_ev(1, 17, "101", "复仇者联盟", parent=100, cat=0)]
    monkeypatch.setattr(life_mod, "_life_iter",
                        lambda *a, **kw: iter(events))
    stats = LifeEventMonitor(task["id"]).once()
    assert stats["created"] == 1
    assert (out / "复仇者联盟" / "a.mkv.strm").exists()


def test_life_api_switch(env):
    """开关 API: 开启/关闭 + 状态查询。"""
    c, h, out, task, acc = env
    r = c.put(f"/api/tasks/{task['id']}/life", headers=h,
              json={"monitor_life": True, "interval": 5})
    assert r.status_code == 200, r.text
    got = c.get(f"/api/tasks/{task['id']}/life", headers=h).json()
    assert got["monitor_life"] is True
    assert got["interval"] == 5
    c.put(f"/api/tasks/{task['id']}/life", headers=h,
          json={"monitor_life": False})
    got = c.get(f"/api/tasks/{task['id']}/life", headers=h).json()
    assert got["monitor_life"] is False
