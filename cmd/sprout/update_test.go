package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeReleases 起一个模拟 GitHub Release API 的服务端，并把 releaseURL 指向它。
func fakeReleases(t *testing.T, status int, body any) *[]*http.Request {
	t.Helper()
	var reqs []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, r)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	orig := releaseURL
	releaseURL = func(repo string) string { return srv.URL + "/repos/" + repo + "/releases/latest" }
	t.Cleanup(func() { releaseURL = orig })
	return &reqs
}

func release(tag string) map[string]any {
	asset := func(p, ext string) map[string]any {
		name := "sprout_" + strings.TrimPrefix(tag, "v") + "_" + p + ext
		return map[string]any{"name": name, "browser_download_url": "https://dl/" + name}
	}
	return map[string]any{
		"tag_name": tag,
		"html_url": "https://github.com/shumengya/sprout/releases/tag/" + tag,
		"assets": []any{
			asset(platformName(), ".zip"),
			asset("other_os", ".tar.gz"),
			map[string]any{"name": "SHA256SUMS.txt", "browser_download_url": "https://dl/SHA256SUMS.txt"},
		},
	}
}

func runUpdateCase(version, repo string) (int, string, string) {
	var o, e bytes.Buffer
	code := runUpdate(version, repo, nil, &o, &e)
	return code, o.String(), e.String()
}

func TestUpdateReportsNewerRelease(t *testing.T) {
	reqs := fakeReleases(t, http.StatusOK, release("v2026.11.20"))
	code, out, errOut := runUpdateCase("2026.11.12", "")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	for _, want := range []string{"有新版本可用：v2026.11.20", "当前 2026.11.12", "releases/tag/v2026.11.20", "_" + platformName() + ".zip"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "other_os") {
		t.Errorf("不该列出其他平台的产物:\n%s", out)
	}
	r := (*reqs)[0]
	if r.URL.Path != "/repos/shumengya/sprout/releases/latest" {
		t.Errorf("默认应查询本仓库，得到 %s", r.URL.Path)
	}
	if r.Header.Get("User-Agent") == "" {
		t.Error("GitHub API 要求 User-Agent")
	}
}

func TestUpdateUpToDate(t *testing.T) {
	fakeReleases(t, http.StatusOK, release("v2026.11.12"))
	code, out, _ := runUpdateCase("2026.11.12", "")
	if code != 0 || !strings.Contains(out, "已是最新版本") || strings.Contains(out, "有新版本") {
		t.Fatalf("code=%d\n%s", code, out)
	}
}

func TestUpdateLocalNewerDoesNotSuggestDowngrade(t *testing.T) {
	fakeReleases(t, http.StatusOK, release("v2026.11.12"))
	_, out, _ := runUpdateCase("2026.11.14", "")
	if !strings.Contains(out, "比最新发布还新") || strings.Contains(out, "有新版本") {
		t.Fatalf("不该劝用户降级:\n%s", out)
	}
}

func TestUpdateDevBuild(t *testing.T) {
	fakeReleases(t, http.StatusOK, release("v2026.11.12"))
	code, out, _ := runUpdateCase("dev", "")
	if code != 0 || !strings.Contains(out, "开发构建") || !strings.Contains(out, "v2026.11.12") {
		t.Fatalf("code=%d\n%s", code, out)
	}
}

func TestUpdateCustomRepoAndErrors(t *testing.T) {
	reqs := fakeReleases(t, http.StatusNotFound, map[string]any{"message": "Not Found"})
	code, _, errOut := runUpdateCase("2026.11.12", "me/fork")
	if code == 0 || !strings.Contains(errOut, "还没有任何发布") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if (*reqs)[0].URL.Path != "/repos/me/fork/releases/latest" {
		t.Errorf("--repo 应生效，得到 %s", (*reqs)[0].URL.Path)
	}

	fakeReleases(t, http.StatusForbidden, map[string]any{"message": "rate limit"})
	if code, _, errOut := runUpdateCase("2026.11.12", ""); code == 0 || !strings.Contains(errOut, "限流") {
		t.Fatalf("限流应报错: code=%d stderr=%q", code, errOut)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2026.11.12", "v2026.11.12", 0},
		{"2026.11.12", "2026.11.13", -1},
		{"2026.12.01", "2026.11.30", 1},
		{"2026.11.12-rc.1", "2026.11.12", -1},
		{"2026.11.12-rc.2", "2026.11.12-rc.10", -1},
		{"2027.01.01", "2026.12.31", 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d，期望 %d", c.a, c.b, got, c.want)
		}
	}
	if isReleaseVersion("dev") || !isReleaseVersion("v2026.11.12-rc.1") {
		t.Error("isReleaseVersion 判断错误")
	}
}

func TestParseUpdateArgs(t *testing.T) {
	a, err := parseArgs([]string{"update", "--repo", "me/fork"})
	if err != nil || !a.selfUpdate || a.updateRepo != "me/fork" {
		t.Fatalf("得到 %+v %v", a, err)
	}
	for _, bad := range [][]string{{"update", "extra"}, {"update", "--repo", "nofork"}, {"update", "--repo", "a/b/c"}} {
		if _, err := parseArgs(bad); err == nil {
			t.Errorf("%v 应报用法错误", bad)
		}
	}
}
