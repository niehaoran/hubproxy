package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"hubproxy/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// ImageSizeRequest 镜像大小查询请求
type ImageSizeRequest struct {
	Image        string `json:"image" binding:"required"`
	Tag          string `json:"tag"`
	Architecture string `json:"architecture"`
}

// ImageSizeResponse 镜像大小查询响应
type ImageSizeResponse struct {
	Success      bool     `json:"success"`
	Image        string   `json:"image"`
	Size         SizeInfo `json:"size"`
	Layers       int      `json:"layers"`
	Architecture string   `json:"architecture"`
	Registry     string   `json:"registry"`
	Timestamp    string   `json:"timestamp"`
	Cached       bool     `json:"cached,omitempty"`
}

// SizeInfo 大小信息
type SizeInfo struct {
	Bytes int64   `json:"bytes"`
	MB    float64 `json:"mb"`
	GB    float64 `json:"gb"`
	Human string  `json:"human"`
}

// ImageLayersRequest 镜像层信息查询请求
type ImageLayersRequest struct {
	Image        string `json:"image" binding:"required"`
	Tag          string `json:"tag"`
	Architecture string `json:"architecture"`
}

// ImageLayersResponse 镜像层信息查询响应
type ImageLayersResponse struct {
	Success   bool         `json:"success"`
	Image     string       `json:"image"`
	Manifest  ManifestInfo `json:"manifest"`
	Layers    []LayerInfo  `json:"layers"`
	Registry  string       `json:"registry"`
	Timestamp string       `json:"timestamp"`
	Cached    bool         `json:"cached,omitempty"`
}

// ManifestInfo Manifest信息
type ManifestInfo struct {
	Type         string `json:"type"`
	Architecture string `json:"architecture"`
	ConfigDigest string `json:"configDigest,omitempty"`
	LayerCount   int    `json:"layerCount"`
}

// LayerInfo 层信息
type LayerInfo struct {
	Index       int               `json:"index"`
	Digest      string            `json:"digest"`
	MediaType   string            `json:"mediaType"`
	Size        int64             `json:"size"`
	URLs        []string          `json:"urls,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ErrorResponse 错误响应
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Image   string `json:"image,omitempty"`
}

// parseImageName 解析镜像名称，返回registry和路径
func parseImageName(image string) (registry, path string) {
	// 支持的registry列表
	allowedRegistries := []string{
		"registry-1.docker.io",
		"docker.io",
		"ghcr.io",
		"gcr.io",
		"k8s.gcr.io",
		"registry.k8s.io",
		"quay.io",
	}

	parts := strings.Split(image, "/")

	// 处理 docker.io 或直接指定的registry
	if strings.HasPrefix(image, "docker.io/") {
		registry = "registry-1.docker.io"
		path = strings.TrimPrefix(image, "docker.io/")
		if !strings.Contains(path, "/") {
			path = "library/" + path
		}
		return
	}

	// 检查是否包含registry域名
	if len(parts) >= 2 {
		for _, reg := range allowedRegistries {
			if parts[0] == reg || (reg == "registry-1.docker.io" && parts[0] == "docker.io") {
				if parts[0] == "docker.io" {
					registry = "registry-1.docker.io"
				} else {
					registry = parts[0]
				}
				path = strings.Join(parts[1:], "/")
				return
			}
		}
	}

	// 默认使用Docker Hub
	registry = "registry-1.docker.io"
	if !strings.Contains(image, "/") {
		path = "library/" + image
	} else {
		path = image
	}
	return
}

// calculateImageSize 计算镜像大小
func calculateImageSize(ctx context.Context, imageName, tag, arch string) (*ImageSizeResponse, error) {
	if tag == "" {
		tag = "latest"
	}
	if arch == "" {
		arch = "amd64"
	}

	// 解析镜像名称
	registryHost, imagePath := parseImageName(imageName)

	// 构建完整的镜像引用
	imageRef := fmt.Sprintf("%s/%s:%s", registryHost, imagePath, tag)

	// 解析引用
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return nil, fmt.Errorf("解析镜像引用失败: %v", err)
	}

	// 配置remote选项
	options := []remote.Option{
		remote.WithAuth(authn.Anonymous),
		remote.WithUserAgent("hubproxy/go-containerregistry"),
		remote.WithTransport(utils.GetGlobalHTTPClient().Transport),
		remote.WithContext(ctx),
	}

	// 获取镜像描述符
	desc, err := remote.Get(ref, options...)
	if err != nil {
		return nil, fmt.Errorf("获取镜像描述符失败: %v", err)
	}

	// 解析manifest
	var totalSize int64
	var layerCount int
	var architecture string

	// 根据MediaType判断manifest类型
	switch string(desc.MediaType) {
	case "application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.oci.image.manifest.v1+json":
		// 标准manifest
		img, err := desc.Image()
		if err != nil {
			return nil, fmt.Errorf("解析镜像失败: %v", err)
		}

		// 获取config
		configFile, err := img.ConfigFile()
		if err != nil {
			return nil, fmt.Errorf("获取配置文件失败: %v", err)
		}
		architecture = configFile.Architecture

		// 计算所有层的大小
		layers, err := img.Layers()
		if err != nil {
			return nil, fmt.Errorf("获取层信息失败: %v", err)
		}

		for _, layer := range layers {
			size, err := layer.Size()
			if err != nil {
				continue
			}
			totalSize += size
			layerCount++
		}

	case "application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.index.v1+json":
		// 多架构manifest list
		index, err := desc.ImageIndex()
		if err != nil {
			return nil, fmt.Errorf("解析镜像索引失败: %v", err)
		}

		indexManifest, err := index.IndexManifest()
		if err != nil {
			return nil, fmt.Errorf("获取索引manifest失败: %v", err)
		}

		// 查找指定架构的manifest
		var selectedDesc *v1.Descriptor
		for i, m := range indexManifest.Manifests {
			if m.Platform != nil {
				if m.Platform.Architecture == arch && m.Platform.OS == "linux" {
					selectedDesc = &indexManifest.Manifests[i]
					break
				}
			}
		}

		// 如果没找到指定架构，尝试amd64
		if selectedDesc == nil {
			for i, m := range indexManifest.Manifests {
				if m.Platform != nil && m.Platform.Architecture == "amd64" && m.Platform.OS == "linux" {
					selectedDesc = &indexManifest.Manifests[i]
					break
				}
			}
		}

		// 如果还是没找到，使用第一个
		if selectedDesc == nil && len(indexManifest.Manifests) > 0 {
			selectedDesc = &indexManifest.Manifests[0]
		}

		if selectedDesc == nil {
			return nil, fmt.Errorf("未找到可用的manifest")
		}

		// 获取具体架构的镜像
		img, err := index.Image(selectedDesc.Digest)
		if err != nil {
			return nil, fmt.Errorf("获取架构特定镜像失败: %v", err)
		}

		// 获取config
		configFile, err := img.ConfigFile()
		if err != nil {
			return nil, fmt.Errorf("获取配置文件失败: %v", err)
		}
		architecture = configFile.Architecture
		if selectedDesc.Platform != nil {
			architecture = fmt.Sprintf("%s/%s", selectedDesc.Platform.Architecture, selectedDesc.Platform.OS)
		}

		// 计算所有层的大小
		layers, err := img.Layers()
		if err != nil {
			return nil, fmt.Errorf("获取层信息失败: %v", err)
		}

		for _, layer := range layers {
			size, err := layer.Size()
			if err != nil {
				continue
			}
			totalSize += size
			layerCount++
		}

	default:
		return nil, fmt.Errorf("不支持的manifest类型: %s", string(desc.MediaType))
	}

	// 计算MB和GB
	sizeInMB := float64(totalSize) / 1024 / 1024
	sizeInGB := sizeInMB / 1024

	// 格式化可读字符串
	var humanSize string
	if totalSize > 1024*1024*1024 {
		humanSize = fmt.Sprintf("%.2f GB", sizeInGB)
	} else {
		humanSize = fmt.Sprintf("%.2f MB", sizeInMB)
	}

	return &ImageSizeResponse{
		Success: true,
		Image:   fmt.Sprintf("%s:%s", imageName, tag),
		Size: SizeInfo{
			Bytes: totalSize,
			MB:    sizeInMB,
			GB:    sizeInGB,
			Human: humanSize,
		},
		Layers:       layerCount,
		Architecture: architecture,
		Registry:     registryHost,
		Timestamp:    time.Now().Format(time.RFC3339),
	}, nil
}

// getImageLayers 获取镜像层信息
func getImageLayers(ctx context.Context, imageName, tag, arch string) (*ImageLayersResponse, error) {
	if tag == "" {
		tag = "latest"
	}
	if arch == "" {
		arch = "amd64"
	}

	// 解析镜像名称
	registryHost, imagePath := parseImageName(imageName)

	// 构建完整的镜像引用
	imageRef := fmt.Sprintf("%s/%s:%s", registryHost, imagePath, tag)

	// 解析引用
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return nil, fmt.Errorf("解析镜像引用失败: %v", err)
	}

	// 配置remote选项
	options := []remote.Option{
		remote.WithAuth(authn.Anonymous),
		remote.WithUserAgent("hubproxy/go-containerregistry"),
		remote.WithTransport(utils.GetGlobalHTTPClient().Transport),
		remote.WithContext(ctx),
	}

	// 获取镜像描述符
	desc, err := remote.Get(ref, options...)
	if err != nil {
		return nil, fmt.Errorf("获取镜像描述符失败: %v", err)
	}

	response := &ImageLayersResponse{
		Success:   true,
		Image:     fmt.Sprintf("%s:%s", imageName, tag),
		Registry:  registryHost,
		Timestamp: time.Now().Format(time.RFC3339),
		Layers:    make([]LayerInfo, 0),
	}

	// 解析manifest
	switch string(desc.MediaType) {
	case "application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.oci.image.manifest.v1+json":
		// 标准manifest
		img, err := desc.Image()
		if err != nil {
			return nil, fmt.Errorf("解析镜像失败: %v", err)
		}

		// 获取config
		configFile, err := img.ConfigFile()
		if err != nil {
			return nil, fmt.Errorf("获取配置文件失败: %v", err)
		}

		// 获取manifest原始数据
		manifestBytes, err := desc.RawManifest()
		if err != nil {
			return nil, fmt.Errorf("获取原始manifest失败: %v", err)
		}

		var manifestData map[string]interface{}
		if err := json.Unmarshal(manifestBytes, &manifestData); err != nil {
			return nil, fmt.Errorf("解析manifest JSON失败: %v", err)
		}

		response.Manifest = ManifestInfo{
			Type:         string(desc.MediaType),
			Architecture: configFile.Architecture,
			LayerCount:   0,
		}

		// 提取config digest
		if config, ok := manifestData["config"].(map[string]interface{}); ok {
			if digest, ok := config["digest"].(string); ok {
				response.Manifest.ConfigDigest = digest
			}
		}

		// 获取所有层
		layers, err := img.Layers()
		if err != nil {
			return nil, fmt.Errorf("获取层信息失败: %v", err)
		}

		for i, layer := range layers {
			digest, _ := layer.Digest()
			size, _ := layer.Size()
			mediaType, _ := layer.MediaType()

			response.Layers = append(response.Layers, LayerInfo{
				Index:     i + 1,
				Digest:    digest.String(),
				Size:      size,
				MediaType: string(mediaType),
			})
		}

		response.Manifest.LayerCount = len(response.Layers)

	case "application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.index.v1+json":
		// 多架构manifest，需要选择特定架构
		index, err := desc.ImageIndex()
		if err != nil {
			return nil, fmt.Errorf("解析镜像索引失败: %v", err)
		}

		indexManifest, err := index.IndexManifest()
		if err != nil {
			return nil, fmt.Errorf("获取索引manifest失败: %v", err)
		}

		// 查找指定架构的manifest
		var selectedDesc *v1.Descriptor
		for i, m := range indexManifest.Manifests {
			if m.Platform != nil {
				if m.Platform.Architecture == arch && m.Platform.OS == "linux" {
					selectedDesc = &indexManifest.Manifests[i]
					break
				}
			}
		}

		// 如果没找到，尝试amd64
		if selectedDesc == nil {
			for i, m := range indexManifest.Manifests {
				if m.Platform != nil && m.Platform.Architecture == "amd64" && m.Platform.OS == "linux" {
					selectedDesc = &indexManifest.Manifests[i]
					break
				}
			}
		}

		// 还是没找到，使用第一个
		if selectedDesc == nil && len(indexManifest.Manifests) > 0 {
			selectedDesc = &indexManifest.Manifests[0]
		}

		if selectedDesc == nil {
			return nil, fmt.Errorf("未找到可用的manifest")
		}

		// 获取具体架构的镜像
		img, err := index.Image(selectedDesc.Digest)
		if err != nil {
			return nil, fmt.Errorf("获取架构特定镜像失败: %v", err)
		}

		// 获取config
		configFile, err := img.ConfigFile()
		if err != nil {
			return nil, fmt.Errorf("获取配置文件失败: %v", err)
		}

		archInfo := configFile.Architecture
		if selectedDesc.Platform != nil {
			archInfo = fmt.Sprintf("%s/%s", selectedDesc.Platform.Architecture, selectedDesc.Platform.OS)
		}

		response.Manifest = ManifestInfo{
			Type:         "application/vnd.docker.distribution.manifest.list.v2+json",
			Architecture: archInfo,
			ConfigDigest: selectedDesc.Digest.String(),
			LayerCount:   0,
		}

		// 获取所有层
		layers, err := img.Layers()
		if err != nil {
			return nil, fmt.Errorf("获取层信息失败: %v", err)
		}

		for i, layer := range layers {
			digest, _ := layer.Digest()
			size, _ := layer.Size()
			mediaType, _ := layer.MediaType()

			response.Layers = append(response.Layers, LayerInfo{
				Index:     i + 1,
				Digest:    digest.String(),
				Size:      size,
				MediaType: string(mediaType),
			})
		}

		response.Manifest.LayerCount = len(response.Layers)

	default:
		return nil, fmt.Errorf("不支持的manifest类型: %s", string(desc.MediaType))
	}

	return response, nil
}

// ImageSizeHandler 处理镜像大小查询请求
func ImageSizeHandler(c *gin.Context) {
	var req ImageSizeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "无效的请求参数",
			Message: err.Error(),
		})
		return
	}

	// 设置默认值
	if req.Tag == "" {
		req.Tag = "latest"
	}
	if req.Architecture == "" {
		req.Architecture = "amd64"
	}

	// 检查缓存
	cacheKey := fmt.Sprintf("image-size:%s:%s:%s", req.Image, req.Tag, req.Architecture)
	if cached, ok := searchCache.Get(cacheKey); ok {
		response := cached.(*ImageSizeResponse)
		response.Cached = true
		c.JSON(http.StatusOK, response)
		return
	}

	// 创建带超时的context
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	// 计算镜像大小
	response, err := calculateImageSize(ctx, req.Image, req.Tag, req.Architecture)
	if err != nil {
		statusCode := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "未找到") {
			statusCode = http.StatusNotFound
		}

		c.JSON(statusCode, ErrorResponse{
			Error:   "获取镜像大小失败",
			Message: err.Error(),
			Image:   fmt.Sprintf("%s:%s", req.Image, req.Tag),
		})
		return
	}

	// 缓存结果（30分钟）
	searchCache.SetWithTTL(cacheKey, response, 30*time.Minute)

	c.JSON(http.StatusOK, response)
}

// ImageLayersHandler 处理镜像层信息查询请求
func ImageLayersHandler(c *gin.Context) {
	var req ImageLayersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "无效的请求参数",
			Message: err.Error(),
		})
		return
	}

	// 设置默认值
	if req.Tag == "" {
		req.Tag = "latest"
	}
	if req.Architecture == "" {
		req.Architecture = "amd64"
	}

	// 检查缓存
	cacheKey := fmt.Sprintf("image-layers:%s:%s:%s", req.Image, req.Tag, req.Architecture)
	if cached, ok := searchCache.Get(cacheKey); ok {
		response := cached.(*ImageLayersResponse)
		response.Cached = true
		c.JSON(http.StatusOK, response)
		return
	}

	// 创建带超时的context
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	// 获取镜像层信息
	response, err := getImageLayers(ctx, req.Image, req.Tag, req.Architecture)
	if err != nil {
		statusCode := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "未找到") {
			statusCode = http.StatusNotFound
		}

		c.JSON(statusCode, ErrorResponse{
			Error:   "获取镜像层信息失败",
			Message: err.Error(),
			Image:   fmt.Sprintf("%s:%s", req.Image, req.Tag),
		})
		return
	}

	// 缓存结果（30分钟）
	searchCache.SetWithTTL(cacheKey, response, 30*time.Minute)

	c.JSON(http.StatusOK, response)
}

// RegisterImageSizeRoutes 注册镜像大小查询路由
func RegisterImageSizeRoutes(router *gin.Engine) {
	api := router.Group("/api")
	{
		api.POST("/image-size", ImageSizeHandler)
		api.POST("/image-layers", ImageLayersHandler)
	}
}
