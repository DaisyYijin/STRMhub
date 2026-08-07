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


class TestRenderTemplate:
    def test_basic_vars(self):
        from app.services.organize import render_template
        ctx = {"title": "钢铁侠", "year": 2008, "pix": "2160p"}
        assert render_template("{title}.{year}.{pix}", ctx) == "钢铁侠.2008.2160p"

    def test_empty_var_removed(self):
        from app.services.organize import render_template
        ctx = {"title": "钢铁侠", "year": 2008}
        assert render_template("{title}.{year}.{missing}", ctx) == "钢铁侠.2008."

    def test_block_syntax(self):
        """<...> 块: 变量非空输出整块, 为空整块消失。"""
        from app.services.organize import render_template
        tpl = "{title}.{year}<.{pix}><.{fps}>"
        assert render_template(tpl, {"title": "A", "year": 2020, "pix": "1080p"}) == "A.2020.1080p"
        assert render_template(tpl, {"title": "A", "year": 2020}) == "A.2020"

    def test_format_spec(self):
        from app.services.organize import render_template
        assert render_template("Season {season_num:02d}",
                               {"season_num": 1}) == "Season 01"

    def test_brackets_escape(self):
        from app.services.organize import render_template
        ctx = {"tmdb_id": "1726"}
        assert render_template("[tmdb=[[tmdb_id]]]", ctx) == "[tmdb=1726]"

    def test_movie_filename_template(self):
        """用户给出的电影文件命名规则。"""
        from app.services.organize import render_template, DEFAULT_TEMPLATES
        ctx = {"title": "钢铁侠", "year": 2008, "resource_pix": "2160p",
               "resource_type": "BluRay", "resource_effect": "DV.HDR",
               "video_encode": "HEVC", "audio_encode": "TrueHD",
               "resource_team": "TnT"}
        out = render_template(DEFAULT_TEMPLATES["movie_file"], ctx)
        assert out == "钢铁侠.2008.2160p.BluRay.DV.HDR.HEVC.TrueHD-TnT"
        # 缺省字段的块消失
        out2 = render_template(DEFAULT_TEMPLATES["movie_file"],
                               {"title": "A", "year": 2020})
        assert out2 == "A.2020"

    def test_folder_template(self):
        from app.services.organize import render_template, DEFAULT_TEMPLATES
        ctx = {"first_letter": "G", "title": "钢铁侠", "year": 2008,
               "tmdb_id": "1726"}
        assert render_template(DEFAULT_TEMPLATES["movie_folder"], ctx) \
            == "G-钢铁侠-2008-[tmdb=1726]"


class TestOrganizeRun:
    """三目录整理流程(本地驱动): 成功/已存在/冗余分类。"""

    def _make_account_with_rules(self, root, rules):
        from app.main import app
        from fastapi.testclient import TestClient
        with TestClient(app) as c:
            token = c.post("/api/auth/login",
                           json={"password": "testpass"}).json()["token"]
            h = {"Authorization": f"Bearer {token}"}
            acc = c.post("/api/accounts", headers=h, json={
                "name": f"org-{root.name}", "driver_type": "local",
                "credential": "", "config": {"root": "/"}}).json()
            c.put(f"/api/accounts/{acc['id']}/rules", headers=h,
                  json={"rules": rules})
            return acc, h

    def test_three_dirs_flow(self, tmp_path: Path):
        pending = tmp_path / "pending"; existing = tmp_path / "existing"
        redundant = tmp_path / "redundant"
        for d in (pending, existing, redundant):
            d.mkdir()
        (pending / "Iron.Man.2008.2160p.BluRay.mkv").write_bytes(b"x")
        (pending / "garbage.xyz").write_bytes(b"x")   # 非视频 -> 冗余
        # 已存在目标(与模板输出一致: 英文标题首字母 I + tmdb 空)
        (pending / "I-Iron Man-2008-[tmdb=]").mkdir()
        (pending / "Duplicate.Movie.2020.1080p.mkv").write_bytes(b"x")

        from app.services.organize import organize
        acc, _ = self._make_account_with_rules(tmp_path, {
            "organize_dirs": {"pending": str(pending),
                              "existing": str(existing),
                              "redundant": str(redundant)},
            "rename_movie_folder": "{first_letter}-{title}-{year}-[tmdb=[[tmdb_id]]]",
        })
        result = organize.run(acc["id"])
        counts = result["counts"]
        # 1 个识别成功(Duplicate Movie), 1 个已存在(Iron Man), 1 个冗余(非视频)
        assert counts["ok"] == 1, result
        assert counts["existing"] == 1, result
        assert counts["redundant"] == 1, result
        # 冗余文件被移走
        assert not (pending / "garbage.xyz").exists()
        assert (redundant / "garbage.xyz").exists()
        # 已存在文件移入 existing
        assert (existing / "Iron.Man.2008.2160p.BluRay.mkv").exists()
        # 成功文件建目录并移入
        assert (pending / "D-Duplicate Movie-2020-[tmdb=]").exists()
        assert list((pending / "D-Duplicate Movie-2020-[tmdb=]").iterdir())

    def test_run_requires_pending_dir(self, tmp_path: Path):
        from app.services.organize import organize
        import pytest as _pytest
        acc, _ = self._make_account_with_rules(tmp_path, {})
        with _pytest.raises(ValueError, match="等待整理"):
            organize.run(acc["id"])
