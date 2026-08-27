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
	imageRef, _ := insp["Config"].(map[string]interface{})["Image"].(string)
	containerName := strings.TrimPrefix(insp["Name"].(string), "/")
	ref := normalizeImageRef(imageRef)

	// 2. 拉取最新镜像（同步等待，可能需要几十秒）
	if err := dockerPull(client, ref); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "拉取镜像失败: " + err.Error()})
		return
	}
	log.Printf("[自更新] ✓ 镜像已拉取: %s（当前 v%s → v%s）", ref, buildVersion[:7], latest[:7])

	// 3. 先应答客户端，再异步重建（stop 自身会切断连接）
	go func() {
		time.Sleep(800 * time.Millisecond)
		if err := dockerRecreateSelf(client, selfID, containerName, ref, insp); err != nil {
			log.Printf("[自更新] ✗ 重建容器失败: %v（旧容器仍在运行，可手动处理）", err)
			return
		}
		log.Printf("[自更新] ✓ 容器已用新镜像重建并启动")
	}()
	c.JSON(http.StatusAccepted, gin.H{"message": "镜像已拉取，容器即将重启（预计 10~30 秒），页面恢复后请刷新",
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

// dockerRecreateSelf 用相同配置重建容器（安全顺序：先建新再删旧，失败可恢复）：
// create(tmp) → stop(old) → rename(old→xxx-old) → rename(tmp→原名) → start(new) → remove(old)
func dockerRecreateSelf(client *http.Client, oldID, name, imageRef string, insp map[string]interface{}) error {
	cfg := insp["Config"].(map[string]interface{})
	hostCfg := insp["HostConfig"].(map[string]interface{})

	create := map[string]interface{}{
		"Image":      imageRef,
		"Cmd":        cfg["Cmd"],
		"Env":        cfg["Env"],
		"Labels":     cfg["Labels"],
		"Entrypoint": cfg["Entrypoint"],
		"WorkingDir": cfg["WorkingDir"],
		"User":       cfg["User"],
		"HostConfig": hostCfg,
	}
	createBody, _ := json.Marshal(create)

	// 1. 旧容器还在运行时先创建新容器（临时名；端口冲突只在 start 时生效，create 不受影响）
	tmpName := name + "-updating"
	if resp, err := dockerDo(client, "DELETE", "/containers/"+tmpName+"?force=1", nil); err == nil {
		resp.Body.Close() // 清理上次失败残留
	}
	resp, err := dockerDo(client, "POST", "/containers/create?name="+url.QueryEscape(tmpName), createBody)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	var created struct {
		ID string `json:"Id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if resp.StatusCode >= 400 || created.ID == "" {
		return fmt.Errorf("create HTTP %d: %s", resp.StatusCode, truncateStr(fmt.Sprint(created.ID), 200))
	}

	// 2. 停旧 → 旧改名让出原名 → 新改名回原名 → 启动新
	if resp, err := dockerDo(client, "POST", "/containers/"+oldID+"/stop?t=10", nil); err == nil {
		resp.Body.Close()
	}
	oldBak := name + "-old"
	if resp, err := dockerDo(client, "DELETE", "/containers/"+oldBak+"?force=1", nil); err == nil {
		resp.Body.Close()
	}
	if resp, err := dockerDo(client, "POST", "/containers/"+oldID+"/rename?name="+url.QueryEscape(oldBak), nil); err == nil {
		resp.Body.Close()
	}
	if resp, err := dockerDo(client, "POST", "/containers/"+created.ID+"/rename?name="+url.QueryEscape(name), nil); err == nil {
		resp.Body.Close()
	}
	resp2, err := dockerDo(client, "POST", "/containers/"+created.ID+"/start", nil)
	if err != nil {
		return fmt.Errorf("start 新容器失败（旧容器保留为 %s，可手动 docker start 恢复）: %w", oldBak, err)
	}
	resp2.Body.Close()

	// 3. 新容器已启动，删除旧容器
	if resp, err := dockerDo(client, "DELETE", "/containers/"+oldID+"?force=1", nil); err == nil {
		resp.Body.Close()
	}
	return nil
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
