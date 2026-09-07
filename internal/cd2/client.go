// Package cd2 提供 CloudDrive2（CD2）gRPC 客户端。
//
// CD2 把几十种网盘（115/阿里/夸克/百度/天翼/OneDrive/S3…）统一成一个
// gRPC 服务（默认端口 19798，明文 HTTP/2）。本包只封装只读能力：
//
//	GetToken    用户名密码换 JWT（返回 expiration，过期自动重登）
//	GetSubFiles 列目录（服务端流式，按批返回 CloudDriveFile）
//	GetDownloadUrlPath 取播放地址：
//	  - directUrl        网盘原始直链（可能要求特定 UA/请求头，见 UserAgent/AdditionalHeaders）
//	  - downloadUrlPath  CD2 中转地址模板，形如 /static/{SCHEME}/{HOST}/{PREVIEW}/path?token=…，
//	                    替换 {SCHEME}/{HOST}/{PREVIEW} 后即得 CD2 服务器上的完整 URL，
//	                    由 CD2 中转流量，对任何网盘都可用且不受 UA 限制
//
// 认证失败（Unauthenticated）时自动重登一次再重试。
package cd2

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	pb "strmhub/internal/cd2/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const cd2Service = "clouddrive.CloudDriveFileSrv"

// Client 线程安全；endpoint 形如 host:port（可带 http(s):// 前缀，默认端口 19798）
type Client struct {
	endpoint string // grpc target（host:port）
	username string
	password string

	mu          sync.Mutex
	conn        *grpc.ClientConn
	token       string
	tokenExpiry time.Time
}

// NewClient 创建客户端（惰性连接，首次调用时拨号）
func NewClient(endpoint, username, password string) *Client {
	return &Client{endpoint: endpoint, username: username, password: password}
}

// Close 释放底层连接
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

func (c *Client) getConn() (*grpc.ClientConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn, nil
	}
	conn, err := grpc.NewClient(c.endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("连接 CD2 失败: %w", err)
	}
	c.conn = conn
	return conn, nil
}

// ensureTokenLocked 确保 JWT 有效（过期前 5 分钟内视为失效）；调用方须持有 c.mu
func (c *Client) ensureTokenLocked() error {
	if c.token != "" && time.Now().Before(c.tokenExpiry.Add(-5*time.Minute)) {
		return nil
	}
	conn, err := c.getConn()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req := &pb.GetTokenRequest{UserName: c.username, Password: c.password}
	var out pb.JWTToken
	if err := conn.Invoke(ctx, cd2Service+"/GetToken", req, &out); err != nil {
		return fmt.Errorf("CD2 登录失败: %w", err)
	}
	if !out.Success || out.Token == "" {
		return fmt.Errorf("CD2 登录失败: %s", orDefault(out.ErrorMessage, "账号或密码错误"))
	}
	c.token = out.Token
	if out.Expiration != nil {
		c.tokenExpiry = out.Expiration.AsTime()
	} else {
		c.tokenExpiry = time.Now().Add(12 * time.Hour)
	}
	return nil
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func (c *Client) invalidateToken() {
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
}

func authErr(err error) bool {
	code := status.Code(err)
	return code == codes.Unauthenticated || code == codes.PermissionDenied
}

// withAuth 返回带 Bearer 的 ctx（必要时登录）
func (c *Client) withAuth(ctx context.Context) (context.Context, error) {
	c.mu.Lock()
	err := c.ensureTokenLocked()
	tok := c.token
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+tok)), nil
}

// File 目录项（只取 STRM 相关字段）
type File struct {
	Name  string
	Path  string // CD2 内绝对路径
	Size  int64
	IsDir bool
}

// ListDir 列出一层目录（GetSubFiles 服务端流，可能分批返回）
func (c *Client) ListDir(ctx context.Context, path string) ([]File, error) {
	files, err := c.listDirOnce(ctx, path)
	if authErr(err) {
		c.invalidateToken()
		files, err = c.listDirOnce(ctx, path)
	}
	return files, err
}

func (c *Client) listDirOnce(ctx context.Context, path string) ([]File, error) {
	actx, err := c.withAuth(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := c.getConn()
	if err != nil {
		return nil, err
	}
	desc := &grpc.StreamDesc{StreamName: "GetSubFiles", ServerStreams: true}
	stream, err := conn.NewStream(actx, desc, cd2Service+"/GetSubFiles")
	if err != nil {
		return nil, fmt.Errorf("CD2 列目录失败: %w", err)
	}
	if err := stream.SendMsg(&pb.ListSubFileRequest{Path: path}); err != nil {
		return nil, err
	}
	if err := stream.CloseSend(); err != nil {
		return nil, err
	}
	var files []File
	for {
		reply := &pb.SubFilesReply{}
		if err := stream.RecvMsg(reply); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("CD2 列目录中断（%s）: %w", path, err)
		}
		for _, f := range reply.SubFiles {
			if f == nil {
				continue
			}
			files = append(files, File{Name: f.Name, Path: f.FullPathName, Size: f.Size, IsDir: f.IsDirectory})
		}
	}
	return files, nil
}

// URLInfo GetDownloadUrlPath 结果
type URLInfo struct {
	ProxyPath    string // CD2 中转地址模板（含 {SCHEME}/{HOST}/{PREVIEW} 占位符）
	ExpiresIn    uint64 // 秒；HasExpires=false 表示不过期
	HasExpires   bool
	DirectURL    string // 网盘原始直链（可能为空）
	UserAgent    string // 访问 DirectURL 必需的 UA（空表示无要求）
	ExtraHeaders map[string]string
}

// DownloadURL 取播放地址。direct=true 时附带请求网盘原始直链
func (c *Client) DownloadURL(ctx context.Context, path string, direct bool) (*URLInfo, error) {
	info, err := c.downloadURLOnce(ctx, path, direct)
	if authErr(err) {
		c.invalidateToken()
		info, err = c.downloadURLOnce(ctx, path, direct)
	}
	return info, err
}

func (c *Client) downloadURLOnce(ctx context.Context, path string, direct bool) (*URLInfo, error) {
	actx, err := c.withAuth(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := c.getConn()
	if err != nil {
		return nil, err
	}
	req := &pb.GetDownloadUrlPathRequest{Path: path, Preview: false, LazyRead: true, GetDirectUrl: direct}
	var out pb.DownloadUrlPathInfo
	if err := conn.Invoke(actx, cd2Service+"/GetDownloadUrlPath", req, &out); err != nil {
		return nil, fmt.Errorf("CD2 取直链失败: %w", err)
	}
	if out.DownloadUrlPath == "" && out.DirectUrl == nil {
		return nil, fmt.Errorf("CD2 未返回播放地址（文件是否存在？）")
	}
	info := &URLInfo{ProxyPath: out.DownloadUrlPath}
	if out.ExpiresIn != nil {
		info.ExpiresIn = *out.ExpiresIn
		info.HasExpires = true
	}
	if out.DirectUrl != nil {
		info.DirectURL = *out.DirectUrl
	}
	if out.UserAgent != nil {
		info.UserAgent = *out.UserAgent
	}
	info.ExtraHeaders = out.AdditionalHeaders
	return info, nil
}
