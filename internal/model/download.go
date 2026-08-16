package model

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Progress struct {
	Downloaded, Total int64
	BytesPerSecond    float64
}
type DownloadOptions struct {
	Client   *http.Client
	Retries  int
	Progress func(Progress)
}

func Download(ctx context.Context, m Meta, dir string, legacy func(int64, int64)) error {
	return DownloadWithOptions(ctx, m, dir, DownloadOptions{Retries: 2, Progress: func(p Progress) {
		if legacy != nil {
			legacy(p.Downloaded, p.Total)
		}
	}})
}

func DownloadWithOptions(ctx context.Context, m Meta, dir string, opt DownloadOptions) error {
	if opt.Client == nil {
		opt.Client = &http.Client{Timeout: 0}
	}
	if opt.Retries < 0 {
		opt.Retries = 0
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	info, err := ValidateURLInfo(ctx, opt.Client, m.URL)
	if err != nil {
		return fmt.Errorf("下载链接无效: %w", err)
	}
	if m.ArchiveBytes > 0 && info.Size > 0 && info.Size != m.ArchiveBytes {
		return fmt.Errorf("官方模型压缩包大小异常: 得到 %d，预期 %d 字节", info.Size, m.ArchiveBytes)
	}
	part := filepath.Join(dir, m.ID+".tar.bz2.part")
	for attempt := 0; ; attempt++ {
		err = downloadAttempt(ctx, opt.Client, m.URL, part, info, opt.Progress)
		if err == nil || attempt >= opt.Retries || ctx.Err() != nil {
			break
		}
		t := time.NewTimer(time.Duration(attempt+1) * 500 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	if err != nil {
		return fmt.Errorf("下载模型失败: %w", err)
	}
	if err = verifySHA(part, m.SHA256); err != nil {
		return err
	}
	if err = extractAtomic(part, filepath.Join(dir, m.ID)); err != nil {
		return err
	}
	if err = ValidateInstalled(m, dir); err != nil {
		_ = os.RemoveAll(filepath.Join(dir, m.ID))
		return err
	}
	return os.Rename(part, filepath.Join(dir, m.ID+".tar.bz2"))
}

func downloadAttempt(ctx context.Context, c *http.Client, url, part string, info URLInfo, progress func(Progress)) error {
	var start int64
	if st, err := os.Stat(part); err == nil && info.RangeSupported {
		start = st.Size()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if start > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))
	}
	r, err := c.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK && r.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("HTTP %s", r.Status)
	}
	if r.StatusCode == http.StatusOK {
		start = 0
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if start == 0 {
		if err = f.Truncate(0); err != nil {
			return err
		}
	}
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		return err
	}
	total := info.Size
	if total <= 0 {
		total = start + r.ContentLength
	}
	done, began, last := start, time.Now(), time.Now()
	buf := make([]byte, 256*1024)
	for {
		n, readErr := r.Body.Read(buf)
		if n > 0 {
			if _, err = f.Write(buf[:n]); err != nil {
				return err
			}
			done += int64(n)
			if progress != nil && (time.Since(last) >= 200*time.Millisecond || done == total) {
				progress(Progress{done, total, float64(done-start) / time.Since(began).Seconds()})
				last = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if total > 0 && done != total {
		return fmt.Errorf("下载不完整: %d/%d 字节", done, total)
	}
	if progress != nil {
		progress(Progress{done, total, float64(done-start) / time.Since(began).Seconds()})
	}
	return f.Sync()
}

func extractAtomic(archive, target string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	return extractTar(tar.NewReader(bzip2.NewReader(f)), target)
}

func extractTar(tr *tar.Reader, target string) error {
	var err error
	stage := target + ".extracting"
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	if err := os.MkdirAll(stage, 0755); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(stage)
		}
	}()
	files := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		rawName := filepath.FromSlash(h.Name)
		if filepath.IsAbs(rawName) {
			return fmt.Errorf("压缩包包含绝对路径 %q", h.Name)
		}
		for _, part := range strings.Split(rawName, string(os.PathSeparator)) {
			if part == ".." {
				return fmt.Errorf("压缩包包含路径穿越 %q", h.Name)
			}
		}
		name := filepath.Clean(rawName)
		parts := strings.Split(name, string(os.PathSeparator))
		if len(parts) > 1 {
			name = filepath.Join(parts[1:]...)
		} else {
			continue
		}
		if name == "." || filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("压缩包包含非法路径 %q", h.Name)
		}
		dst := filepath.Join(stage, name)
		rel, _ := filepath.Rel(stage, dst)
		if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("压缩包路径越界 %q", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(dst, 0755)
		case tar.TypeReg, tar.TypeRegA:
			if err = os.MkdirAll(filepath.Dir(dst), 0755); err == nil {
				var out *os.File
				out, err = os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, h.FileInfo().Mode().Perm())
				if err == nil {
					_, err = io.Copy(out, io.LimitReader(tr, h.Size+1))
					closeErr := out.Close()
					if err == nil {
						err = closeErr
					}
					files++
				}
			}
		default:
			continue
		}
		if err != nil {
			return err
		}
	}
	if files == 0 {
		return fmt.Errorf("模型压缩包为空")
	}
	if _, err = os.Stat(target); err == nil {
		return fmt.Errorf("目标模型目录已存在: %s", target)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = os.Rename(stage, target); err != nil {
		return err
	}
	ok = true
	return nil
}
