"""驱动层测试: 注册表 + 本地驱动列目录/能力。"""
from __future__ import annotations

from pathlib import Path

import pytest

from app.drivers import registry
from app.drivers.base import FileItem
from app.drivers.registry import create, get_meta, list_drivers, supports


class TestRegistry:
    def test_local_registered(self):
        names = [m.name for m in list_drivers()]
        assert "local" in names
        assert get_meta("local").label == "本地文件系统"

    def test_create_unknown(self):
        with pytest.raises(KeyError):
            create("nope")

    def test_create_local(self):
        driver = create("local", config={"root": "/"})
        assert driver.meta().name == "local"


class TestLocalDriver:
    def test_list_files(self, tmp_path: Path):
        (tmp_path / "a.mp4").write_bytes(b"x")
        (tmp_path / "sub").mkdir()
        (tmp_path / "sub" / "b.mkv").write_bytes(b"y")

        driver = create("local", config={"root": str(tmp_path)})
        items = driver.list_files(str(tmp_path))
        names = {i.name for i in items}
        assert "a.mp4" in names
        assert "sub" in names
        f = next(i for i in items if i.name == "a.mp4")
        assert f.is_file and not f.is_dir and f.size == 1
        assert isinstance(f, FileItem)

    def test_list_missing_dir(self):
        driver = create("local")
        with pytest.raises(FileNotFoundError):
            driver.list_files("Z:/definitely/not/exists")

    def test_capabilities(self):
        driver = create("local")
        assert supports(driver, "download")
        assert supports(driver, "delete")
        assert supports(driver, "mkdir")

    def test_create_folder_and_delete(self, tmp_path: Path):
        driver = create("local")
        new_id = driver.create_folder(str(tmp_path), "newdir")
        assert Path(new_id).is_dir()
        item = FileItem(id=new_id, name="newdir", is_dir=True)
        assert driver.delete_file(item) is True
        assert not Path(new_id).exists()
