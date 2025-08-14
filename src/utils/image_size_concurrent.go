package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"hubproxy/config"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// ConcurrentImageSizeChecker 并发镜像大小检查器
type ConcurrentImageSizeChecker struct {
	maxWorkers int
	timeout    time.Duration
}

// LayerSizeResult 层大小计算结果
type LayerSizeResult struct {
	Index int
	Size  int64
	Error error
}

// ManifestSizeResult Manifest大小计算结果
type ManifestSizeResult struct {
	Index      int
	Size       int64
	LayerCount int
	LayerSizes []int64
	Platform   string
	Error      error
}

// NewConcurrentImageSizeChecker 创建并发大小检查器
func NewConcurrentImageSizeChecker(maxWorkers int, timeout time.Duration) *ConcurrentImageSizeChecker {
	if maxWorkers <= 0 {
		maxWorkers = 10 // 默认10个并发工作者
	}
	if timeout <= 0 {
		timeout = 30 * time.Second // 默认30秒超时
	}

	return &ConcurrentImageSizeChecker{
		maxWorkers: maxWorkers,
		timeout:    timeout,
	}
}

// CheckImageSizeConcurrent 并发检查镜像大小
func (c *ConcurrentImageSizeChecker) CheckImageSizeConcurrent(
	ctx context.Context,
	imageRef, reference string,
	options []remote.Option,
) (allowed bool, sizeInfo *ImageSizeInfo, reason string) {
	cfg := config.GetConfig()

	// 如果未启用大小检查，直接允许
	if !cfg.Docker.SizeCheckEnabled {
		return true, nil, ""
	}

	// 创建带超时的context
	timeoutCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

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
	contextOptions := append(options, remote.WithContext(timeoutCtx))
	desc, err := remote.Get(ref, contextOptions...)
	if err != nil {
		return false, nil, fmt.Sprintf("获取镜像信息失败: %v", err)
	}

	sizeInfo = &ImageSizeInfo{}

	switch desc.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		// 多架构镜像并发处理
		return c.checkMultiArchImageSizeConcurrent(timeoutCtx, desc, cfg.Docker.MaxImageSize, sizeInfo, contextOptions)
	case types.OCIManifestSchema1, types.DockerManifestSchema2:
		// 单架构镜像并发处理
		return c.checkSingleImageSizeConcurrent(timeoutCtx, desc, cfg.Docker.MaxImageSize, sizeInfo, contextOptions)
	default:
		return c.checkSingleImageSizeConcurrent(timeoutCtx, desc, cfg.Docker.MaxImageSize, sizeInfo, contextOptions)
	}
}

// checkSingleImageSizeConcurrent 并发检查单架构镜像大小
func (c *ConcurrentImageSizeChecker) checkSingleImageSizeConcurrent(
	ctx context.Context,
	desc *remote.Descriptor,
	maxSize int64,
	sizeInfo *ImageSizeInfo,
	options []remote.Option,
) (bool, *ImageSizeInfo, string) {
	sizeInfo.IsMultiArch = false

	// 解析manifest
	var manifest struct {
		Config struct {
			Size int64 `json:"size"`
		} `json:"config"`
		Layers []struct {
			Size   int64  `json:"size"`
			Digest string `json:"digest"`
		} `json:"layers"`
	}

	if err := json.Unmarshal(desc.Manifest, &manifest); err != nil {
		return false, sizeInfo, fmt.Sprintf("解析manifest失败: %v", err)
	}

	layerCount := len(manifest.Layers)
	sizeInfo.LayerCount = layerCount
	sizeInfo.LayerSizes = make([]int64, layerCount)

	// 如果层数较少，直接计算
	if layerCount <= 3 {
		totalSize := manifest.Config.Size
		for i, layer := range manifest.Layers {
			totalSize += layer.Size
			sizeInfo.LayerSizes[i] = layer.Size
		}
		sizeInfo.TotalSize = totalSize
	} else {
		// 并发计算层大小
		totalSize := c.calculateLayerSizesConcurrent(ctx, manifest.Layers, sizeInfo.LayerSizes)
		totalSize += manifest.Config.Size
		sizeInfo.TotalSize = totalSize
	}

	if sizeInfo.TotalSize > maxSize {
		return false, sizeInfo, fmt.Sprintf("镜像大小 %s 超过限制 %s",
			FormatBytes(sizeInfo.TotalSize), FormatBytes(maxSize))
	}

	return true, sizeInfo, ""
}

// calculateLayerSizesConcurrent 并发计算层大小
func (c *ConcurrentImageSizeChecker) calculateLayerSizesConcurrent(
	ctx context.Context,
	layers []struct {
		Size   int64  `json:"size"`
		Digest string `json:"digest"`
	},
	layerSizes []int64,
) int64 {
	layerCount := len(layers)
	if layerCount == 0 {
		return 0
	}

	// 创建工作者池
	jobs := make(chan int, layerCount)
	results := make(chan LayerSizeResult, layerCount)

	// 启动工作者goroutines
	workerCount := c.maxWorkers
	if workerCount > layerCount {
		workerCount = layerCount
	}

	var wg sync.WaitGroup
	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case layerIndex, ok := <-jobs:
					if !ok {
						return
					}
					// 直接使用manifest中的大小信息，避免额外网络请求
					results <- LayerSizeResult{
						Index: layerIndex,
						Size:  layers[layerIndex].Size,
						Error: nil,
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// 发送任务
	go func() {
		defer close(jobs)
		for i := 0; i < layerCount; i++ {
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()

	// 等待所有工作者完成
	go func() {
		wg.Wait()
		close(results)
	}()

	// 收集结果
	var totalSize int64
	processedCount := 0

	for result := range results {
		if result.Error != nil {
			continue // 忽略错误，使用manifest中的大小
		}
		layerSizes[result.Index] = result.Size
		totalSize += result.Size
		processedCount++
	}

	return totalSize
}

// checkMultiArchImageSizeConcurrent 并发检查多架构镜像大小
func (c *ConcurrentImageSizeChecker) checkMultiArchImageSizeConcurrent(
	ctx context.Context,
	desc *remote.Descriptor,
	maxSize int64,
	sizeInfo *ImageSizeInfo,
	options []remote.Option,
) (bool, *ImageSizeInfo, string) {
	sizeInfo.IsMultiArch = true

	// 解析manifest list
	var manifestList struct {
		Manifests []struct {
			Size     int64  `json:"size"`
			Digest   string `json:"digest"`
			Platform struct {
				Architecture string `json:"architecture"`
				OS           string `json:"os"`
				Variant      string `json:"variant,omitempty"`
			} `json:"platform"`
		} `json:"manifests"`
	}

	if err := json.Unmarshal(desc.Manifest, &manifestList); err != nil {
		return false, sizeInfo, fmt.Sprintf("解析manifest list失败: %v", err)
	}

	manifestCount := len(manifestList.Manifests)
	sizeInfo.LayerCount = manifestCount
	sizeInfo.LayerSizes = make([]int64, manifestCount)

	// 如果架构较少，直接计算
	if manifestCount <= 2 {
		var totalSize int64
		for i, manifest := range manifestList.Manifests {
			totalSize += manifest.Size
			sizeInfo.LayerSizes[i] = manifest.Size
		}
		sizeInfo.TotalSize = totalSize
	} else {
		// 并发处理多个架构的manifest
		totalSize := c.calculateMultiArchSizesConcurrent(ctx, manifestList.Manifests, sizeInfo.LayerSizes, options)
		sizeInfo.TotalSize = totalSize
	}

	if sizeInfo.TotalSize > maxSize {
		return false, sizeInfo, fmt.Sprintf("多架构镜像总大小 %s 超过限制 %s",
			FormatBytes(sizeInfo.TotalSize), FormatBytes(maxSize))
	}

	return true, sizeInfo, ""
}

// calculateMultiArchSizesConcurrent 并发计算多架构大小
func (c *ConcurrentImageSizeChecker) calculateMultiArchSizesConcurrent(
	ctx context.Context,
	manifests []struct {
		Size     int64  `json:"size"`
		Digest   string `json:"digest"`
		Platform struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
			Variant      string `json:"variant,omitempty"`
		} `json:"platform"`
	},
	manifestSizes []int64,
	options []remote.Option,
) int64 {
	manifestCount := len(manifests)
	if manifestCount == 0 {
		return 0
	}

	// 创建工作者池
	jobs := make(chan int, manifestCount)
	results := make(chan ManifestSizeResult, manifestCount)

	// 启动工作者goroutines
	workerCount := c.maxWorkers
	if workerCount > manifestCount {
		workerCount = manifestCount
	}

	var wg sync.WaitGroup
	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case manifestIndex, ok := <-jobs:
					if !ok {
						return
					}

					manifest := manifests[manifestIndex]
					platform := fmt.Sprintf("%s/%s", manifest.Platform.OS, manifest.Platform.Architecture)
					if manifest.Platform.Variant != "" {
						platform += "/" + manifest.Platform.Variant
					}

					// 对于多架构镜像，通常使用manifest list中的大小信息就足够了
					// 避免为每个架构都获取详细的manifest，这样可以大大提升性能
					results <- ManifestSizeResult{
						Index:      manifestIndex,
						Size:       manifest.Size,
						LayerCount: 1, // manifest list中每个条目算作一层
						Platform:   platform,
						Error:      nil,
					}

				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// 发送任务
	go func() {
		defer close(jobs)
		for i := 0; i < manifestCount; i++ {
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()

	// 等待所有工作者完成
	go func() {
		wg.Wait()
		close(results)
	}()

	// 收集结果
	var totalSize int64
	processedCount := 0

	for result := range results {
		if result.Error != nil {
			continue // 忽略错误的架构
		}
		manifestSizes[result.Index] = result.Size
		totalSize += result.Size
		processedCount++
	}

	return totalSize
}

// 全局并发检查器实例
var globalConcurrentChecker *ConcurrentImageSizeChecker

// InitConcurrentImageSizeChecker 初始化并发检查器
func InitConcurrentImageSizeChecker() {
	cfg := config.GetConfig()

	if !cfg.Docker.SizeCheckEnabled {
		return
	}

	// 从配置中读取参数
	maxWorkers := cfg.Docker.ConcurrentWorkers
	if maxWorkers <= 0 {
		maxWorkers = 10 // 默认10个工作者
	}

	timeout := 30 * time.Second // 默认超时
	if cfg.Docker.CheckTimeout != "" {
		if parsedTimeout, err := time.ParseDuration(cfg.Docker.CheckTimeout); err == nil {
			timeout = parsedTimeout
		}
	}

	globalConcurrentChecker = NewConcurrentImageSizeChecker(maxWorkers, timeout)
	fmt.Printf("🚀 并发大小检查器已启用: %d工作者, %v超时\n", maxWorkers, timeout)
}

// CheckImageSizeFast 快速检查镜像大小（使用并发版本）
func CheckImageSizeFast(ctx context.Context, imageRef, reference string, options []remote.Option) (allowed bool, sizeInfo *ImageSizeInfo, reason string) {
	if globalConcurrentChecker == nil {
		// 回退到原始实现
		return CheckImageSize(imageRef, reference, options)
	}

	return globalConcurrentChecker.CheckImageSizeConcurrent(ctx, imageRef, reference, options)
}
