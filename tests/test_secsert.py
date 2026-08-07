"""秒传 JSON 解析器测试: 四前缀/base62/md5 归一化/错误处理。"""
from __future__ import annotations

from app.services.transfer.secsert import (
    decode_base62_etag,
    normalize_md5,
    parse_secsert,
)

# 已知值: "abc" 的 md5 = 900150983cd24fb0d6963f7d28e17f72
MD5_ABC_HEX = "900150983cd24fb0d6963f7d28e17f72"
MD5_ABC_B64 = "kAFQmDzST7DWlj99KOF/cg=="


class TestNormalizeMd5:
    def test_hex_lowercase(self):
        assert normalize_md5(MD5_ABC_HEX.upper()) == MD5_ABC_HEX

    def test_base64_to_hex(self):
        assert normalize_md5(MD5_ABC_B64) == MD5_ABC_HEX

    def test_padding_variants(self):
        # 无 padding 的 base64
        assert normalize_md5(MD5_ABC_B64.rstrip("=")) == MD5_ABC_HEX

    def test_garbage(self):
        assert normalize_md5("zzz") == "zzz"


class TestBase62:
    def test_roundtrip(self):
        # 0 -> 0000...; 62 -> 10
        assert decode_base62_etag("0") == "0" * 31 + "0"
        assert decode_base62_etag("10") == f"{62:032x}"

    def test_unknown_chars_unchanged(self):
        assert decode_base62_etag("-") == "-"


class TestParseSecsert:
    def test_v1_line(self):
        text = ("123FSLinkV1$\n"
                "Movie.mkv|1048576|900150983cd24fb0d6963f7d28e17f72\n"
                "Sub.srt|100|kAFQmDzST7DWlj99KOF/cg==\n")
        bundle = parse_secsert(text)
        assert bundle.format == "123FSLinkV1$"
        assert len(bundle.files) == 2
        assert bundle.files[0].name == "Movie.mkv"
        assert bundle.files[0].size == 1048576
        # base64 编码的 md5 被归一化为 hex
        assert bundle.files[1].etag == "900150983cd24fb0d6963f7d28e17f72"

    def test_v2_line_base62(self):
        text = "123FSLinkV2$\nA.mkv|10|10\n"  # base62 "10" = 62
        bundle = parse_secsert(text)
        assert bundle.files[0].etag == f"{62:032x}"

    def test_json_v1(self):
        text = ('123FLCPV1${"commonPath":"/电影","files":['
                '{"name":"a.mkv","size":100,"etag":"' + MD5_ABC_B64 + '"},'
                '{"name":"sub/b.srt","size":5,"etag":"' + MD5_ABC_HEX + '",'
                '"isDir":false}]}')
        bundle = parse_secsert(text)
        assert bundle.format == "123FLCPV1$"
        assert bundle.common_path == "/电影"
        assert len(bundle.files) == 2
        assert bundle.files[0].etag == MD5_ABC_HEX  # base64 已归一化
        assert bundle.files[1].name == "sub/b.srt"
        assert bundle.total_size == 105

    def test_json_v2_base62(self):
        text = ('123FLCPV2${"commonPath":"","usesBase62EtagsInExport":true,'
                '"files":[{"name":"x.mkv","size":1,"etag":"10"}]}')
        bundle = parse_secsert(text)
        assert bundle.files[0].etag == f"{62:032x}"

    def test_json_v2_implies_base62(self):
        # V2 前缀即使不声明 usesBase62EtagsInExport 也按 base62
        text = ('123FLCPV2${"commonPath":"","files":'
                '[{"name":"x.mkv","size":1,"etag":"10"}]}')
        bundle = parse_secsert(text)
        assert bundle.files[0].etag == f"{62:032x}"

    def test_bare_json_fallback(self):
        text = '{"commonPath":"","files":[{"name":"f.mkv","size":2,"etag":"' + \
               MD5_ABC_HEX + '"}]}'
        bundle = parse_secsert(text)
        assert len(bundle.files) == 1

    def test_empty_and_garbage(self):
        assert parse_secsert("").error == "空输入"
        assert parse_secsert("hello world").error == "无法识别秒传格式"
        assert parse_secsert('123FLCPV1${bad json').error == "JSON 解析失败"
