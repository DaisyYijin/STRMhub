"""驱动级配置测试: 未创建账户也能配置规则, 整理时按驱动读取。"""
from __future__ import annotations

import pytest
from fastapi.testclient import TestClient

from app.main import app


@pytest.fixture(scope="module")
def client():
    with TestClient(app) as c:
        yield c


def _login(c):
    return c.post("/api/auth/login",
                  json={"password": "testpass"}).json()["token"]


def test_driver_rules_without_account(client):
    """无账户也能保存/读取驱动级规则。"""
    h = {"Authorization": f"Bearer {_login(client)}"}
    rules = {
        "rename_movie_folder": "{title}-{year}",
        "organize_dirs": {"pending": {"id": "99", "name": "待整理"}},
        "category_yaml": "movie:\n  外语电影:\n",
    }
    r = client.put("/api/drivers/p123/rules", headers=h,
                   json={"rules": rules})
    assert r.status_code == 200, r.text
    got = client.get("/api/drivers/p123/rules", headers=h).json()
    assert got["rules"]["rename_movie_folder"] == "{title}-{year}"
    assert got["rules"]["organize_dirs"]["pending"]["id"] == "99"
    # 驱动间隔离
    other = client.get("/api/drivers/p115/rules", headers=h).json()
    assert other["rules"] == {}


def test_driver_rules_validation(client):
    """非法 YAML / 未知驱动 / 非白名单字段。"""
    h = {"Authorization": f"Bearer {_login(client)}"}
    assert client.put("/api/drivers/xxx/rules", headers=h,
                      json={"rules": {}}).status_code == 404
    r = client.put("/api/drivers/local/rules", headers=h,
                   json={"rules": {"evil": 1, "category_yaml": "movie: ["}})
    assert r.status_code == 400
    assert "evil" not in r.json().get("rules", {})


def test_organize_reads_driver_rules(client, tmp_path):
    """整理执行: 未存账户级规则时, 读取驱动级规则(目录来自驱动配置)。"""
    from pathlib import Path
    pending = tmp_path / "pending"; redundant = tmp_path / "redundant"
    pending.mkdir(); redundant.mkdir()
    (pending / "Iron.Man.2008.1080p.mkv").write_bytes(b"x")
    h = {"Authorization": f"Bearer {_login(client)}"}
    # 驱动级规则: 目录 + 简化模板
    client.put("/api/drivers/local/rules", headers=h, json={"rules": {
        "organize_dirs": {"pending": str(pending),
                          "redundant": str(redundant)},
        "rename_movie_folder": "{title}-{year}",
        "rename_movie_file": "{title}.{year}",
    }})
    acc = client.post("/api/accounts", headers=h, json={
        "name": "drv账户", "driver_type": "local", "credential": ""}).json()
    # 账户级规则留空(驱动级生效)
    r = client.post("/api/organize/run", headers=h,
                    json={"account_id": acc["id"]})
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["counts"]["ok"] == 1, body
    assert (pending / "Iron Man-2008").exists()          # 驱动级模板生效
    assert (pending / "Iron Man-2008" / "Iron Man.2008.mkv").exists()
