"""测试基座: 在任何 app 模块导入前, 将数据目录隔离到临时目录。

注意: config.data_dir()/secret_key()/admin_password() 均为动态读取,
因此设置环境变量即可隔离, 无需重置模块缓存。
"""
from __future__ import annotations

import os
import tempfile

_TEST_DATA = tempfile.mkdtemp(prefix="strmhub_test_")
os.environ["STRMHUB_DATA"] = _TEST_DATA
os.environ["STRMHUB_ADMIN_PASSWORD"] = "testpass"
