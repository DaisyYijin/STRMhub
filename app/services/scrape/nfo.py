"""nfo/海报写入。

设计来源: LitePan internal/strmscrape/nfo.go —— 电影同名.nfo + poster.jpg;
剧集 tvshow.nfo + poster.jpg + seasonXX-poster.jpg。
"""
from __future__ import annotations

from pathlib import Path
from xml.sax.saxutils import escape


def _movie_nfo(title: str, year: int | None, tmdb_id: int,
               plot: str = "", original_title: str = "") -> str:
    return f"""<?xml version="1.0" encoding="utf-8"?>
<movie>
  <title>{escape(title)}</title>
  <originaltitle>{escape(original_title or title)}</originaltitle>
  <year>{year or ''}</year>
  <tmdbid>{tmdb_id}</tmdbid>
  <plot>{escape(plot)}</plot>
</movie>
"""


def _tvshow_nfo(title: str, year: int | None, tmdb_id: int,
                plot: str = "", original_title: str = "") -> str:
    return f"""<?xml version="1.0" encoding="utf-8"?>
<tvshow>
  <title>{escape(title)}</title>
  <originaltitle>{escape(original_title or title)}</originaltitle>
  <year>{year or ''}</year>
  <tmdbid>{tmdb_id}</tmdbid>
  <plot>{escape(plot)}</plot>
  <episodeguide>
    <url cache="tmdb">https://www.themoviedb.org/tv/{tmdb_id}</url>
  </episodeguide>
</tvshow>
"""


def write_movie_nfo(movie_dir: Path, title: str, year: int | None,
                    tmdb_id: int, plot: str = "",
                    original_title: str = "") -> Path:
    """电影: 与第一个 .strm 同名的 .nfo(无同名时写 movie.nfo)。"""
    strm_files = sorted(movie_dir.glob(f"*{'.strm'}"))
    name = strm_files[0].name[: -len(".strm")] if strm_files else "movie"
    nfo = movie_dir / f"{name}.nfo"
    nfo.write_text(_movie_nfo(title, year, tmdb_id, plot, original_title),
                   encoding="utf-8")
    return nfo


def write_tvshow_nfo(tv_dir: Path, title: str, year: int | None,
                     tmdb_id: int, plot: str = "",
                     original_title: str = "") -> Path:
    nfo = tv_dir / "tvshow.nfo"
    nfo.write_text(_tvshow_nfo(title, year, tmdb_id, plot, original_title),
                   encoding="utf-8")
    return nfo


def _season_nfo(season_num: int, title: str = "", plot: str = "",
                 premiered: str = "") -> str:
    return f"""<?xml version="1.0" encoding="utf-8"?>
<season>
  <title>{escape(title)}</title>
  <seasonnumber>{season_num}</seasonnumber>
  <plot>{escape(plot)}</plot>
  <premiered>{escape(premiered)}</premiered>
</season>
"""


def _episode_nfo(title: str, show_title: str, plot: str = "",
                 aired: str = "", tmdb_id: int = 0,
                 season: int = 0, episode: int = 0) -> str:
    return f"""<?xml version="1.0" encoding="utf-8"?>
<episodedetails>
  <title>{escape(title)}</title>
  <showtitle>{escape(show_title)}</showtitle>
  <season>{season}</season>
  <episode>{episode}</episode>
  <plot>{escape(plot)}</plot>
  <aired>{escape(aired)}</aired>
  <tmdbid>{tmdb_id}</tmdbid>
</episodedetails>
"""


def write_season_nfo(season_dir: Path, season_num: int, title: str = "",
                     plot: str = "", premiered: str = "") -> Path:
    """季: <季目录>/season.nfo(LitePan 同款结构)。"""
    season_dir.mkdir(parents=True, exist_ok=True)
    nfo = season_dir / "season.nfo"
    nfo.write_text(_season_nfo(season_num, title, plot, premiered),
                   encoding="utf-8")
    return nfo


def write_episode_nfo(strm_path: Path, title: str, show_title: str,
                      plot: str = "", aired: str = "", tmdb_id: int = 0,
                      season: int = 0, episode: int = 0) -> Path:
    """集: 与 .strm 同名的 .nfo(Emby 扫库识别)。"""
    nfo = strm_path.with_suffix(".nfo")
    nfo.write_text(_episode_nfo(title, show_title, plot, aired, tmdb_id,
                                season, episode), encoding="utf-8")
    return nfo


def write_thumb(strm_path: Path, still_path: str | None, downloader) -> Path | None:
    """集缩略图: 与 .strm 同名 -thumb.jpg。"""
    if not still_path:
        return None
    dest = strm_path.with_name(strm_path.stem + "-thumb.jpg")
    if downloader(still_path, dest):
        return dest
    return None


def write_poster(target_dir: Path, poster_path: str | None, downloader) -> Path | None:
    """下载海报(同名-poster.jpg 电影 / poster.jpg 剧集由调用方指定文件名)。"""
    if not poster_path:
        return None
    dest = target_dir / "poster.jpg"
    if downloader(poster_path, dest):
        return dest
    return None
