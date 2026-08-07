"""API 全链路测试: 登录/鉴权/账户 CRUD/任务 CRUD/触发生成。"""
from __future__ import annotations

from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from app.main import app


@pytest.fixture(scope="module")
def client():
    with TestClient(app) as c:  # with 触发 startup(init_db)
        yield c


def _login(client, password="testpass"):
    r = client.post("/api/auth/login", json={"password": password})
    assert r.status_code == 200, r.text
    return r.json()["token"]


def _auth(token):
    return {"Authorization": f"Bearer {token}"}


class TestAuth:
    def test_health(self, client):
        assert client.get("/api/health").json()["status"] == "ok"

    def test_login_wrong_password(self, client):
        r = client.post("/api/auth/login", json={"password": "wrong"})
        assert r.status_code == 401

    def test_login_ok(self, client):
        assert client.post("/api/auth/login",
                           json={"password": "testpass"}).status_code == 200

    def test_protected_requires_token(self, client):
        assert client.get("/api/accounts").status_code == 401
        assert client.get("/api/tasks").status_code == 401

    def test_me(self, client):
        token = _login(client)
        r = client.get("/api/me", headers=_auth(token))
        assert r.status_code == 200
        assert r.json()["user"] == "admin"


class TestAccountApi:
    def test_drivers_list(self, client):
        token = _login(client)
        r = client.get("/api/accounts/drivers", headers=_auth(token))
        names = [d["name"] for d in r.json()]
        assert "local" in names

    def test_create_list_delete(self, client):
        token = _login(client)
        r = client.post("/api/accounts", headers=_auth(token), json={
            "name": "本机媒体",
            "driver_type": "local",
            "config": {"root": "C:/"},
        })
        assert r.status_code == 200, r.text
        acc = r.json()
        assert acc["name"] == "本机媒体"
        assert "credential" not in acc and "credential_enc" not in acc  # 脱敏

        lst = client.get("/api/accounts", headers=_auth(token)).json()
        assert any(a["id"] == acc["id"] for a in lst)

        r = client.delete(f"/api/accounts/{acc['id']}", headers=_auth(token))
        assert r.status_code == 200

    def test_duplicate_name(self, client):
        token = _login(client)
        body = {"name": "dup", "driver_type": "local"}
        assert client.post("/api/accounts", headers=_auth(token),
                           json=body).status_code == 200
        r = client.post("/api/accounts", headers=_auth(token), json=body)
        assert r.status_code == 400


class TestTaskApi:
    def test_full_pipeline(self, client, tmp_path: Path):
        token = _login(client)

        src = tmp_path / "media"
        src.mkdir()
        (src / "hello.mkv").write_bytes(b"h")
        out = tmp_path / "strm"

        # 账户
        acc = client.post("/api/accounts", headers=_auth(token), json={
            "name": f"src_{tmp_path.name}", "driver_type": "local",
        }).json()

        # 任务
        r = client.post("/api/tasks", headers=_auth(token), json={
            "account_id": acc["id"],
            "name": "test task",
            "remote_path": str(src),
            "local_output": str(out),
            "scan_mode": "incremental_missing",
            "base_url": "http://hub:6060",
        })
        assert r.status_code == 200, r.text
        task = r.json()
        assert task["token"].startswith("lpk_strm_")

        # 执行
        r = client.post(f"/api/tasks/{task['id']}/run", headers=_auth(token))
        assert r.status_code == 200, r.text
        res = r.json()
        assert res["generated"] == 1 and res["written"] == 1 and res["error"] == ""

        # 产物 + 任务状态落库
        assert (out / "hello.strm").exists()
        tasks = client.get("/api/tasks", headers=_auth(token)).json()
        t = next(t for t in tasks if t["id"] == task["id"])
        assert t["status"] == "done"

        # 删除任务
        assert client.delete(f"/api/tasks/{task['id']}",
                             headers=_auth(token)).status_code == 200

    def test_run_missing_task(self, client):
        token = _login(client)
        assert client.post("/api/tasks/9999/run",
                           headers=_auth(token)).status_code == 404
