package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// ImageConfigResponse 镜像配置响应结构
type ImageConfigResponse struct {
	Success bool             `json:"success"`
	Data    *ImageConfigData `json:"data,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// ImageConfigData 镜像配置数据
type ImageConfigData struct {
	Name         string           `json:"name"`
	Tag          string           `json:"tag"`
	Architecture string           `json:"architecture"`
	OS           string           `json:"os"`
	Created      string           `json:"created"`
	Author       string           `json:"author"`
	Config       *ContainerConfig `json:"config"`
	History      []HistoryEntry   `json:"history,omitempty"`
	RootFS       *RootFS          `json:"rootfs,omitempty"`
	Platforms    []PlatformInfo   `json:"platforms,omitempty"`
	IsMultiArch  bool             `json:"is_multi_arch"`
}

// ContainerConfig 容器配置
type ContainerConfig struct {
	Hostname        string              `json:"Hostname,omitempty"`
	Domainname      string              `json:"Domainname,omitempty"`
	User            string              `json:"User,omitempty"`
	AttachStdin     bool                `json:"AttachStdin,omitempty"`
	AttachStdout    bool                `json:"AttachStdout,omitempty"`
	AttachStderr    bool                `json:"AttachStderr,omitempty"`
	ExposedPorts    map[string]struct{} `json:"ExposedPorts,omitempty"`
	Tty             bool                `json:"Tty,omitempty"`
	OpenStdin       bool                `json:"OpenStdin,omitempty"`
	StdinOnce       bool                `json:"StdinOnce,omitempty"`
	Env             []string            `json:"Env,omitempty"`
	Cmd             []string            `json:"Cmd,omitempty"`
	Healthcheck     *HealthConfig       `json:"Healthcheck,omitempty"`
	ArgsEscaped     bool                `json:"ArgsEscaped,omitempty"`
	Image           string              `json:"Image,omitempty"`
	Volumes         map[string]struct{} `json:"Volumes,omitempty"`
	WorkingDir      string              `json:"WorkingDir,omitempty"`
	Entrypoint      []string            `json:"Entrypoint,omitempty"`
	NetworkDisabled bool                `json:"NetworkDisabled,omitempty"`
	MacAddress      string              `json:"MacAddress,omitempty"`
	OnBuild         []string            `json:"OnBuild,omitempty"`
	Labels          map[string]string   `json:"Labels,omitempty"`
	StopSignal      string              `json:"StopSignal,omitempty"`
	StopTimeout     *int                `json:"StopTimeout,omitempty"`
	Shell           []string            `json:"Shell,omitempty"`
}

// HealthConfig 健康检查配置
type HealthConfig struct {
	Test        []string      `json:"Test,omitempty"`
	Interval    time.Duration `json:"Interval,omitempty"`
	Timeout     time.Duration `json:"Timeout,omitempty"`
	StartPeriod time.Duration `json:"StartPeriod,omitempty"`
	Retries     int           `json:"Retries,omitempty"`
}

// HistoryEntry 历史记录
type HistoryEntry struct {
	Created    string `json:"created"`
	CreatedBy  string `json:"created_by"`
	EmptyLayer bool   `json:"empty_layer,omitempty"`
	Comment    string `json:"comment,omitempty"`
}

// RootFS 根文件系统信息
type RootFS struct {
	Type    string   `json:"type"`
	DiffIDs []string `json:"diff_ids"`
}

// PlatformInfo 平台信息
type PlatformInfo struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant,omitempty"`
}

// HandleImageConfig 处理镜像配置查询
func HandleImageConfig(c *gin.Context) {
	imageParam := c.Param("image")
	if imageParam == "" {
		c.JSON(http.StatusBadRequest, ImageConfigResponse{
			Success: false,
			Error:   "缺少镜像参数",
		})
		return
	}

	// 解码镜像名称（将下划线替换为斜杠）
	imageRef := strings.ReplaceAll(imageParam, "_", "/")
	tag := c.DefaultQuery("tag", "latest")
	platform := c.Query("platform") // 可选平台参数，如 "linux/amd64"

	// 构建完整的镜像引用
	if !strings.Contains(imageRef, ":") && !strings.Contains(imageRef, "@") {
		imageRef = imageRef + ":" + tag
	}

	// 解析镜像引用
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		c.JSON(http.StatusBadRequest, ImageConfigResponse{
			Success: false,
			Error:   "镜像引用格式错误: " + err.Error(),
		})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	// 获取镜像配置
	configData, err := getImageConfig(ctx, ref, platform)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ImageConfigResponse{
			Success: false,
			Error:   "获取镜像配置失败: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, ImageConfigResponse{
		Success: true,
		Data:    configData,
	})
}

// getImageConfig 获取镜像配置
func getImageConfig(ctx context.Context, ref name.Reference, platform string) (*ImageConfigData, error) {
	// 使用Docker代理的选项
	options := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuth(authn.Anonymous), // 使用匿名认证
	}

	// 获取镜像描述符
	desc, err := remote.Get(ref, options...)
	if err != nil {
		return nil, fmt.Errorf("获取镜像描述符失败: %w", err)
	}

	configData := &ImageConfigData{
		Name: ref.Context().Name(),
		Tag:  getTagFromRef(ref),
	}

	switch desc.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		// 多架构镜像
		configData.IsMultiArch = true
		return getMultiArchImageConfig(ctx, desc, configData, platform, options)
	case types.OCIManifestSchema1, types.DockerManifestSchema2:
		// 单架构镜像
		configData.IsMultiArch = false
		return getSingleImageConfig(ctx, desc, configData, options)
	default:
		// 默认按单架构处理
		configData.IsMultiArch = false
		return getSingleImageConfig(ctx, desc, configData, options)
	}
}

// getSingleImageConfig 获取单架构镜像配置
func getSingleImageConfig(ctx context.Context, desc *remote.Descriptor, configData *ImageConfigData, options []remote.Option) (*ImageConfigData, error) {
	// 解析manifest
	var manifest struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Config        struct {
			MediaType string `json:"mediaType"`
			Size      int64  `json:"size"`
			Digest    string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			MediaType string `json:"mediaType"`
			Size      int64  `json:"size"`
			Digest    string `json:"digest"`
		} `json:"layers"`
	}

	if err := json.Unmarshal(desc.Manifest, &manifest); err != nil {
		return nil, fmt.Errorf("解析manifest失败: %w", err)
	}

	// 获取配置blob
	configRef, err := name.NewDigest(fmt.Sprintf("%s@%s", configData.Name, manifest.Config.Digest))
	if err != nil {
		return nil, fmt.Errorf("构建配置引用失败: %w", err)
	}

	// 获取配置数据
	configBlob, err := remote.Layer(configRef, options...)
	if err != nil {
		return nil, fmt.Errorf("获取配置数据失败: %w", err)
	}

	configReader, err := configBlob.Uncompressed()
	if err != nil {
		return nil, fmt.Errorf("解压配置数据失败: %w", err)
	}
	defer configReader.Close()

	// 读取配置JSON
	var configJSON struct {
		Architecture string           `json:"architecture"`
		OS           string           `json:"os"`
		Created      string           `json:"created"`
		Author       string           `json:"author"`
		Config       *ContainerConfig `json:"config"`
		History      []HistoryEntry   `json:"history"`
		RootFS       *RootFS          `json:"rootfs"`
	}

	decoder := json.NewDecoder(configReader)
	if err := decoder.Decode(&configJSON); err != nil {
		return nil, fmt.Errorf("解析配置JSON失败: %w", err)
	}

	// 填充配置数据
	configData.Architecture = configJSON.Architecture
	configData.OS = configJSON.OS
	configData.Created = configJSON.Created
	configData.Author = configJSON.Author
	configData.Config = configJSON.Config
	configData.History = configJSON.History
	configData.RootFS = configJSON.RootFS

	return configData, nil
}

// getMultiArchImageConfig 获取多架构镜像配置
func getMultiArchImageConfig(ctx context.Context, desc *remote.Descriptor, configData *ImageConfigData, platform string, options []remote.Option) (*ImageConfigData, error) {
	// 解析manifest list
	var manifestList struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Manifests     []struct {
			MediaType string `json:"mediaType"`
			Size      int64  `json:"size"`
			Digest    string `json:"digest"`
			Platform  struct {
				Architecture string `json:"architecture"`
				OS           string `json:"os"`
				Variant      string `json:"variant,omitempty"`
			} `json:"platform"`
		} `json:"manifests"`
	}

	if err := json.Unmarshal(desc.Manifest, &manifestList); err != nil {
		return nil, fmt.Errorf("解析manifest list失败: %w", err)
	}

	// 收集平台信息
	platforms := make([]PlatformInfo, 0, len(manifestList.Manifests))
	for _, m := range manifestList.Manifests {
		platforms = append(platforms, PlatformInfo{
			Architecture: m.Platform.Architecture,
			OS:           m.Platform.OS,
			Variant:      m.Platform.Variant,
		})
	}
	configData.Platforms = platforms

	// 选择特定平台或默认平台
	var selectedManifest *struct {
		MediaType string `json:"mediaType"`
		Size      int64  `json:"size"`
		Digest    string `json:"digest"`
		Platform  struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
			Variant      string `json:"variant,omitempty"`
		} `json:"platform"`
	}

	if platform != "" {
		// 查找指定平台
		parts := strings.Split(platform, "/")
		if len(parts) >= 2 {
			targetOS, targetArch := parts[0], parts[1]
			var targetVariant string
			if len(parts) >= 3 {
				targetVariant = parts[2]
			}

			for _, m := range manifestList.Manifests {
				if m.Platform.OS == targetOS && m.Platform.Architecture == targetArch {
					if targetVariant == "" || m.Platform.Variant == targetVariant {
						selectedManifest = &m
						break
					}
				}
			}
		}
	}

	// 如果没有找到指定平台，选择第一个（通常是linux/amd64）
	if selectedManifest == nil && len(manifestList.Manifests) > 0 {
		selectedManifest = &manifestList.Manifests[0]
	}

	if selectedManifest == nil {
		return nil, fmt.Errorf("未找到可用的平台manifest")
	}

	// 获取选定平台的manifest
	platformRef, err := name.NewDigest(fmt.Sprintf("%s@%s", configData.Name, selectedManifest.Digest))
	if err != nil {
		return nil, fmt.Errorf("构建平台引用失败: %w", err)
	}

	platformDesc, err := remote.Get(platformRef, options...)
	if err != nil {
		return nil, fmt.Errorf("获取平台manifest失败: %w", err)
	}

	// 递归获取单架构配置
	return getSingleImageConfig(ctx, platformDesc, configData, options)
}

// getTagFromRef 从引用中提取标签
func getTagFromRef(ref name.Reference) string {
	if tag, ok := ref.(name.Tag); ok {
		return tag.TagStr()
	}
	if digest, ok := ref.(name.Digest); ok {
		return digest.DigestStr()
	}
	return "latest"
}

// RegisterImageConfigRoutes 注册镜像配置路由
func RegisterImageConfigRoutes(r *gin.Engine) {
	// 镜像配置查询路由
	r.GET("/config/:image", HandleImageConfig)

	// 支持带标签的查询
	r.GET("/config/:image/:tag", func(c *gin.Context) {
		image := c.Param("image")
		tag := c.Param("tag")

		// 重定向到主处理器，传递标签参数
		c.Request.URL.RawQuery = fmt.Sprintf("tag=%s", tag)
		c.Params = gin.Params{{Key: "image", Value: image}}
		HandleImageConfig(c)
	})
}
