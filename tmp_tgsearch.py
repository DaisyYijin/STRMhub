import re

# ========== ① tgsearch.go：删 4 个 Web handler（保留引擎与配置读写供订阅用） ==========
p = 'internal/api/tgsearch.go'
s = open(p, encoding='utf-8').read()

def remove_fn(s, recv, name):
    m = re.search(r'func ' + re.escape(recv) + re.escape(name) + r'\([^)]*\) \{[\s\S]*?\n\}\n', s)
    if not m:
        # 匹配不到带接收者的，试纯函数
        m = re.search(r'func ' + name + r'\([^)]*\) \{[\s\S]*?\n\}\n', s)
        if not m:
            raise AssertionError('fn not found: ' + name)
    return s[:m.start()] + s[m.end():]

# 文件头注释也要改（第 3 行附近描述）
for fn in ['TgSearchGetConfig', 'TgSearchSaveConfig', 'TgSearchSearch', 'TgSearchSave']:
    s = remove_fn(s, '(h *Handler) ', fn)
    print('removed handler', fn)

# 文件头注释更新
s = re.sub(r'^// ={3,}.*$', lambda m: m.group(0) if 'tgsearch' in m.group(0).lower() else m.group(0), s, flags=re.M)
s = re.sub(r'(// ==+ \n)// [^\n]*(TG|tg|频道搜索|搜索资源|转存)[^\n]*\n', r'\1', s, count=4)
open(p, 'w', encoding='utf-8', newline='\n').write(s)
print('tgsearch.go handlers removed')

# ========== ② routes.go：删 4 条路由 ==========
p = 'internal/api/routes.go'
s = open(p, encoding='utf-8').read()
old = '''		// TG 频道搜索（t.me/s 公开预览抓取 → 网盘链接提取 → 转存）
		protected.GET("/tgsearch/config", h.TgSearchGetConfig)
		protected.POST("/tgsearch/config", h.TgSearchSaveConfig)
		protected.GET("/tgsearch/search", h.TgSearchSearch)
		protected.POST("/tgsearch/save", h.TgSearchSave)
'''
assert old in s, 'routes block'
s = s.replace(old, '', 1)
open(p, 'w', encoding='utf-8', newline='\n').write(s)
print('routes ok')

# ========== ③ index.html：删扩展卡片 + 弹窗 ==========
p = 'web/index.html'
s = open(p, encoding='utf-8').read()

# 卡片（注释 → 下一个卡片注释/容器尾）
si = s.index('<!-- 卡片：TG 频道搜索（可用） -->')
# 卡片结束 = 下一个 '<!-- 卡片：' 或同层结构；找卡片 div 配平
depth = 0
j = s.index('<div class="rule-card', si)
i0 = j
k = j
while k < len(s):
    m = re.match(r'<div\b|</div>', s[k:])
    if m:
        if m.group(0).startswith('<div'):
            depth += 1
        else:
            depth -= 1
            if depth == 0:
                break
        k += m.end()
    else:
        k += 1
end = s.index('</div>', k + 6) + 6  # rule-footer 内层闭合后的外层 </div>
# 简化：直接找下一个 '<!-- 卡片：' 注释为界
nxt = s.find('<!-- 卡片：', si + 10)
if nxt == -1:
    nxt = s.find('      </div>', si + 10)
s = s[:si] + s[nxt:]
print('extension card removed')

# 弹窗（tgsearch-modal 整块）
si = s.index('<div class="modal-mask" id="tgsearch-modal"')
# 找该 modal 配平：从 si 起第一个 modal-mask 结构是 mask>modal>…，用 id 下一处 '</div>\n</div>' 不好定位
# 改为：找下一个顶层注释 '<!--' 在 si 之后
nxt = s.find('\n    <!--', si)
assert nxt > 0
s = s[:si] + s[nxt+1:]
open(p, 'w', encoding='utf-8', newline='\n').write(s)
print('modal removed')
