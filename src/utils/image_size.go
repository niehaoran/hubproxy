package utils

import (
	"encoding/json"
	"fmt"
	"strings"

	"hubproxy/config"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// ImageSizeInfo 镜像大小信息
type ImageSizeInfo struct {
	TotalSize        int64   `json:"total_size"`        // 总大小（压缩）
	UncompressedSize int64   `json:"uncompressed_size"` // 解压后大小
	LayerCount       int     `json:"layer_count"`       // 层数量
	LayerSizes       []int64 `json:"layer_sizes"`       // 各层大小
	IsMultiArch      bool    `json:"is_multi_arch"`     // 是否多架构
}

// CheckImageSize 检查镜像大小是否超过限制
func CheckImageSize(imageRef, reference string, options []remote.Option) (allowed bool, sizeInfo *ImageSizeInfo, reason string) {
	cfg := config.GetConfig()

	// 如果未启用大小检查，直接允许
	if !cfg.Docker.SizeCheckEnabled {
		return true, nil, ""
	}

	var ref name.Reference
	var err error

	if strings.HasPrefix(reference, "sha256:") {
		ref, err = name.NewDigest(fmt.Sprintf("%s@%s", imageRef, reference))
	} else {
		ref, err = name.NewTag(fmt.Sprintf("%s:%s", imageRef, reference))
	}

	if err != nil {
		return false, nil, fmt.Sprintf("解析镜像引用失败: %v", err)
	}

	// 获取镜像描述符
	desc, err := remote.Get(ref, options...)
	if err != nil {
		return false, nil, fmt.Sprintf("获取镜像信息失败: %v", err)
	}

	sizeInfo = &ImageSizeInfo{}

	switch desc.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		// 多架构镜像
		return checkMultiArchImageSize(desc, cfg.Docker.MaxImageSize, sizeInfo)
	case types.OCIManifestSchema1, types.DockerManifestSchema2:
		// 单架构镜像
		return checkSingleImageSize(desc, cfg.Docker.MaxImageSize, sizeInfo)
	default:
		return checkSingleImageSize(desc, cfg.Docker.MaxImageSize, sizeInfo)
	}
}

// checkSingleImageSize 检查单架构镜像大小
func checkSingleImageSize(desc *remote.Descriptor, maxSize int64, sizeInfo *ImageSizeInfo) (bool, *ImageSizeInfo, string) {
	sizeInfo.IsMultiArch = false

	// 解析manifest
	var manifest struct {
		Config struct {
			Size int64 `json:"size"`
		} `json:"config"`
		Layers []struct {
			Size int64 `json:"size"`
		} `json:"layers"`
	}

	if err := json.Unmarshal(desc.Manifest, &manifest); err != nil {
		return false, sizeInfo, fmt.Sprintf("解析manifest失败: %v", err)
	}

	// 计算总大小
	totalSize := manifest.Config.Size
	sizeInfo.LayerCount = len(manifest.Layers)
	sizeInfo.LayerSizes = make([]int64, len(manifest.Layers))

	for i, layer := range manifest.Layers {
		totalSize += layer.Size
		sizeInfo.LayerSizes[i] = layer.Size
	}

	sizeInfo.TotalSize = totalSize

	if totalSize > maxSize {
		return false, sizeInfo, fmt.Sprintf("镜像大小 %s 超过限制 %s",
			FormatBytes(totalSize), FormatBytes(maxSize))
	}

	return true, sizeInfo, ""
}

// checkMultiArchImageSize 检查多架构镜像大小
func checkMultiArchImageSize(desc *remote.Descriptor, maxSize int64, sizeInfo *ImageSizeInfo) (bool, *ImageSizeInfo, string) {
	sizeInfo.IsMultiArch = true

	// 解析manifest list
	var manifestList struct {
		Manifests []struct {
			Size     int64 `json:"size"`
			Platform struct {
				Architecture string `json:"architecture"`
				OS           string `json:"os"`
			} `json:"platform"`
		} `json:"manifests"`
	}

	if err := json.Unmarshal(desc.Manifest, &manifestList); err != nil {
		return false, sizeInfo, fmt.Sprintf("解析manifest list失败: %v", err)
	}

	// 计算所有架构的总大小
	var totalSize int64
	sizeInfo.LayerCount = len(manifestList.Manifests)
	sizeInfo.LayerSizes = make([]int64, len(manifestList.Manifests))

	for i, manifest := range manifestList.Manifests {
		totalSize += manifest.Size
		sizeInfo.LayerSizes[i] = manifest.Size
	}

	sizeInfo.TotalSize = totalSize

	if totalSize > maxSize {
		return false, sizeInfo, fmt.Sprintf("多架构镜像总大小 %s 超过限制 %s",
			FormatBytes(totalSize), FormatBytes(maxSize))
	}

	return true, sizeInfo, ""
}

// FormatBytes 格式化字节大小为可读格式
func FormatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// GetImageSizeInfo 仅获取镜像大小信息（不检查限制）
func GetImageSizeInfo(imageRef, reference string, options []remote.Option) (*ImageSizeInfo, error) {
	var ref name.Reference
	var err error

	if strings.HasPrefix(reference, "sha256:") {
		ref, err = name.NewDigest(fmt.Sprintf("%s@%s", imageRef, reference))
	} else {
		ref, err = name.NewTag(fmt.Sprintf("%s:%s", imageRef, reference))
	}

	if err != nil {
		return nil, fmt.Errorf("解析镜像引用失败: %v", err)
	}

	desc, err := remote.Get(ref, options...)
	if err != nil {
		return nil, fmt.Errorf("获取镜像信息失败: %v", err)
	}

	sizeInfo := &ImageSizeInfo{}

	switch desc.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		_, sizeInfo, _ = checkMultiArchImageSize(desc, 0, sizeInfo) // 不检查限制
	default:
		_, sizeInfo, _ = checkSingleImageSize(desc, 0, sizeInfo) // 不检查限制
	}

	return sizeInfo, nil
}
