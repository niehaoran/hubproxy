package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"hubproxy/config"
	"hubproxy/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// DockerProxy Docker代理配置
type DockerProxy struct {
	registry name.Registry
	options  []remote.Option
}

var dockerProxy *DockerProxy

// RegistryDetector Registry检测器
type RegistryDetector struct{}

// detectRegistryDomain 检测Registry域名并返回域名和剩余路径
func (rd *RegistryDetector) detectRegistryDomain(path string) (string, string) {
	cfg := config.GetConfig()

	for domain := range cfg.Registries {
		if strings.HasPrefix(path, domain+"/") {
			remainingPath := strings.TrimPrefix(path, domain+"/")
			return domain, remainingPath
		}
	}

	return "", path
}

// isRegistryEnabled 检查Registry是否启用
func (rd *RegistryDetector) isRegistryEnabled(domain string) bool {
	cfg := config.GetConfig()
	if mapping, exists := cfg.Registries[domain]; exists {
		return mapping.Enabled
	}
	return false
}

// getRegistryMapping 获取Registry映射配置
func (rd *RegistryDetector) getRegistryMapping(domain string) (config.RegistryMapping, bool) {
	cfg := config.GetConfig()
	mapping, exists := cfg.Registries[domain]
	return mapping, exists && mapping.Enabled
}

var registryDetector = &RegistryDetector{}

// InitDockerProxy 初始化Docker代理
func InitDockerProxy() {
	registry, err := name.NewRegistry("registry-1.docker.io")
	if err != nil {
		fmt.Printf("创建Docker registry失败: %v\n", err)
		return
	}

	options := []remote.Option{
		remote.WithAuth(authn.Anonymous),
		remote.WithUserAgent("hubproxy/go-containerregistry"),
		remote.WithTransport(utils.GetGlobalHTTPClient().Transport),
	}

	dockerProxy = &DockerProxy{
		registry: registry,
		options:  options,
	}
}

// ProxyDockerRegistryGin 标准Docker Registry API v2代理
func ProxyDockerRegistryGin(c *gin.Context) {
	path := c.Request.URL.Path

	if path == "/v2/" {
		c.JSON(http.StatusOK, gin.H{})
		return
	}

	if strings.HasPrefix(path, "/v2/") {
		handleRegistryRequest(c, path)
	} else {
		c.String(http.StatusNotFound, "Docker Registry API v2 only")
	}
}

// handleRegistryRequest 处理Registry请求
func handleRegistryRequest(c *gin.Context, path string) {
	pathWithoutV2 := strings.TrimPrefix(path, "/v2/")

	if registryDomain, remainingPath := registryDetector.detectRegistryDomain(pathWithoutV2); registryDomain != "" {
		if registryDetector.isRegistryEnabled(registryDomain) {
			c.Set("target_registry_domain", registryDomain)
			c.Set("target_path", remainingPath)

			handleMultiRegistryRequest(c, registryDomain, remainingPath)
			return
		}
	}

	imageName, apiType, reference := parseRegistryPath(pathWithoutV2)
	if imageName == "" || apiType == "" {
		c.String(http.StatusBadRequest, "Invalid path format")
		return
	}

	if !strings.Contains(imageName, "/") {
		imageName = "library/" + imageName
	}

	if allowed, reason := utils.GlobalAccessController.CheckDockerAccess(imageName); !allowed {
		fmt.Printf("Docker镜像 %s 访问被拒绝: %s\n", imageName, reason)
		c.String(http.StatusForbidden, "镜像访问被限制")
		return
	}

	imageRef := fmt.Sprintf("%s/%s", dockerProxy.registry.Name(), imageName)

	switch apiType {
	case "manifests":
		handleManifestRequest(c, imageRef, reference)
	case "blobs":
		handleBlobRequest(c, imageRef, reference)
	case "tags":
		handleTagsRequest(c, imageRef)
	default:
		c.String(http.StatusNotFound, "API endpoint not found")
	}
}

// parseRegistryPath 解析Registry路径
func parseRegistryPath(path string) (imageName, apiType, reference string) {
	if idx := strings.Index(path, "/manifests/"); idx != -1 {
		imageName = path[:idx]
		apiType = "manifests"
		reference = path[idx+len("/manifests/"):]
		return
	}

	if idx := strings.Index(path, "/blobs/"); idx != -1 {
		imageName = path[:idx]
		apiType = "blobs"
		reference = path[idx+len("/blobs/"):]
		return
	}

	if idx := strings.Index(path, "/tags/list"); idx != -1 {
		imageName = path[:idx]
		apiType = "tags"
		reference = "list"
		return
	}

	return "", "", ""
}

// handleManifestRequest 处理manifest请求
func handleManifestRequest(c *gin.Context, imageRef, reference string) {
	// 早期大小检查 - 在manifest阶段就进行验证
	// 大小检查 - 使用并发版本，可能会获取manifest数据
	ctx := c.Request.Context()
	if allowed, sizeInfo, reason := utils.CheckImageSizeFast(ctx, imageRef, reference, dockerProxy.options); !allowed {
		fmt.Printf("镜像 %s:%s 大小检查失败: %s\n", imageRef, reference, reason)
		c.Header("X-Image-Size-Info", fmt.Sprintf("total=%s,layers=%d", utils.FormatBytes(sizeInfo.TotalSize), sizeInfo.LayerCount))
		c.String(http.StatusRequestEntityTooLarge, fmt.Sprintf("镜像过大: %s", reason))
		return
	} else if sizeInfo != nil {
		// 添加大小信息到响应头
		c.Header("X-Image-Size", utils.FormatBytes(sizeInfo.TotalSize))
		c.Header("X-Image-Layers", fmt.Sprintf("%d", sizeInfo.LayerCount))
		if sizeInfo.IsMultiArch {
			c.Header("X-Image-Multi-Arch", "true")
		}
	}

	// 尝试从缓存获取manifest（可能在大小检查时已缓存）
	if utils.IsCacheEnabled() && c.Request.Method == http.MethodGet {
		cacheKey := utils.BuildManifestCacheKey(imageRef, reference)

		if cachedItem := utils.GlobalCache.Get(cacheKey); cachedItem != nil {
			fmt.Printf("✅ 复用缓存的manifest: %s:%s\n", imageRef, reference)
			utils.WriteCachedResponse(c, cachedItem)
			return
		}
	}

	var ref name.Reference
	var err error

	if strings.HasPrefix(reference, "sha256:") {
		ref, err = name.NewDigest(fmt.Sprintf("%s@%s", imageRef, reference))
	} else {
		ref, err = name.NewTag(fmt.Sprintf("%s:%s", imageRef, reference))
	}

	if err != nil {
		fmt.Printf("解析镜像引用失败: %v\n", err)
		c.String(http.StatusBadRequest, "Invalid reference")
		return
	}

	if c.Request.Method == http.MethodHead {
		desc, err := remote.Head(ref, dockerProxy.options...)
		if err != nil {
			fmt.Printf("HEAD请求失败: %v\n", err)
			c.String(http.StatusNotFound, "Manifest not found")
			return
		}

		c.Header("Content-Type", string(desc.MediaType))
		c.Header("Docker-Content-Digest", desc.Digest.String())
		c.Header("Content-Length", fmt.Sprintf("%d", desc.Size))
		c.Status(http.StatusOK)
	} else {
		// GET请求 - 如果缓存未命中，需要重新获取
		fmt.Printf("🔄 获取新的manifest: %s:%s\n", imageRef, reference)
		desc, err := remote.Get(ref, dockerProxy.options...)
		if err != nil {
			fmt.Printf("GET请求失败: %v\n", err)
			c.String(http.StatusNotFound, "Manifest not found")
			return
		}

		headers := map[string]string{
			"Docker-Content-Digest": desc.Digest.String(),
			"Content-Length":        fmt.Sprintf("%d", len(desc.Manifest)),
		}

		// 缓存新获取的manifest
		if utils.IsCacheEnabled() {
			cacheKey := utils.BuildManifestCacheKey(imageRef, reference)
			ttl := utils.GetManifestTTL(reference)
			utils.GlobalCache.Set(cacheKey, desc.Manifest, string(desc.MediaType), headers, ttl)
			fmt.Printf("💾 缓存新manifest: %s:%s (TTL: %v)\n", imageRef, reference, ttl)
		}

		c.Header("Content-Type", string(desc.MediaType))
		for key, value := range headers {
			c.Header(key, value)
		}

		c.Data(http.StatusOK, string(desc.MediaType), desc.Manifest)
	}
}

// handleBlobRequest 处理blob请求
func handleBlobRequest(c *gin.Context, imageRef, digest string) {
	digestRef, err := name.NewDigest(fmt.Sprintf("%s@%s", imageRef, digest))
	if err != nil {
		fmt.Printf("解析digest引用失败: %v\n", err)
		c.String(http.StatusBadRequest, "Invalid digest reference")
		return
	}

	layer, err := remote.Layer(digestRef, dockerProxy.options...)
	if err != nil {
		fmt.Printf("获取layer失败: %v\n", err)
		c.String(http.StatusNotFound, "Layer not found")
		return
	}

	size, err := layer.Size()
	if err != nil {
		fmt.Printf("获取layer大小失败: %v\n", err)
		c.String(http.StatusInternalServerError, "Failed to get layer size")
		return
	}

	reader, err := layer.Compressed()
	if err != nil {
		fmt.Printf("获取layer内容失败: %v\n", err)
		c.String(http.StatusInternalServerError, "Failed to get layer content")
		return
	}
	defer reader.Close()

	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", fmt.Sprintf("%d", size))
	c.Header("Docker-Content-Digest", digest)

	c.Status(http.StatusOK)
	io.Copy(c.Writer, reader)
}

// handleTagsRequest 处理tags列表请求
func handleTagsRequest(c *gin.Context, imageRef string) {
	repo, err := name.NewRepository(imageRef)
	if err != nil {
		fmt.Printf("解析repository失败: %v\n", err)
		c.String(http.StatusBadRequest, "Invalid repository")
		return
	}

	tags, err := remote.List(repo, dockerProxy.options...)
	if err != nil {
		fmt.Printf("获取tags失败: %v\n", err)
		c.String(http.StatusNotFound, "Tags not found")
		return
	}

	response := map[string]interface{}{
		"name": strings.TrimPrefix(imageRef, dockerProxy.registry.Name()+"/"),
		"tags": tags,
	}

	c.JSON(http.StatusOK, response)
}

// ProxyDockerAuthGin Docker认证代理
func ProxyDockerAuthGin(c *gin.Context) {
	if utils.IsTokenCacheEnabled() {
		proxyDockerAuthWithCache(c)
	} else {
		proxyDockerAuthOriginal(c)
	}
}

// proxyDockerAuthWithCache 带缓存的认证代理
func proxyDockerAuthWithCache(c *gin.Context) {
	cacheKey := utils.BuildTokenCacheKey(c.Request.URL.RawQuery)

	if cachedToken := utils.GlobalCache.GetToken(cacheKey); cachedToken != "" {
		utils.WriteTokenResponse(c, cachedToken)
		return
	}

	recorder := &ResponseRecorder{
		ResponseWriter: c.Writer,
		statusCode:     200,
	}
	c.Writer = recorder

	proxyDockerAuthOriginal(c)

	if recorder.statusCode == 200 && len(recorder.body) > 0 {
		ttl := utils.ExtractTTLFromResponse(recorder.body)
		utils.GlobalCache.SetToken(cacheKey, string(recorder.body), ttl)
	}

	c.Writer = recorder.ResponseWriter
	c.Data(recorder.statusCode, "application/json", recorder.body)
}

// ResponseRecorder HTTP响应记录器
type ResponseRecorder struct {
	gin.ResponseWriter
	statusCode int
	body       []byte
}

func (r *ResponseRecorder) WriteHeader(code int) {
	r.statusCode = code
}

func (r *ResponseRecorder) Write(data []byte) (int, error) {
	r.body = append(r.body, data...)
	return len(data), nil
}

func proxyDockerAuthOriginal(c *gin.Context) {
	var authURL string
	if targetDomain, exists := c.Get("target_registry_domain"); exists {
		if mapping, found := registryDetector.getRegistryMapping(targetDomain.(string)); found {
			authURL = "https://" + mapping.AuthHost + c.Request.URL.Path
		} else {
			authURL = "https://auth.docker.io" + c.Request.URL.Path
		}
	} else {
		authURL = "https://auth.docker.io" + c.Request.URL.Path
	}

	if c.Request.URL.RawQuery != "" {
		authURL += "?" + c.Request.URL.RawQuery
	}

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: utils.GetGlobalHTTPClient().Transport,
	}

	req, err := http.NewRequestWithContext(
		context.Background(),
		c.Request.Method,
		authURL,
		c.Request.Body,
	)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to create request")
		return
	}

	for key, values := range c.Request.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		c.String(http.StatusBadGateway, "Auth request failed")
		return
	}
	defer resp.Body.Close()

	proxyHost := c.Request.Host
	if proxyHost == "" {
		cfg := config.GetConfig()
		proxyHost = fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
		if cfg.Server.Host == "0.0.0.0" {
			proxyHost = fmt.Sprintf("localhost:%d", cfg.Server.Port)
		}
	}

	for key, values := range resp.Header {
		for _, value := range values {
			if key == "Www-Authenticate" {
				value = rewriteAuthHeader(value, proxyHost)
			}
			c.Header(key, value)
		}
	}

	c.Status(resp.StatusCode)
	io.Copy(c.Writer, resp.Body)
}

// rewriteAuthHeader 重写认证头
func rewriteAuthHeader(authHeader, proxyHost string) string {
	authHeader = strings.ReplaceAll(authHeader, "https://auth.docker.io", "http://"+proxyHost)
	authHeader = strings.ReplaceAll(authHeader, "https://ghcr.io", "http://"+proxyHost)
	authHeader = strings.ReplaceAll(authHeader, "https://gcr.io", "http://"+proxyHost)
	authHeader = strings.ReplaceAll(authHeader, "https://quay.io", "http://"+proxyHost)

	return authHeader
}

// handleMultiRegistryRequest 处理多Registry请求
func handleMultiRegistryRequest(c *gin.Context, registryDomain, remainingPath string) {
	mapping, exists := registryDetector.getRegistryMapping(registryDomain)
	if !exists {
		c.String(http.StatusBadRequest, "Registry not configured")
		return
	}

	imageName, apiType, reference := parseRegistryPath(remainingPath)
	if imageName == "" || apiType == "" {
		c.String(http.StatusBadRequest, "Invalid path format")
		return
	}

	fullImageName := registryDomain + "/" + imageName
	if allowed, reason := utils.GlobalAccessController.CheckDockerAccess(fullImageName); !allowed {
		fmt.Printf("镜像 %s 访问被拒绝: %s\n", fullImageName, reason)
		c.String(http.StatusForbidden, "镜像访问被限制")
		return
	}

	upstreamImageRef := fmt.Sprintf("%s/%s", mapping.Upstream, imageName)

	switch apiType {
	case "manifests":
		handleUpstreamManifestRequest(c, upstreamImageRef, reference, mapping)
	case "blobs":
		handleUpstreamBlobRequest(c, upstreamImageRef, reference, mapping)
	case "tags":
		handleUpstreamTagsRequest(c, upstreamImageRef, mapping)
	default:
		c.String(http.StatusNotFound, "API endpoint not found")
	}
}

// handleUpstreamManifestRequest 处理上游Registry的manifest请求
func handleUpstreamManifestRequest(c *gin.Context, imageRef, reference string, mapping config.RegistryMapping) {
	// 上游Registry大小检查 - 使用并发版本，可能会获取manifest数据
	options := createUpstreamOptions(mapping)
	ctx := c.Request.Context()
	if allowed, sizeInfo, reason := utils.CheckImageSizeFast(ctx, imageRef, reference, options); !allowed {
		fmt.Printf("上游镜像 %s:%s 大小检查失败: %s\n", imageRef, reference, reason)
		c.Header("X-Image-Size-Info", fmt.Sprintf("total=%s,layers=%d", utils.FormatBytes(sizeInfo.TotalSize), sizeInfo.LayerCount))
		c.String(http.StatusRequestEntityTooLarge, fmt.Sprintf("镜像过大: %s", reason))
		return
	} else if sizeInfo != nil {
		// 添加大小信息到响应头
		c.Header("X-Image-Size", utils.FormatBytes(sizeInfo.TotalSize))
		c.Header("X-Image-Layers", fmt.Sprintf("%d", sizeInfo.LayerCount))
		if sizeInfo.IsMultiArch {
			c.Header("X-Image-Multi-Arch", "true")
		}
	}

	// 尝试从缓存获取manifest（可能在大小检查时已缓存）
	if utils.IsCacheEnabled() && c.Request.Method == http.MethodGet {
		cacheKey := utils.BuildManifestCacheKey(imageRef, reference)

		if cachedItem := utils.GlobalCache.Get(cacheKey); cachedItem != nil {
			fmt.Printf("✅ 复用缓存的上游manifest: %s:%s\n", imageRef, reference)
			utils.WriteCachedResponse(c, cachedItem)
			return
		}
	}

	var ref name.Reference
	var err error

	if strings.HasPrefix(reference, "sha256:") {
		ref, err = name.NewDigest(fmt.Sprintf("%s@%s", imageRef, reference))
	} else {
		ref, err = name.NewTag(fmt.Sprintf("%s:%s", imageRef, reference))
	}

	if err != nil {
		fmt.Printf("解析镜像引用失败: %v\n", err)
		c.String(http.StatusBadRequest, "Invalid reference")
		return
	}

	if c.Request.Method == http.MethodHead {
		desc, err := remote.Head(ref, options...)
		if err != nil {
			fmt.Printf("HEAD请求失败: %v\n", err)
			c.String(http.StatusNotFound, "Manifest not found")
			return
		}

		c.Header("Content-Type", string(desc.MediaType))
		c.Header("Docker-Content-Digest", desc.Digest.String())
		c.Header("Content-Length", fmt.Sprintf("%d", desc.Size))
		c.Status(http.StatusOK)
	} else {
		desc, err := remote.Get(ref, options...)
		if err != nil {
			fmt.Printf("GET请求失败: %v\n", err)
			c.String(http.StatusNotFound, "Manifest not found")
			return
		}

		headers := map[string]string{
			"Docker-Content-Digest": desc.Digest.String(),
			"Content-Length":        fmt.Sprintf("%d", len(desc.Manifest)),
		}

		if utils.IsCacheEnabled() {
			cacheKey := utils.BuildManifestCacheKey(imageRef, reference)
			ttl := utils.GetManifestTTL(reference)
			utils.GlobalCache.Set(cacheKey, desc.Manifest, string(desc.MediaType), headers, ttl)
		}

		c.Header("Content-Type", string(desc.MediaType))
		for key, value := range headers {
			c.Header(key, value)
		}

		c.Data(http.StatusOK, string(desc.MediaType), desc.Manifest)
	}
}

// handleUpstreamBlobRequest 处理上游Registry的blob请求
func handleUpstreamBlobRequest(c *gin.Context, imageRef, digest string, mapping config.RegistryMapping) {
	digestRef, err := name.NewDigest(fmt.Sprintf("%s@%s", imageRef, digest))
	if err != nil {
		fmt.Printf("解析digest引用失败: %v\n", err)
		c.String(http.StatusBadRequest, "Invalid digest reference")
		return
	}

	options := createUpstreamOptions(mapping)
	layer, err := remote.Layer(digestRef, options...)
	if err != nil {
		fmt.Printf("获取layer失败: %v\n", err)
		c.String(http.StatusNotFound, "Layer not found")
		return
	}

	size, err := layer.Size()
	if err != nil {
		fmt.Printf("获取layer大小失败: %v\n", err)
		c.String(http.StatusInternalServerError, "Failed to get layer size")
		return
	}

	reader, err := layer.Compressed()
	if err != nil {
		fmt.Printf("获取layer内容失败: %v\n", err)
		c.String(http.StatusInternalServerError, "Failed to get layer content")
		return
	}
	defer reader.Close()

	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", fmt.Sprintf("%d", size))
	c.Header("Docker-Content-Digest", digest)

	c.Status(http.StatusOK)
	io.Copy(c.Writer, reader)
}

// handleUpstreamTagsRequest 处理上游Registry的tags请求
func handleUpstreamTagsRequest(c *gin.Context, imageRef string, mapping config.RegistryMapping) {
	repo, err := name.NewRepository(imageRef)
	if err != nil {
		fmt.Printf("解析repository失败: %v\n", err)
		c.String(http.StatusBadRequest, "Invalid repository")
		return
	}

	options := createUpstreamOptions(mapping)
	tags, err := remote.List(repo, options...)
	if err != nil {
		fmt.Printf("获取tags失败: %v\n", err)
		c.String(http.StatusNotFound, "Tags not found")
		return
	}

	response := map[string]interface{}{
		"name": strings.TrimPrefix(imageRef, mapping.Upstream+"/"),
		"tags": tags,
	}

	c.JSON(http.StatusOK, response)
}

// createUpstreamOptions 创建上游Registry选项
func createUpstreamOptions(mapping config.RegistryMapping) []remote.Option {
	options := []remote.Option{
		remote.WithAuth(authn.Anonymous),
		remote.WithUserAgent("hubproxy/go-containerregistry"),
		remote.WithTransport(utils.GetGlobalHTTPClient().Transport),
	}

	switch mapping.AuthType {
	case "github":
	case "google":
	case "quay":
	}

	return options
}
