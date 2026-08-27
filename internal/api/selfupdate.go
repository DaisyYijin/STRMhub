package api

// ==================== 应用内自更新（Docker 部署） ====================
//
// 依赖 compose 挂载 Docker socket：
//   volumes:
//     - /var/run/docker.sock:/var/run/docker.sock
// 流程：前端比对 GitHub 最新提交 → 展示两个版本间的提交（更新内容）
//   → 拉取当前容器镜像引用的最新版 → 用完全相同的配置重建并启动容器。
// 未挂载 socket 时接口返回配置指引，不做任何危险动作。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const dockerSockPath = "/var/run/docker.sock"

func dockerHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.DialTimeout("unix", dockerSockPath, 3*time.Second)
			},
		},
		Timeout: timeout,
	}
}

// fetchLatestSHA 查询 GitHub main 最新提交（15 秒内去重防狂刷，force 时跳过；失败返回上次已知值）
func fetchLatestSHA(force bool) string {
	latestVersionCache.Lock()
	cached, cacheAt := latestVersionCache.sha, latestVersionCache.at
	latestVersionCache.Unlock()
	if !force && cached != "" && time.Since(cacheAt) < 15*time.Second {
		return cached
	}
	client := &http.Client{Timeout: 5 * time.Second}
	if pu := getProxyURL(); pu != "" {
		if p, err := parseProxyURL(pu); err == nil {
			client.Transport = &http.Transport{Proxy: p}
		}
	}
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/DaisyYijin/STRMhub/commits/main", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return cached
	}
	defer resp.Body.Close()
	var out struct {
		Sha string `json:"sha"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) == nil && out.Sha != "" {
		latestVersionCache.Lock()
		latestVersionCache.sha, latestVersionCache.at = out.Sha, time.Now()
		latestVersionCache.Unlock()
		return out.Sha
	}
	return cached
}

// VersionChanges GET /version/changes —— 当前版本与最新版本之间的提交列表（更新内容）
func (h *Handler) VersionChanges(c *gin.Context) {
	current := buildVersion
	latest := fetchLatestSHA(true)
	if current == "dev" || latest == "" || strings.HasPrefix(latest, current[:7]) {
		c.JSON(http.StatusOK, gin.H{"current": current, "latest": latest, "commits": nil,
			"uptodate": current != "dev" && latest != "" && strings.HasPrefix(latest, current[:7])})
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	if pu := getProxyURL(); pu != "" {
		if p, err := parseProxyURL(pu); err == nil {
			client.Transport = &http.Transport{Proxy: p}
		}
	}
	u := "https://api.github.com/repos/DaisyYijin/STRMhub/compare/" + current + "..." + latest
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"current": current, "latest": latest, "commits": nil, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	var cmp struct {
		Commits []struct {
			Sha string `json:"sha"`
			Commit struct {
				Message string `json:"message"`
				Author  struct {
					Date string `json:"date"`
				} `json:"author"`
			} `json:"commit"`
		} `json:"commits"`
	}
	if json.NewDecoder(resp.Body).Decode(&cmp) != nil {
		c.JSON(http.StatusOK, gin.H{"current": current, "latest": latest, "commits": nil})
		return
	}
	type commitItem struct {
		Sha     string `json:"sha"`
		Message string `json:"message"`
		Date    string `json:"date"`
	}
	commits := []commitItem{}
	// 最新在最上
	for i := len(cmp.Commits) - 1; i >= 0; i-- {
		cm := cmp.Commits[i]
		msg := cm.Commit.Message
		if idx := strings.Index(msg, "\n"); idx > 0 {
			msg = msg[:idx] // 提交首行（标题）
		}
		commits = append(commits, commitItem{Sha: cm.Sha, Message: msg, Date: cm.Commit.Author.Date})
	}
	c.JSON(http.StatusOK, gin.H{"current": current, "latest": latest, "commits": commits})
}

// ApplyUpdate POST /update/apply —— 拉取最新镜像并重建当前容器
func (h *Handler) ApplyUpdate(c *gin.Context) {
	if buildVersion == "dev" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "本地开发构建（dev）不支持自更新，请用 Docker 镜像部署"})
		return
	}
	if _, err := os.Stat(dockerSockPath); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未挂载 Docker socket，无法自更新。\n配置方法：在 docker-compose.yml 的 strmhub 服务 volumes 中加一行 /var/run/docker.sock:/var/run/docker.sock，然后 docker compose up -d 重建一次，之后即可在界面内一键更新"})
		return
	}
	latest := fetchLatestSHA(true)
	if latest == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "无法获取最新版本（GitHub 不可达，可在系统配置代理卡配置代理），稍后再试"})
		return
	}
	if strings.HasPrefix(latest, buildVersion[:7]) {
		c.JSON(http.StatusOK, gin.H{"message": "已是最新版本", "latest": latest})
		return
	}

	client := dockerHTTPClient(5 * time.Minute)

	// 1. 定位当前容器（优先 cgroup 里的真实 ID；HOSTNAME 可能被自定义 hostname 覆盖）
	selfID := selfContainerID()
	if selfID == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "无法确定当前容器 ID（cgroup 与 HOSTNAME 均不可用）"})
		return
	}
	insp, err := dockerRequestJSON(client, "GET", "/containers/"+selfID+"/json", nil)
	if err != nil {
		// ID 未命中（cgroup namespace 隐藏真实 ID / HOSTNAME 异常）：按镜像名兜底，
		// 找"唯一运行中的 strmhub 镜像容器"即视为自身
		if fallbackID, ferr := dockerFindSelfByImage(client); ferr == nil && fallbackID != "" && fallbackID != selfID {
			if finsp, ierr := dockerRequestJSON(client, "GET", "/containers/"+fallbackID+"/json", nil); ierr == nil {
				log.Printf("[自更新] HOSTNAME(%s) 未命中，已按镜像名兜底定位容器 %s", selfID, fallbackID[:12])
				selfID, insp, err = fallbackID, finsp, nil
			}
		}
	}
	if err != nil {
		// 附带诊断：列出该 socket 对应守护进程里可见的容器，区分"自定义 hostname"与"挂错 socket"
		hint := ""
		if names, lerr := dockerListContainerNames(client); lerr == nil && len(names) > 0 {
			hint = "该 Docker 守护进程可见容器：" + strings.Join(names, ", ")
		} else if lerr == nil {
			hint = "该 Docker 守护进程下没有任何容器（疑似挂载了别的 docker.sock）"
		}
		errMsg := "查询当前容器失败（" + selfID + "）: " + err.Error()
		if hint != "" {
			errMsg += "\n" + hint
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": errMsg})
		return
	}
	cfgMap, _ := insp["Config"].(map[string]interface{})
	hostCfg, _ := insp["HostConfig"].(map[string]interface{})
	imageRef, _ := cfgMap["Image"].(string)
	containerName := strings.TrimPrefix(insp["Name"].(string), "/")
	ref := normalizeImageRef(imageRef)
	cfgCmd, cfgEnv, cfgLabels := cfgMap["Cmd"], cfgMap["Env"], cfgMap["Labels"]
	cfgEntry, cfgWD, cfgUser := cfgMap["Entrypoint"], cfgMap["WorkingDir"], cfgMap["User"]
	// compose 自定义网络需要显式 EndpointsConfig（静态 IP/别名缺失会导致启动失败）
	endpoints := map[string]interface{}{}
	if ns, ok := insp["NetworkSettings"].(map[string]interface{})["Networks"].(map[string]interface{}); ok {
		for netName, nv := range ns {
			if m, ok := nv.(map[string]interface{}); ok {
				ep := map[string]interface{}{}
				for _, k := range []string{"IPAMConfig", "Aliases", "MacAddress"} {
					if v, ok := m[k]; ok && v != nil {
						ep[k] = v
					}
				}
				endpoints[netName] = ep
			}
		}
	}
	// 先清理上次失败残留的 -updating/-old 容器，避免本次改名冲突
	for _, suffix := range []string{"-updating", "-old", "-updater"} {
		if resp, err := dockerDo(client, "DELETE", "/containers/"+containerName+suffix+"?force=1", nil); err == nil {
			resp.Body.Close()
		}
	}

	// 2. 拉取最新镜像（同步等待，可能需要几十秒）
	if err := dockerPull(client, ref); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "拉取镜像失败: " + err.Error()})
		return
	}
	log.Printf("[自更新] ✓ 镜像已拉取: %s（当前 v%s → v%s）", ref, buildVersion[:7], latest[:7])

	// 3. 创建新容器（临时名；此刻旧容器仍在运行，无任何影响）
	tmpName := containerName + "-updating"
	if resp, err := dockerDo(client, "DELETE", "/containers/"+tmpName+"?force=1", nil); err == nil {
		resp.Body.Close()
	}
	createCfg := map[string]interface{}{
		"Image":      ref,
		"Cmd":        cfgCmd, "Env": cfgEnv, "Labels": cfgLabels,
		"Entrypoint": cfgEntry, "WorkingDir": cfgWD, "User": cfgUser,
		"HostConfig": hostCfg,
	}
	if len(endpoints) > 0 {
		createCfg["NetworkingConfig"] = map[string]interface{}{"EndpointsConfig": endpoints}
	}
	createBody, _ := json.Marshal(createCfg)
	resp, err := dockerDo(client, "POST", "/containers/create?name="+url.QueryEscape(tmpName), createBody)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "创建新容器失败: " + err.Error()})
		return
	}
	var created struct {
		ID string `json:"Id"`
	}
	createCode := resp.StatusCode
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if createCode >= 400 || created.ID == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("创建新容器失败: HTTP %d", createCode)})
		return
	}
	log.Printf("[自更新] ✓ 新容器已创建（%s）", tmpName)

	// 4. 双改名（运行中容器可安全改名）：旧→bak 让出原名，新→原名
	oldBak := containerName + "-old"
	if resp, err := dockerDo(client, "DELETE", "/containers/"+oldBak+"?force=1", nil); err == nil {
		resp.Body.Close()
	}
	if resp, err := dockerDo(client, "POST", "/containers/"+selfID+"/rename?name="+url.QueryEscape(oldBak), nil); err == nil {
		resp.Body.Close()
	} else {
		_ = resp
	}
	if resp, err := dockerDo(client, "POST", "/containers/"+created.ID+"/rename?name="+url.QueryEscape(containerName), nil); err == nil {
		resp.Body.Close()
	} else {
		_ = resp
	}
	log.Printf("[自更新] ✓ 改名完成（%s → %s，新容器已占用原名）", oldBak, containerName)

	// 5. 启动更新辅助容器（独立进程）完成停旧/启新/清理——主容器不能停自己
	if err := dockerLaunchUpdater(client, ref, selfID, created.ID, hostCfg); err != nil {
		// 辅助容器失败：回滚改名，一切如旧
		log.Printf("[自更新] ✗ %v，回滚改名", err)
		if resp, e := dockerDo(client, "POST", "/containers/"+created.ID+"/rename?name="+url.QueryEscape(tmpName), nil); e == nil {
			resp.Body.Close()
		}
		if resp, e := dockerDo(client, "POST", "/containers/"+selfID+"/rename?name="+url.QueryEscape(containerName), nil); e == nil {
			resp.Body.Close()
		}
		if resp, e := dockerDo(client, "DELETE", "/containers/"+created.ID+"?force=1", nil); e == nil {
			resp.Body.Close()
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error() + "（已回滚，服务未受影响）"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": "镜像已拉取，容器切换中（约 10~30 秒），页面将自动刷新",
		"latest": latest})
}

// normalizeImageRef 规范镜像引用为 repo:tag（digest 固定或无 tag 时取 latest）
func normalizeImageRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, "@"); i > 0 {
		ref = ref[:i] + ":latest"
	}
	if !strings.Contains(strings.SplitN(ref, "/", 2)[len(strings.SplitN(ref, "/", 2))-1], ":") {
		ref += ":latest"
	}
	return ref
}

// dockerPull POST /images/create 流式拉取，直到完成或出错
func dockerPull(client *http.Client, ref string) error {
	repo, tag := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		repo, tag = ref[:i], ref[i+1:]
	}
	resp, err := dockerDo(client, "POST", "/images/create?fromImage="+url.QueryEscape(repo)+"&tag="+url.QueryEscape(tag), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(string(b), 150))
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var line struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := dec.Decode(&line); err != nil {
			if err == io.EOF {
				return nil
			}
			return nil // 流解析异常视为完成（镜像层复用时流为空）
		}
		if line.Error != "" {
			return fmt.Errorf("%s", line.Error)
		}
	}
}

// dockerLaunchUpdater 启动「更新辅助容器」执行更新收尾。
// 主容器不能停自己（docker stop 会杀掉本进程，后续步骤无法执行），
// 因此用一个独立容器（同一镜像、以 update-finish 子命令运行、挂同一 docker.sock）
// 完成：停旧 → 启动新 → 删除旧。此前主容器已完成：创建新容器 + 双改名。
func dockerLaunchUpdater(client *http.Client, imageRef, oldID, newID string, hostCfg map[string]interface{}) error {
	// 从自身挂载里找 docker.sock 的 bind（辅助容器需要同样的 socket）
	sockBind := ""
	if binds, ok := hostCfg["Binds"].([]interface{}); ok {
		for _, b := range binds {
			if bs, ok := b.(string); ok && strings.HasSuffix(bs, ":"+dockerSockPath) {
				sockBind = bs
				break
			}
		}
	}
	if sockBind == "" {
		sockBind = dockerSockPath + ":" + dockerSockPath
	}
	body, _ := json.Marshal(map[string]interface{}{
		"Image": imageRef,
		"Cmd":   []string{"update-finish", oldID, newID},
		"HostConfig": map[string]interface{}{
			"Binds":        []string{sockBind},
			"NetworkMode":  "none",
			"AutoRemove":   true, // 退出后自动删除（收尾日志已写入主流程可观测的结果）
		},
		"Labels": map[string]string{"strmhub-role": "updater"},
	})
	resp, err := dockerDo(client, "POST", "/containers/create?name=strmhub-updater", body)
	if err != nil {
		return fmt.Errorf("创建辅助容器: %w", err)
	}
	var created struct {
		ID string `json:"Id"`
	}
	code := resp.StatusCode
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if code >= 400 || created.ID == "" {
		return fmt.Errorf("创建辅助容器 HTTP %d", code)
	}
	resp2, err := dockerDo(client, "POST", "/containers/"+created.ID+"/start", nil)
	if err != nil {
		return fmt.Errorf("启动辅助容器: %w", err)
	}
	resp2.Body.Close()
	log.Printf("[自更新] ✓ 更新辅助容器已启动（strmhub-updater），将由它完成停旧/启新")
	return nil
}

// RunUpdateFinish 更新收尾（在辅助容器进程内运行）：停旧 → 启动新 → 清理旧。
// 新容器启动失败时回滚：把旧容器重新启动（旧容器只是被停，配置未动）。
func RunUpdateFinish(oldID, newID string) {
	log.Printf("[更新辅助] ▶ 收尾开始：旧=%s 新=%s", truncateStr(oldID, 12), truncateStr(newID, 12))
	client := dockerHTTPClient(3 * time.Minute)

	// 等 2 秒让主容器把 HTTP 应答发出去
	time.Sleep(2 * time.Second)

	// 1. 停旧容器
	if resp, err := dockerDo(client, "POST", "/containers/"+oldID+"/stop?t=15", nil); err == nil {
		resp.Body.Close()
	} else {
		log.Printf("[更新辅助] ○ 停止旧容器返回: %v（可能已退出，继续）", err)
	}
	// 2. 等旧容器真正停止（端口释放），最多 30 秒
	stopped := false
	for i := 0; i < 15; i++ {
		insp, err := dockerRequestJSON(client, "GET", "/containers/"+oldID+"/json", nil)
		if err != nil {
			stopped = true // 已查不到（异常但可继续）
			break
		}
		if st, ok := insp["State"].(map[string]interface{}); ok {
			if run, _ := st["Running"].(bool); !run {
				stopped = true
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	if !stopped {
		log.Printf("[更新辅助] ✗ 旧容器 30 秒未停止，中止（新容器未启动，服务未受影响）")
		return
	}
	log.Printf("[更新辅助] ✓ 旧容器已停止")

	// 3. 启动新容器（端口已释放）
	resp, err := dockerDo(client, "POST", "/containers/"+newID+"/start", nil)
	if err != nil {
		log.Printf("[更新辅助] ✗ 启动新容器失败: %v —— 回滚：重新启动旧容器", err)
		if r2, e2 := dockerDo(client, "POST", "/containers/"+oldID+"/start", nil); e2 == nil {
			r2.Body.Close()
			log.Printf("[更新辅助] ✓ 旧容器已回滚启动，服务恢复")
		} else {
			log.Printf("[更新辅助] ✗ 回滚失败：%v（请手动 docker start 旧容器）", e2)
		}
		return
	}
	resp.Body.Close()
	log.Printf("[更新辅助] ✓ 新容器已启动")

	// 4. 等新容器确认运行（最多 20 秒），然后删除旧容器
	for i := 0; i < 10; i++ {
		insp, err := dockerRequestJSON(client, "GET", "/containers/"+newID+"/json", nil)
		if err == nil {
			if st, ok := insp["State"].(map[string]interface{}); ok {
				if run, _ := st["Running"].(bool); run {
					break
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	if resp, err := dockerDo(client, "DELETE", "/containers/"+oldID+"?force=1", nil); err == nil {
		resp.Body.Close()
	}
	log.Printf("[更新辅助] ✅ 更新完成（旧容器已清理）")
}

func dockerDo(client *http.Client, method, path string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(string(body))
	}
	req, err := http.NewRequest(method, "http://docker"+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return client.Do(req)
}

// selfContainerID 定位自身容器 ID：
// /proc/self/cgroup 形如 .../docker-<64位ID>.scope（cgroup v2）或 /docker/<64位ID>（v1），最可靠；
// HOSTNAME 默认等于容器短 ID，但用户自定义 hostname 时会失效，仅作兜底。
func selfContainerID() string {
	if b, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		re := regexp.MustCompile(`docker[/-]([0-9a-f]{12,64})`)
		for _, line := range strings.Split(string(b), "\n") {
			if m := re.FindStringSubmatch(line); m != nil {
				return m[1]
			}
		}
	}
	return os.Getenv("HOSTNAME")
}

// dockerFindSelfByImage 按镜像名兜底定位自身：唯一运行中的 strmhub 镜像容器视为自己
func dockerFindSelfByImage(client *http.Client) (string, error) {
	resp, err := dockerDo(client, "GET", "/containers/json?all=1", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var list []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
		Image string   `json:"Image"`
		State string   `json:"State"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return "", err
	}
	hit := ""
	for _, it := range list {
		img := strings.ToLower(it.Image)
		name := ""
		if len(it.Names) > 0 {
			name = strings.TrimPrefix(it.Names[0], "/")
		}
		if it.State == "running" && (strings.Contains(img, "/strmhub") || strings.HasSuffix(img, "strmhub:latest") || strings.Contains(name, "strmhub")) {
			if hit != "" {
				return "", fmt.Errorf("发现多个 strmhub 容器，无法确定自身")
			}
			hit = it.ID
		}
	}
	return hit, nil
}

// dockerListContainerNames 列出守护进程内所有容器名（自更新失败时诊断用）
func dockerListContainerNames(client *http.Client) ([]string, error) {
	resp, err := dockerDo(client, "GET", "/containers/json?all=1", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var list []struct {
		Names []string `json:"Names"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	names := []string{}
	for _, it := range list {
		if len(it.Names) > 0 {
			names = append(names, strings.TrimPrefix(it.Names[0], "/"))
		}
	}
	return names, nil
}

func dockerRequestJSON(client *http.Client, method, path string, body []byte) (map[string]interface{}, error) {
	resp, err := dockerDo(client, method, path, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(string(b), 150))
	}
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
