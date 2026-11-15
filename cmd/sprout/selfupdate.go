package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/shumengya/sprout/internal/agent/platform"
)

// maxArchiveSize 是下载压缩包的上限。当前产物十几 MB，留足余量的同时防止异常响应把磁盘写满。
const maxArchiveSize = 200 << 20

// checksumsName 是 tools/release 生成、随 Release 上传的校验文件。
const checksumsName = "SHA256SUMS.txt"

// updatePlan 是一次检查的结论：有没有新版本、该下载哪个产物。
type updatePlan struct {
	Current, Latest string
	PageURL         string
	AssetName       string
	AssetURL        string
	ChecksumsURL    string
	// Newer 为 true 表示可以更新；开发构建、已是最新、本地更新时为 false。
	Newer bool
	// Dev 表示当前是开发构建，无法比较版本。
	Dev bool
}

// checkUpdate 查询最新发布并和当前版本比较，不下载任何东西。
func checkUpdate(version, repo string, client *http.Client) (*updatePlan, error) {
	if repo == "" {
		repo = releaseRepo
	}
	rel, err := fetchLatestRelease(repo, client)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(rel.Tag) == "" {
		return nil, errors.New("最新发布没有版本号，无法比较")
	}
	p := &updatePlan{Current: version, Latest: rel.Tag, PageURL: rel.URL}
	p.AssetName, p.AssetURL = platformAsset(rel)
	for _, a := range rel.Assets {
		if a.Name == checksumsName {
			p.ChecksumsURL = a.URL
		}
	}
	switch {
	case !isReleaseVersion(version):
		p.Dev = true
	case compareVersions(version, rel.Tag) < 0:
		p.Newer = true
	}
	return p, nil
}

// installUpdate 下载本平台产物、校验 SHA-256、解压出程序、试运行，再替换 exePath。
// 任何一步失败都不动原程序；progress 用来报告进度，可为 nil。
func installUpdate(ctx context.Context, p *updatePlan, exePath string, client *http.Client, progress func(string)) error {
	if progress == nil {
		progress = func(string) {}
	}
	switch {
	case p.AssetURL == "":
		return fmt.Errorf("发布 %s 里没有本平台（%s）的产物，请到发布页手动下载", p.Latest, platformName())
	case p.ChecksumsURL == "":
		// 没有校验文件就不替换：从网络拿一个程序覆盖自己，必须能确认它没坏、没被换。
		return fmt.Errorf("发布 %s 没有附带 %s，无法校验，已放弃自动更新", p.Latest, checksumsName)
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	// 先确认能写：装在需要管理员权限的目录里时，下载十几 MB 之后才失败太浪费。
	if err := platform.CheckWritable(exePath); err != nil {
		return err
	}

	progress("下载校验文件…")
	sums, err := download(ctx, client, p.ChecksumsURL, 1<<20)
	if err != nil {
		return fmt.Errorf("下载 %s 失败: %w", checksumsName, err)
	}
	want, ok := lookupChecksum(sums, p.AssetName)
	if !ok {
		return fmt.Errorf("%s 里没有 %s 的校验值，已放弃自动更新", checksumsName, p.AssetName)
	}

	progress("下载 " + p.AssetName + "…")
	archive, err := download(ctx, client, p.AssetURL, maxArchiveSize)
	if err != nil {
		return fmt.Errorf("下载 %s 失败: %w", p.AssetName, err)
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, want) {
		return fmt.Errorf("%s 校验失败：期望 %s，实际 %s；文件可能损坏或被篡改，已放弃更新", p.AssetName, want, got)
	}

	progress("解压…")
	bin, err := extractBinary(p.AssetName, archive, filepath.Base(exePath))
	if err != nil {
		return err
	}

	// 写到程序同目录的临时文件：同一文件系统上替换才是一次原子 rename。
	tmp, err := os.CreateTemp(filepath.Dir(exePath), ".sprout-update-*"+filepath.Ext(exePath))
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return fmt.Errorf("写入新程序失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("写入新程序失败: %w", err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return fmt.Errorf("设置可执行权限失败: %w", err)
	}

	// 试运行：能打印版本号才说明这个程序在本机跑得起来（架构对、没被截断）。
	// 比校验和更进一步——校验和只证明下载的是发布的那个文件。
	progress("验证新程序…")
	if err := smokeTest(ctx, tmpName); err != nil {
		return err
	}

	progress("替换程序…")
	if err := platform.ReplaceExecutable(exePath, tmpName); err != nil {
		return err
	}
	keep = true // 已经被 rename 成正式程序
	return nil
}

// download 读取一个 URL，超过 limit 字节直接报错，不截断。
func download(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sprout")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("文件超过 %d MB 上限", limit>>20)
	}
	return data, nil
}

// lookupChecksum 从 sha256sum 格式（"<hex>  <文件名>"，文件名前可能带 * 表示二进制模式）里找某个文件。
func lookupChecksum(sums []byte, name string) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == name {
			return fields[0], true
		}
	}
	return "", false
}

// extractBinary 从压缩包里取出程序文件。只认顶层目录下名为 binName 的那一个，
// 不按包内路径写盘，所以不存在 ../ 之类的路径穿越问题。
func extractBinary(assetName string, archive []byte, binName string) ([]byte, error) {
	match := func(name string) bool {
		name = strings.TrimPrefix(path.Clean("/"+name), "/")
		dir, base := path.Split(name)
		return base == binName && strings.Count(dir, "/") <= 1
	}
	if strings.HasSuffix(assetName, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, fmt.Errorf("解压 %s 失败: %w", assetName, err)
		}
		for _, f := range zr.File {
			if f.FileInfo().IsDir() || !match(f.Name) {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("解压 %s 失败: %w", f.Name, err)
			}
			defer rc.Close()
			return readAllLimited(rc, f.Name)
		}
		return nil, fmt.Errorf("%s 里没有 %s", assetName, binName)
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("解压 %s 失败: %w", assetName, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s 里没有 %s", assetName, binName)
		}
		if err != nil {
			return nil, fmt.Errorf("解压 %s 失败: %w", assetName, err)
		}
		if h.Typeflag == tar.TypeReg && match(h.Name) {
			return readAllLimited(tr, h.Name)
		}
	}
}

func readAllLimited(r io.Reader, name string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxArchiveSize+1))
	if err != nil {
		return nil, fmt.Errorf("解压 %s 失败: %w", name, err)
	}
	if len(data) > maxArchiveSize {
		return nil, fmt.Errorf("%s 超过 %d MB 上限", name, maxArchiveSize>>20)
	}
	return data, nil
}

// smokeTest 运行一次 `新程序 --version`。测试里替换成替身，避免真去执行。
var smokeTest = func(ctx context.Context, exe string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("新程序无法运行（%v），已放弃更新；输出: %s", err, firstNonEmptyLine(string(out)))
	}
	if !strings.HasPrefix(strings.TrimSpace(string(out)), "sprout ") {
		return fmt.Errorf("新程序的版本输出不符合预期（%s），已放弃更新", firstNonEmptyLine(string(out)))
	}
	return nil
}
