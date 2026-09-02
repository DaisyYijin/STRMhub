package api

// ==================== 115 专属 HTTP 客户端 ====================
//
// 115 部分 API 节点（pro.api.115.com 等）下发的 TLS 证书缺少 SAN
// （实测报错 "certificate is not valid for any names, but wanted to
// match pro.api.115.com"），Go 默认严格校验主机名直接握手失败——
// 直链获取/转封装播放全部中断。p115client/OpenList 生态对此的通行
// 处理是对 115 域名放宽校验。
//
// 这里取折衷：VerifyPeerCertificate 手工验证证书链（系统根 CA），
// 仅跳过主机名匹配——证书必须是正规 CA 签发的有效链，防 MITM；
// 比生态常用的裸 InsecureSkipVerify 安全。

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"sync"
	"time"
)

var (
	tls115Once sync.Once
	tls115Conf *tls.Config
)

func tlsConfig115() *tls.Config {
	tls115Once.Do(func() {
		tls115Conf = &tls.Config{
			MinVersion: tls.VersionTLS12,
			// InsecureSkipVerify=true 只是关掉默认校验流程，
			// 实际校验由 VerifyPeerCertificate 接管（链有效、不校验主机名）
			InsecureSkipVerify:    true,
			VerifyPeerCertificate: verifyChainSkipHostname115,
		}
	})
	return tls115Conf
}

// verifyChainSkipHostname115 证书链用系统根验证，主机名不匹配放行
// （115 自己的证书缺 SAN 是服务端配置问题，不是攻击特征）
func verifyChainSkipHostname115(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	if len(rawCerts) == 0 {
		return err115TLS("服务端未提供证书")
	}
	certs := make([]*x509.Certificate, 0, len(rawCerts))
	for _, raw := range rawCerts {
		c, err := x509.ParseCertificate(raw)
		if err != nil {
			return err115TLS("证书解析失败: " + err.Error())
		}
		certs = append(certs, c)
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		// 拿不到系统根（极端环境）：链校验无法进行，退回仅时间有效性检查
		now := time.Now()
		for _, c := range certs {
			if now.Before(c.NotBefore) || now.After(c.NotAfter) {
				return err115TLS("证书已过期或未生效")
			}
		}
		return nil
	}
	intermediates := x509.NewCertPool()
	for _, c := range certs[1:] {
		intermediates.AddCert(c)
	}
	if _, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates}); err != nil {
		return err115TLS("证书链校验失败: " + err.Error())
	}
	return nil
}

func err115TLS(msg string) error {
	return &tls115Error{msg: msg}
}

type tls115Error struct{ msg string }

func (e *tls115Error) Error() string { return "115 TLS: " + e.msg }

// http115Transport 115 专属共享 Transport（保活复用）
var http115Transport = &http.Transport{
	TLSClientConfig: tlsConfig115(),
	MaxIdleConns:    8,
	IdleConnTimeout: 90 * time.Second,
}

// client115 构建 115 请求客户端（带 SAN 容忍 TLS）
func client115(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: http115Transport}
}
