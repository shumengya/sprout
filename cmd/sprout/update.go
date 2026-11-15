package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shumengya/sprout/internal/agent/platform"
	"github.com/shumengya/sprout/internal/cli"
)

// releaseRepo 是默认的更新来源：本仓库在 GitHub 上的 Releases。
const releaseRepo = "shumengya/sprout"

// releaseURL 返回查询最新 release 的地址。测试替换它指向 httptest，避免打真实网络。
var releaseURL = func(repo string) string {
	return "https://api.github.com/repos/" + repo + "/releases/latest"
}

// releaseInfo 是 GitHub Release API 里用到的字段。
type releaseInfo struct {
	Tag    string `json:"tag_name"`
	URL    string `json:"html_url"`
	Name   string `json:"name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// runUpdate 实现 `sprout update [--repo 仓库] [--check]`：查询最新发布，
// 默认下载本平台产物、校验 SHA-256、试运行后替换当前程序；--check 只报告不下载。
func runUpdate(version, repo string, client *http.Client, checkOnly bool, stdout, stderr io.Writer) int {
	plan, err := checkUpdate(version, repo, client)
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return cli.ExitError
	}
	fmt.Fprintf(stdout, "当前版本: %s\n最新版本: %s\n", plan.Current, plan.Latest)
	switch {
	case plan.Dev:
		fmt.Fprintf(stdout, "\n当前是开发构建（%s），无法和发布版本比较；最新发布是 %s。\n", plan.Current, plan.Latest)
	case !plan.Newer:
		if compareVersions(plan.Current, plan.Latest) > 0 {
			// 本地比最新发布还新：通常是自己打的、还没上传的构建，不该劝用户“降级”。
			fmt.Fprintln(stdout, "当前版本比最新发布还新，无需更新。")
		} else {
			fmt.Fprintln(stdout, "已是最新版本，无需更新。")
		}
		return 0
	default:
		fmt.Fprintf(stdout, "\n有新版本可用：%s（当前 %s）\n", plan.Latest, plan.Current)
	}
	return reportOrInstall(plan, client, checkOnly, stdout, stderr)
}

// reportOrInstall 打印下载地址，或者在版本确实更新时执行安装。
func reportOrInstall(plan *updatePlan, client *http.Client, checkOnly bool, stdout, stderr io.Writer) int {
	if plan.PageURL != "" {
		fmt.Fprintln(stdout, "发布页:", plan.PageURL)
	}
	if !plan.Newer {
		// 开发构建或已是最新：只报告地址，绝不动程序。
		if plan.AssetName != "" {
			fmt.Fprintf(stdout, "本平台（%s）下载: %s\n  %s\n", platformName(), plan.AssetName, plan.AssetURL)
		}
		fmt.Fprintln(stdout, "下载后替换现有程序即可；数据目录（配置、会话）不受影响。")
		return 0
	}
	if checkOnly {
		if plan.AssetName != "" {
			fmt.Fprintf(stdout, "本平台（%s）下载: %s\n  %s\n", platformName(), plan.AssetName, plan.AssetURL)
		} else {
			fmt.Fprintln(stdout, "没有找到与当前平台匹配的产物，请到发布页手动选择。")
		}
		fmt.Fprintln(stdout, "加 --check 之外的默认行为是自动下载并替换；也可以按上面的地址手动下载。")
		return 0
	}

	exe, err := platform.ResolveExecutable()
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return cli.ExitError
	}
	if err := installUpdate(context.Background(), plan, exe, client, func(s string) { fmt.Fprintln(stdout, s) }); err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return cli.ExitError
	}
	fmt.Fprintf(stdout, "已更新到 %s。重新启动 sprout 后生效。\n", plan.Latest)
	return 0
}

// fetchLatestRelease 读取 GitHub 的最新发布。未认证请求有速率限制，平时够用。
func fetchLatestRelease(repo string, client *http.Client) (*releaseInfo, error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequest(http.MethodGet, releaseURL(repo), nil)
	if err != nil {
		return nil, err
	}
	// GitHub API 要求带 User-Agent，否则直接 403。
	req.Header.Set("User-Agent", "sprout")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("查询 %s 的发布失败: %w", repo, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取发布信息失败: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("仓库 %s 还没有任何发布", repo)
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, fmt.Errorf("查询发布被 GitHub 限流（HTTP %d），稍后再试", resp.StatusCode)
	default:
		return nil, fmt.Errorf("查询发布失败（HTTP %d）: %s", resp.StatusCode, firstNonEmptyLine(string(body)))
	}
	var rel releaseInfo
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("解析发布信息失败: %w", err)
	}
	return &rel, nil
}

// platformName 返回当前二进制对应的发布产物平台名，与 tools/release 的命名一致。
func platformName() string {
	switch {
	case runtime.GOOS == "android" && runtime.GOARCH == "arm64":
		return "termux_arm64"
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm":
		return "linux_armv7"
	default:
		return runtime.GOOS + "_" + runtime.GOARCH
	}
}

// platformAsset 在发布产物里找当前平台的压缩包。名字形如 sprout_2026.11.12_windows_amd64.zip。
func platformAsset(rel *releaseInfo) (name, url string) {
	want := platformName()
	for _, a := range rel.Assets {
		if strings.Contains(a.Name, "_"+want+".") {
			return a.Name, a.URL
		}
	}
	return "", ""
}

// releaseVersionRe 与 tools/release 的版本号格式一致：日期，可带 -rc.1 这类后缀。
var releaseVersionRe = regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}(-[0-9A-Za-z][0-9A-Za-z.]*)?$`)

// isReleaseVersion 判断是不是发布构建；go run / 本地 go build 出来的是 "dev"。
func isReleaseVersion(v string) bool {
	return releaseVersionRe.MatchString(normalizeVersion(v))
}

// compareVersions 返回 -1 / 0 / 1：忽略前导 v，日期逐段比数值，同日期时正式版比预发布版新。
func compareVersions(a, b string) int {
	a, b = normalizeVersion(a), normalizeVersion(b)
	ac, ap := splitVersion(a)
	bc, bp := splitVersion(b)
	if c := compareParts(ac, bc); c != 0 {
		return c
	}
	// 日期相同：正式版比预发布版新（2026.11.12 > 2026.11.12-rc.1）。
	switch {
	case ap == "" && bp == "":
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	}
	return compareParts(ap, bp)
}

// normalizeVersion 去掉前导 v 与首尾空白。
func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	return v
}

// splitVersion 拆出日期部分与预发布后缀。
func splitVersion(v string) (core, pre string) {
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

// compareParts 逐段比较点号分隔的部分：都是数字时按数值，否则按字符串。
func compareParts(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y string
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		nx, ex := strconv.Atoi(x)
		ny, ey := strconv.Atoi(y)
		if ex == nil && ey == nil {
			if nx != ny {
				if nx < ny {
					return -1
				}
				return 1
			}
			continue
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// firstNonEmptyLine 取响应体的第一行，用于把服务端返回的错误摘出来。
func firstNonEmptyLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	// 按字符截断，避免把多字节字符切成半截。
	if r := []rune(line); len(r) > 160 {
		line = string(r[:160]) + "…"
	}
	return line
}
