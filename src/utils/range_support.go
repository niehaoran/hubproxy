package utils

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// RangeInfo 存储Range请求信息
type RangeInfo struct {
	Start  int64
	End    int64
	Length int64
	Valid  bool
}

// ParseRangeHeader 解析Range请求头
func ParseRangeHeader(rangeHeader string, contentLength int64) *RangeInfo {
	if rangeHeader == "" {
		return &RangeInfo{Valid: false}
	}

	// 只支持bytes范围请求
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return &RangeInfo{Valid: false}
	}

	rangeSpec := strings.TrimPrefix(rangeHeader, "bytes=")

	// 支持单个范围，格式: start-end 或 start- 或 -suffix
	if strings.Contains(rangeSpec, ",") {
		// 暂不支持多范围请求
		return &RangeInfo{Valid: false}
	}

	var start, end int64
	var err error

	if strings.HasPrefix(rangeSpec, "-") {
		// 后缀范围: -500 (最后500字节)
		suffix, err := strconv.ParseInt(strings.TrimPrefix(rangeSpec, "-"), 10, 64)
		if err != nil || suffix <= 0 {
			return &RangeInfo{Valid: false}
		}
		start = contentLength - suffix
		if start < 0 {
			start = 0
		}
		end = contentLength - 1
	} else if strings.HasSuffix(rangeSpec, "-") {
		// 前缀范围: 500- (从500字节到结尾)
		start, err = strconv.ParseInt(strings.TrimSuffix(rangeSpec, "-"), 10, 64)
		if err != nil || start < 0 {
			return &RangeInfo{Valid: false}
		}
		end = contentLength - 1
	} else if strings.Contains(rangeSpec, "-") {
		// 完整范围: 500-999
		parts := strings.Split(rangeSpec, "-")
		if len(parts) != 2 {
			return &RangeInfo{Valid: false}
		}
		start, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil || start < 0 {
			return &RangeInfo{Valid: false}
		}
		end, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || end < start {
			return &RangeInfo{Valid: false}
		}
	} else {
		return &RangeInfo{Valid: false}
	}

	// 验证范围
	if start >= contentLength || end >= contentLength || start > end {
		return &RangeInfo{Valid: false}
	}

	return &RangeInfo{
		Start:  start,
		End:    end,
		Length: end - start + 1,
		Valid:  true,
	}
}

// SetRangeHeaders 设置Range响应头
func SetRangeHeaders(w http.ResponseWriter, rangeInfo *RangeInfo, contentLength int64, contentType string) {
	if !rangeInfo.Valid {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", contentLength))
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rangeInfo.Start, rangeInfo.End, contentLength))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", rangeInfo.Length))
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(http.StatusPartialContent)
}

// CopyRange 复制指定范围的数据
func CopyRange(dst io.Writer, src io.Reader, rangeInfo *RangeInfo) error {
	if !rangeInfo.Valid {
		_, err := io.Copy(dst, src)
		return err
	}

	// 跳过start之前的数据
	if rangeInfo.Start > 0 {
		_, err := io.CopyN(io.Discard, src, rangeInfo.Start)
		if err != nil {
			return fmt.Errorf("跳过数据失败: %v", err)
		}
	}

	// 复制指定长度的数据
	_, err := io.CopyN(dst, src, rangeInfo.Length)
	return err
}

// CreateRangeRequest 创建带Range头的HTTP请求
func CreateRangeRequest(method, url string, rangeInfo *RangeInfo, originalReq *http.Request) (*http.Request, error) {
	req, err := http.NewRequest(method, url, originalReq.Body)
	if err != nil {
		return nil, err
	}

	// 复制原始请求头
	for key, values := range originalReq.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// 添加Range头
	if rangeInfo.Valid {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", rangeInfo.Start, rangeInfo.End))
	}

	req.Header.Del("Host")
	return req, nil
}
