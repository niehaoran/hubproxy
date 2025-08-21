package handlers

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"hubproxy/config"
	"hubproxy/utils"

	"github.com/gin-gonic/gin"
)

var (
	// GitHub URL匹配正则表达式
	githubExps = []*regexp.Regexp{
		regexp.MustCompile(`^(?:https?://)?github\.com/([^/]+)/([^/]+)/(?:releases|archive)/.*`),
		regexp.MustCompile(`^(?:https?://)?github\.com/([^/]+)/([^/]+)/(?:blob|raw)/.*`),
		regexp.MustCompile(`^(?:https?://)?github\.com/([^/]+)/([^/]+)/(?:info|git-).*`),
		regexp.MustCompile(`^(?:https?://)?raw\.github(?:usercontent|)\.com/([^/]+)/([^/]+)/.+?/.+`),
		regexp.MustCompile(`^(?:https?://)?gist\.(?:githubusercontent|github)\.com/(.+?)/(.+?)/.+\.[a-zA-Z0-9]+$`),
		regexp.MustCompile(`^(?:https?://)?api\.github\.com/repos/([^/]+)/([^/]+)/.*`),
		regexp.MustCompile(`^(?:https?://)?huggingface\.co(?:/spaces)?/([^/]+)/(.+)`),
		regexp.MustCompile(`^(?:https?://)?cdn-lfs\.hf\.co(?:/spaces)?/([^/]+)/([^/]+)(?:/(.*))?`),
		regexp.MustCompile(`^(?:https?://)?download\.docker\.com/([^/]+)/.*\.(tgz|zip)`),
		regexp.MustCompile(`^(?:https?://)?(github|opengraph)\.githubassets\.com/([^/]+)/.+?`),
	}
)

// GitHubProxyHandler GitHub代理处理器
func GitHubProxyHandler(c *gin.Context) {
	rawPath := strings.TrimPrefix(c.Request.URL.RequestURI(), "/")

	for strings.HasPrefix(rawPath, "/") {
		rawPath = strings.TrimPrefix(rawPath, "/")
	}

	// 自动补全协议头
	if !strings.HasPrefix(rawPath, "https://") {
		if strings.HasPrefix(rawPath, "http:/") || strings.HasPrefix(rawPath, "https:/") {
			rawPath = strings.Replace(rawPath, "http:/", "", 1)
			rawPath = strings.Replace(rawPath, "https:/", "", 1)
		} else if strings.HasPrefix(rawPath, "http://") {
			rawPath = strings.TrimPrefix(rawPath, "http://")
		}
		rawPath = "https://" + rawPath
	}

	matches := CheckGitHubURL(rawPath)
	if matches != nil {
		if allowed, reason := utils.GlobalAccessController.CheckGitHubAccess(matches); !allowed {
			var repoPath string
			if len(matches) >= 2 {
				username := matches[0]
				repoName := strings.TrimSuffix(matches[1], ".git")
				repoPath = username + "/" + repoName
			}
			fmt.Printf("GitHub仓库 %s 访问被拒绝: %s\n", repoPath, reason)
			c.String(http.StatusForbidden, reason)
			return
		}
	} else {
		c.String(http.StatusForbidden, "无效输入")
		return
	}

	// 将blob链接转换为raw链接
	if githubExps[1].MatchString(rawPath) {
		rawPath = strings.Replace(rawPath, "/blob/", "/raw/", 1)
	}

	ProxyGitHubRequest(c, rawPath)
}

// CheckGitHubURL 检查URL是否匹配GitHub模式
func CheckGitHubURL(u string) []string {
	for _, exp := range githubExps {
		if matches := exp.FindStringSubmatch(u); matches != nil {
			return matches[1:]
		}
	}
	return nil
}

// ProxyGitHubRequest 代理GitHub请求
func ProxyGitHubRequest(c *gin.Context, u string) {
	proxyGitHubWithRedirect(c, u, 0)
}

// proxyGitHubWithRedirect 带重定向的GitHub代理请求
func proxyGitHubWithRedirect(c *gin.Context, u string, redirectCount int) {
	const maxRedirects = 20
	if redirectCount > maxRedirects {
		c.String(http.StatusLoopDetected, "重定向次数过多，可能存在循环重定向")
		return
	}

	// 首先发送HEAD请求获取文件大小
	headReq, err := http.NewRequest("HEAD", u, nil)
	if err != nil {
		c.String(http.StatusInternalServerError, fmt.Sprintf("server error %v", err))
		return
	}

	// 复制认证相关的请求头到HEAD请求
	for key, values := range c.Request.Header {
		if strings.HasPrefix(strings.ToLower(key), "authorization") ||
			strings.HasPrefix(strings.ToLower(key), "cookie") {
			for _, value := range values {
				headReq.Header.Add(key, value)
			}
		}
	}

	headResp, err := utils.GetGlobalHTTPClient().Do(headReq)
	if err != nil {
		c.String(http.StatusInternalServerError, fmt.Sprintf("server error %v", err))
		return
	}
	headResp.Body.Close()

	// 获取文件大小
	var contentLength int64
	if contentLengthStr := headResp.Header.Get("Content-Length"); contentLengthStr != "" {
		contentLength, _ = strconv.ParseInt(contentLengthStr, 10, 64)
	}

	// 检查文件大小限制
	cfg := config.GetConfig()
	if contentLength > 0 && contentLength > cfg.Server.FileSize {
		c.String(http.StatusRequestEntityTooLarge,
			fmt.Sprintf("文件过大，限制大小: %d MB", cfg.Server.FileSize/(1024*1024)))
		return
	}

	// 解析Range请求
	rangeHeader := c.GetHeader("Range")
	rangeInfo := utils.ParseRangeHeader(rangeHeader, contentLength)

	// 创建实际的下载请求
	req, err := utils.CreateRangeRequest(c.Request.Method, u, rangeInfo, c.Request)
	if err != nil {
		c.String(http.StatusInternalServerError, fmt.Sprintf("server error %v", err))
		return
	}

	resp, err := utils.GetGlobalHTTPClient().Do(req)
	if err != nil {
		c.String(http.StatusInternalServerError, fmt.Sprintf("server error %v", err))
		return
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			fmt.Printf("关闭响应体失败: %v\n", err)
		}
	}()

	// 清理安全相关的头
	resp.Header.Del("Content-Security-Policy")
	resp.Header.Del("Referrer-Policy")
	resp.Header.Del("Strict-Transport-Security")

	// 获取真实域名
	realHost := c.Request.Header.Get("X-Forwarded-Host")
	if realHost == "" {
		realHost = c.Request.Host
	}
	if !strings.HasPrefix(realHost, "http://") && !strings.HasPrefix(realHost, "https://") {
		realHost = "https://" + realHost
	}

	// 处理重定向
	if location := resp.Header.Get("Location"); location != "" {
		if CheckGitHubURL(location) != nil {
			c.Header("Location", "/"+location)
		} else {
			proxyGitHubWithRedirect(c, location, redirectCount+1)
			return
		}
	}

	// 获取内容类型
	contentType := resp.Header.Get("Content-Type")

	// 设置Range响应头
	utils.SetRangeHeaders(c.Writer, rangeInfo, contentLength, contentType)

	// 复制其他响应头（排除已设置的）
	for key, values := range resp.Header {
		lowerKey := strings.ToLower(key)
		if lowerKey != "content-length" && lowerKey != "content-type" &&
			lowerKey != "content-range" && lowerKey != "accept-ranges" {
			for _, value := range values {
				c.Header(key, value)
			}
		}
	}

	// 处理.sh文件的智能处理（仅在非Range请求时）
	if strings.HasSuffix(strings.ToLower(u), ".sh") && !rangeInfo.Valid {
		isGzipCompressed := resp.Header.Get("Content-Encoding") == "gzip"

		processedBody, processedSize, err := utils.ProcessSmart(resp.Body, isGzipCompressed, realHost)
		if err != nil {
			fmt.Printf("智能处理失败，回退到直接代理: %v\n", err)
			processedBody = resp.Body
			processedSize = 0
		}

		// 智能设置响应头
		if processedSize > 0 {
			c.Header("Content-Length", "")
			c.Header("Content-Encoding", "")
			c.Header("Transfer-Encoding", "chunked")
		}

		// 输出处理后的内容
		if _, err := io.Copy(c.Writer, processedBody); err != nil {
			return
		}
	} else {
		// 使用Range支持的流式转发
		if err := utils.CopyRange(c.Writer, resp.Body, rangeInfo); err != nil {
			fmt.Printf("Range数据传输失败: %v\n", err)
			return
		}
	}
}
