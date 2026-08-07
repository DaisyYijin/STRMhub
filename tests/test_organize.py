"""整理服务测试: 文件名解析/计划生成/预览/执行。"""
from __future__ import annotations

from pathlib import Path

from app.services.organize import OrganizeService, parse_filename


class TestParse:
    def test_movie(self):
        p = parse_filename("Avatar.2009.1080p.BluRay.mkv")
        assert p.title == "Avatar" and p.year == 2009
        assert p.season is None

    def test_tv_episode(self):
        p = parse_filename("Breaking.Bad.S01E02.1080p.WEB-DL.mkv")
        assert p.title == "Breaking Bad"
        assert (p.season, p.episode) == (1, 2)

    def test_cn_episode(self):
        p = parse_filename("某某剧 第2季第3集.mp4")
        assert (p.season, p.episode) == (2, 3)
        assert p.title == "某某剧"

    def test_cleanup_quality_tags(self):
        p = parse_filename("Movie.2024.2160p.HDR.HEVC.mkv")
        assert p.title == "Movie" and p.year == 2024


class TestOrganizeService:
    def test_plan_and_execute(self, tmp_path: Path):
        src = tmp_path / "raw"
        src.mkdir()
        (src / "my.movie.2024.1080p.mkv").write_bytes(b"x")
        (src / "show.S01E01.HDTV.mp4").write_bytes(b"y")
        (src / "already Good (2020).mkv").write_bytes(b"z")

        svc = OrganizeService()
        plan = svc.create_plan(str(src))
        preview = svc.preview(plan)
        assert len(preview) == 2  # 第三个文件名已规范, 不生成动作

        result = svc.execute(plan)
        assert result["done"] == 2 and result["skipped"] == 0

        names = sorted(p.name for p in src.iterdir())
        assert "my movie (2024).mkv" in names
        assert "show (S01E01).mp4" in names

    def test_execute_skips_conflicts(self, tmp_path: Path):
        src = tmp_path / "raw"
        src.mkdir()
        (src / "a.movie.2020.mkv").write_bytes(b"x")
        (src / "a movie (2020).mkv").write_bytes(b"y")  # 目标已存在

        svc = OrganizeService()
        plan = svc.create_plan(str(src))
        result = svc.execute(plan)
        assert result["done"] == 0 and result["skipped"] == 1

    def test_plan_roundtrip(self, tmp_path: Path):
        src = tmp_path / "raw"
        src.mkdir()
        (src / "b.movie.2021.mkv").write_bytes(b"x")
        svc = OrganizeService()
        plan = svc.create_plan(str(src))
        plan2 = svc.load(svc.dump(plan))
        assert plan2.plan_id == plan.plan_id
        assert len(plan2.actions) == len(plan.actions)
