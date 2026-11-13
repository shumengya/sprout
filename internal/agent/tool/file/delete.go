package file

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/shumengya/sprout/internal/agent/tool"
)

// 删除报告里最多列出的条目数：够模型核对删了些什么，又不至于让结果刷屏。
const maxReportNames = 40

// Delete 删除文件或目录，不经过回收站。
//
// 设计目标是“删错了也能立刻发现”：删之前能预览影响范围（dry_run），删不了的目标一定报错而不是静默成功，
// 报告里带上条目数和总大小，明显会伤到系统的路径（盘符根、工作目录、用户主目录及其上级）一律拒绝。
type Delete struct {
	dir string
}

func NewDelete(dir string) *Delete { return &Delete{dir: dir} }

func (t *Delete) Name() string { return "delete" }

func (t *Delete) Description() string {
	return "删除一个文件或目录，不可恢复：不进回收站，也没有撤销。\n" +
		"文件直接删除；目录默认只接受空目录，非空目录必须显式设 recursive: true。\n" +
		"不确定会删掉什么时先设 dry_run: true：返回将删除的文件数、目录数和总大小，不做任何改动。\n" +
		"符号链接只删链接本身，不会顺着链接删到目标；Windows 上的只读文件会先去掉只读位再删。\n" +
		"磁盘根目录、工作目录及其上级、用户主目录及其上级一律拒绝删除。\n" +
		"只删自己创建的临时产物，或用户明确要求删的东西；不确定时先把要删的路径和影响范围告诉用户。"
}

func (t *Delete) Parameters() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "要删除的路径，可以是绝对路径，或相对工作目录的路径。"
    },
    "recursive": {
      "type": "boolean",
      "description": "删除非空目录时必须设为 true；删文件时忽略。默认 false，此时非空目录一律报错不删。"
    },
    "dry_run": {
      "type": "boolean",
      "description": "只统计不删除，用来先看清影响范围。默认 false。"
    }
  },
  "required": ["path"]
}`)
}

type deleteArgs struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
	DryRun    bool   `json:"dry_run"`
}

// deleteSummary 是要删（或已删）的内容：分类计数加总大小。
type deleteSummary struct {
	files, dirs, links, others int
	bytes                      int64
	names                      []string // 相对路径，最多 maxReportNames 项
}

func (s *deleteSummary) total() int { return s.files + s.dirs + s.links + s.others }

func (s *deleteSummary) String() string {
	var parts []string
	if s.files > 0 {
		parts = append(parts, fmt.Sprintf("%d 个文件", s.files))
	}
	if s.dirs > 0 {
		parts = append(parts, fmt.Sprintf("%d 个目录", s.dirs))
	}
	if s.links > 0 {
		parts = append(parts, fmt.Sprintf("%d 个符号链接", s.links))
	}
	if s.others > 0 {
		parts = append(parts, fmt.Sprintf("%d 个其他条目", s.others))
	}
	out := "空目录"
	if len(parts) > 0 {
		out = strings.Join(parts, "、")
	}
	if s.bytes > 0 {
		out += "，共 " + humanBytes(s.bytes)
	}
	return out
}

func (t *Delete) Run(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var a deleteArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return tool.Result{}, fmt.Errorf("参数不是有效的 JSON 对象: %w", err)
		}
	}
	start := time.Now()
	abs, err := resolve(t.dir, a.Path)
	if err != nil {
		return tool.Result{}, err
	}
	if err := guardDelete(abs, t.dir); err != nil {
		return tool.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}

	// 用 Lstat：符号链接要按链接本身处理，不能先跟着链到目标上再判断类型。
	info, err := os.Lstat(abs)
	switch {
	case os.IsNotExist(err):
		return tool.Result{}, fmt.Errorf("%s 不存在，没有可删除的内容（delete 不会新建文件；路径写错时先用 ls 或 read 确认）", abs)
	case err != nil:
		return tool.Result{}, fmt.Errorf("无法访问 %s: %w", abs, err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(abs)
		s := &deleteSummary{links: 1}
		if a.DryRun {
			return result(start, "delete", abs, "将删除符号链接（指向 "+target+"），不会触碰目标", s), nil
		}
		if err := removeWithRetry(abs); err != nil {
			return tool.Result{}, fmt.Errorf("删除符号链接 %s 失败: %w", abs, err)
		}
		return result(start, "delete", abs, "已删除符号链接（指向 "+target+"），目标文件未受影响", s), nil
	}

	if !info.IsDir() {
		s := &deleteSummary{files: 1, bytes: info.Size()}
		if a.DryRun {
			return result(start, "delete", abs, "将删除这个文件", s), nil
		}
		if err := removeWithRetry(abs); err != nil {
			return tool.Result{}, fmt.Errorf("删除 %s 失败: %w", abs, err)
		}
		return result(start, "delete", abs, "已删除这个文件", s), nil
	}

	// 目录：先整棵扫描拿到影响范围。非空且没开 recursive 时报错并给出计数，
	// 让模型直接看到“会删掉些什么”，不用先自己数一遍。
	// dry_run 先返回：预览本来就该在“要不要 recursive”之前就能看到全貌。
	st, err := collect(abs)
	if err != nil {
		return tool.Result{}, fmt.Errorf("扫描 %s 失败，未删除任何内容: %w", abs, err)
	}
	if a.DryRun {
		return result(start, "delete", abs, "将删除这个目录及其内容（dry_run，未做任何改动）", st), nil
	}
	if st.total() > 0 && !a.Recursive {
		return tool.Result{}, fmt.Errorf("%s 不是空目录（%s），没有删除。确认要整个删掉时设 recursive: true；只想先看清影响范围可以设 dry_run: true", abs, st)
	}
	if err := removeTree(abs, st); err != nil {
		return tool.Result{}, err
	}
	return result(start, "delete", abs, "已删除这个目录及其内容", st), nil
}

// result 把“删了什么、影响多大”拼成给模型看的正文：分类计数、总大小，再列出前若干项。
func result(start time.Time, verb, abs, head string, s *deleteSummary) tool.Result {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s：%s", head, abs, s)
	if len(s.names) > 0 {
		b.WriteString("\n")
		for _, n := range s.names {
			b.WriteString("  ")
			b.WriteString(n)
			b.WriteString("\n")
		}
		if n := s.total() - len(s.names); n > 0 {
			fmt.Fprintf(&b, "  …还有 %d 项没列出\n", n)
		}
	}
	return tool.Result{Command: verb + " " + abs, Stdout: b.String(), Duration: time.Since(start)}
}

// collect 统计 root 下（不含 root 自身）的条目数与大小。WalkDir 不跟随符号链接，链接按条目本身计。
func collect(root string) (*deleteSummary, error) {
	s := &deleteSummary{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			rel = filepath.Base(p)
		}
		switch {
		case d.Type()&os.ModeSymlink != 0:
			s.links++
		case d.IsDir():
			s.dirs++
		case d.Type().IsRegular():
			s.files++
			if fi, ierr := d.Info(); ierr == nil {
				s.bytes += fi.Size()
			}
		default:
			s.others++
		}
		if len(s.names) < maxReportNames {
			s.names = append(s.names, rel)
		}
		return nil
	})
	return s, err
}

// removeTree 删掉 root 及其全部内容。先整棵走一遍收集路径、再动手：
// 扫描阶段出错时什么都没删，不会留下删了一半的目录树。
func removeTree(root string, s *deleteSummary) error {
	var paths []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return fmt.Errorf("扫描 %s 失败，未删除任何内容: %w", root, err)
	}

	// WalkDir 先父后子，倒序删除就是从里往外删，目录在其内容清空之后才移除。
	failed := 0
	var first error
	for i := len(paths) - 1; i >= 0; i-- {
		if err := removeWithRetry(paths[i]); err != nil {
			failed++
			if first == nil {
				first = err
			}
		}
	}
	if first != nil {
		return fmt.Errorf("删除 %s 时有 %d 项失败（目标共 %s），第一处：%w", root, failed, s, first)
	}
	return nil
}

// removeWithRetry 删除单个条目。Windows 上只读文件直接删会报权限错误（git 检出的文件常是只读），
// 这时去掉只读位再试一次；其他平台不做这件事，权限不够就是真的删不了。
func removeWithRetry(p string) error {
	err := os.Remove(p)
	if err == nil || !os.IsPermission(err) || runtime.GOOS != "windows" {
		return err
	}
	fi, statErr := os.Lstat(p)
	if statErr != nil || !fi.Mode().IsRegular() {
		return err
	}
	if chErr := os.Chmod(p, fi.Mode().Perm()|0o200); chErr != nil {
		return err
	}
	return os.Remove(p)
}

// guardDelete 拒绝明显会伤到系统的目标。宁可在这些路径上直接失败，也不留一个“能删盘符根”的口子。
func guardDelete(abs, cwd string) error {
	if isVolumeRoot(abs) {
		return fmt.Errorf("%s 是磁盘根目录，拒绝删除", abs)
	}
	if isAncestor(abs, cwd) {
		return fmt.Errorf("%s 是工作目录本身或它的上级目录，拒绝删除（删除工作目录会让会话上下文全部失效）", abs)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && isAncestor(abs, home) {
		return fmt.Errorf("%s 是用户主目录或它的上级目录，拒绝删除", abs)
	}
	return nil
}

func isVolumeRoot(p string) bool {
	vol := filepath.VolumeName(p)
	rest := strings.TrimPrefix(p, vol)
	return rest == "" || rest == string(filepath.Separator)
}

// isAncestor 报告 dir 是否等于 target 或是它的上级。Windows 上路径大小写不敏感，比较前统一转小写。
func isAncestor(dir, target string) bool {
	if runtime.GOOS == "windows" {
		dir, target = strings.ToLower(dir), strings.ToLower(target)
	}
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
