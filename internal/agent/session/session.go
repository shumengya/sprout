// Package session 把内核事件流落盘成 JSONL 会话记录。
//
// 目录结构：<sessionsDir>/--<工作目录 slug>--/<时间戳>_<id>.jsonl，同目录放 meta.json 索引。
// 记录器本身是一个 event.Sink，与 surface 订阅同一条事件流。
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
)

// Info 是会话的静态信息。
type Info struct {
	Provider string
	Model    string
	Cwd      string
	Tools    []string
	Version  string
}

// Slug 把工作目录转成目录名：去掉开头的分隔符，把 / \ : 替换成 -，前后加 --。
// 例如 D:\SmyProjects\sprout → --D--SmyProjects-sprout--，/home/a/b → --home-a-b--。
func Slug(cwd string) string {
	s := strings.TrimLeft(cwd, `/\`)
	s = strings.NewReplacer("/", "-", `\`, "-", ":", "-").Replace(s)
	return "--" + s + "--"
}

// Recorder 把事件写成 JSONL。文件在第一个需要落盘的事件到来时才创建，
// 启动后什么都没做就退出的会话不会留下空文件。写入失败只记日志，不影响 agent 运行。
type Recorder struct {
	dir  string
	info Info
	log  *slog.Logger
	now  func() time.Time

	mu     sync.Mutex
	f      *os.File
	path   string
	failed bool // 已经失败过：不再重试，也不再重复刷日志
	err    error
	closed bool
}

// NewRecorder 创建记录器。sessionsDir 是 <dataDir>/sessions。
func NewRecorder(sessionsDir string, info Info, log *slog.Logger) *Recorder {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Recorder{dir: sessionsDir, info: info, log: log, now: time.Now}
}

// Path 返回会话文件路径；尚未创建时为空。
func (r *Recorder) Path() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.path
}

// Err 返回第一个写入错误。
func (r *Recorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// Attach 让记录器改为向已有的会话文件追加，用于恢复会话：恢复后的对话和原来的在同一个文件里。
// 先关掉当前打开的文件（如果有），再以追加方式打开目标，并写一条 resume 记录标明这次续接。
// 调用方必须保证此时没有 Run 在进行。
func (r *Recorder) Attach(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f != nil {
		r.f.Close()
		r.f = nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("打开会话文件失败: %w", err)
	}
	r.f, r.path, r.failed, r.err, r.closed = f, f.Name(), false, nil, false
	r.write(resumeRec{header{Type: "resume", Time: r.now()}, r.info.Provider, r.info.Model, r.info.Tools})
	return nil
}

// Reset 结束当前会话文件，之后的事件写进一个新会话；新文件和 Recorder 刚创建时一样，等第一条记录到来才创建。
// info 换成调用方给的值：中途切换过模型时，新会话的头记录要写当前模型，而不是启动时的。
// 调用方必须保证此时没有 Run 在进行。
func (r *Recorder) Reset(info Info) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f != nil {
		r.f.Close()
	}
	r.f, r.path, r.failed, r.err, r.closed, r.info = nil, "", false, nil, false, info
}

// NoteModelChange 在会话中途切换模型时写一条 resume 记录，标明切换后的模型；仅供审计，恢复重放时忽略。
// 调用方必须保证此时没有 Run 在进行。
func (r *Recorder) NoteModelChange(provider, model string, tools []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed || r.closed {
		return
	}
	if r.f == nil { // 还没有任何内容落盘：模型切换本身不值得创建会话文件
		return
	}
	r.write(resumeRec{header{Type: "resume", Time: r.now()}, provider, model, tools})
}

// Emit 实现 event.Sink。
func (r *Recorder) Emit(e event.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed || r.closed {
		return
	}
	switch ev := e.(type) {
	case event.TurnStarted:
		if len(ev.Images) > 0 {
			if r.f == nil && !r.open() {
				return
			}
			ev.Images = r.storeImages(ev.Images)
			if r.failed {
				// 图片没写成功：这一轮的用户记录也不能写，否则重放时历史与运行时不一致。
				return
			}
			e = ev
		}
	case event.ToolFinished:
		if len(ev.Images) > 0 {
			if r.f == nil && !r.open() {
				return
			}
			ev.Images = r.storeImages(ev.Images)
			if r.failed {
				return
			}
			e = ev
		}
	}
	rec, ok := toRecord(e, r.now())
	if !ok {
		return
	}
	if r.f == nil && !r.open() {
		return
	}
	r.write(rec)
}

// storeImages 把图片（用户输入或工具结果里的）写成会话目录旁边的文件，返回回填了相对路径、清空了 base64 的副本。
// 存放位置是 <会话目录>/<会话名>.images/，和 JSONL 放在一起，整个目录搬走也能恢复。
// 文件名取自内容哈希，同一张图重复发只会存一份。调用方持有 mu。
func (r *Recorder) storeImages(imgs []event.ImageInfo) []event.ImageInfo {
	base := strings.TrimSuffix(r.path, ".jsonl")
	dir := base + ".images"
	// 事件按值分发给多个 sink，但 Images 的底层数组是共享的：TUI 可能正在另一个 goroutine 读它。
	// 先复制一份再改，否则清空 Data 会和 surface 产生数据竞争。
	imgs = append([]event.ImageInfo(nil), imgs...)
	for i := range imgs {
		img := &imgs[i]
		raw, err := base64.StdEncoding.DecodeString(img.Data)
		if err != nil {
			r.fail(fmt.Errorf("解码图片失败: %w", err))
			return imgs
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			r.fail(fmt.Errorf("创建图片目录失败: %w", err))
			return imgs
		}
		sum := sha256.Sum256(raw)
		name := hex.EncodeToString(sum[:8]) + agent.ImageExt(img.MediaType)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			r.fail(fmt.Errorf("写入图片失败: %w", err))
			return imgs
		}
		img.Path = filepath.ToSlash(filepath.Join(filepath.Base(dir), name))
		img.Bytes = len(raw)
		img.Data = ""
	}
	return imgs
}

// Close 关闭会话文件，可重复调用。
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

func (r *Recorder) fail(err error) {
	r.failed, r.err = true, err
	r.log.Error("session recording disabled", "error", err.Error())
}

// open 创建会话文件，写入 session 头记录并登记 meta.json。调用方持有 mu。
func (r *Recorder) open() bool {
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	id := hex.EncodeToString(rnd[:])
	started := r.now()
	file := fmt.Sprintf("%s_%s.jsonl", started.Format("20060102-150405"), id)

	groupDir := filepath.Join(r.dir, Slug(r.info.Cwd))
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		r.fail(fmt.Errorf("创建会话目录失败: %w", err))
		return false
	}
	// 会话里会有命令输出，可能含敏感内容，仅所有者可读写。
	f, err := os.OpenFile(filepath.Join(groupDir, file), os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
	if err != nil {
		r.fail(fmt.Errorf("创建会话文件失败: %w", err))
		return false
	}
	r.f, r.path = f, f.Name()

	r.write(sessionRec{
		header: header{Type: "session", Time: started}, ID: id, Cwd: r.info.Cwd,
		Provider: r.info.Provider, Model: r.info.Model, Tools: r.info.Tools, Version: r.info.Version,
	})
	warn, err := updateMeta(groupDir, file, r.info.Cwd, sessionMeta{
		ID: id, Provider: r.info.Provider, Model: r.info.Model, Tools: r.info.Tools,
		StartedAt: started, Version: r.info.Version,
	})
	if warn != "" {
		r.log.Warn(warn, "dir", groupDir)
	}
	if err != nil { // meta.json 只是索引，写不进去不影响会话记录
		r.log.Warn("update meta.json failed", "error", err.Error())
	}
	return !r.failed
}

// write 追加一行。一条记录一次 Write，避免崩溃时留下半行。调用方持有 mu。
func (r *Recorder) write(rec any) {
	b, err := json.Marshal(rec)
	if err != nil {
		r.fail(fmt.Errorf("编码会话记录失败: %w", err))
		return
	}
	if _, err := r.f.Write(append(b, '\n')); err != nil {
		r.fail(fmt.Errorf("写入会话文件失败: %w", err))
	}
}
