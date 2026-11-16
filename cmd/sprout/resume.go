package main

import (
	"errors"
	"fmt"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/agent/platform"
	"github.com/shumengya/sprout/internal/agent/session"
	"github.com/shumengya/sprout/internal/tui"
)

// resume 恢复选择器指定的会话：让 agent 载入历史和摘要，并让记录器接着写原来的会话文件。
// 调用方必须保证此时没有 Run 在进行（TUI 在恢复期间禁止发送）。
//
// 先 Attach 再 Restore：Attach 失败（文件打不开）时 agent 的状态保持不变，不会出现“历史换了但记录写到别处”。
func (a *app) resume(selector string) (*session.State, string, error) {
	if a.ag == nil || a.rec == nil {
		return nil, "", errors.New("当前没有可用的 agent，无法恢复会话")
	}
	brief, err := session.Find(config.SessionsDir(a.dataDir), a.cwd, selector)
	if err != nil {
		return nil, "", err
	}
	st, notes, err := a.attach(brief.Path)
	if err != nil {
		return nil, "", err
	}
	notice := fmt.Sprintf("已恢复会话 %s（%d 轮）", st.ID, st.Turns)
	if brief.Name != "" {
		notice = fmt.Sprintf("已恢复会话「%s」%s（%d 轮）", brief.Name, st.ID, st.Turns)
	}
	return st, notice + notes, nil
}

// branch 把当前会话的前 turns 轮（0 为全部）复制成新会话并切换过去，原会话文件不动。
// 调用方必须保证此时没有 Run 在进行。
func (a *app) branch(turns int) (*session.State, string, error) {
	if a.ag == nil || a.rec == nil {
		return nil, "", errors.New("当前没有可用的 agent，无法分支会话")
	}
	src := a.rec.Path()
	if src == "" {
		return nil, "", errors.New("当前会话还没有任何内容，不需要分支")
	}
	dst, err := session.Fork(config.SessionsDir(a.dataDir), src, turns)
	if err != nil {
		return nil, "", err
	}
	st, notes, err := a.attach(dst)
	if err != nil {
		// 分支文件已经写好，只是没切过去：告诉用户它在哪，可以之后 /resume。
		return nil, "", fmt.Errorf("分支会话已创建（%s），但切换过去失败: %w", dst, err)
	}
	return st, fmt.Sprintf("已分支出新会话 %s（保留 %d 轮），之后的对话写进新会话；原会话不变，可以 /resume 回去", st.ID, st.Turns) + notes, nil
}

// attach 载入 path 指向的会话，让 agent 换成它的历史、记录器接着写这个文件。
// 返回的 notes 是附加说明（以“；”开头，没有时为空），由调用方拼在自己的提示后面。
func (a *app) attach(path string) (*session.State, string, error) {
	st, err := session.Load(path)
	if err != nil {
		return nil, "", err
	}
	notice := ""
	if st.Info.Cwd != "" && !platform.SamePath(st.Info.Cwd, a.cwd) {
		// 工具仍在当前目录执行，不随会话切过去；提醒一下，免得模型按旧目录的上下文去找文件。
		notice += fmt.Sprintf("；这是在 %s 下的会话，命令仍在当前目录 %s 执行", st.Info.Cwd, a.cwd)
	}
	if st.Info.Model != a.sel.ModelName || st.Info.Provider != a.sel.ProviderName {
		// 换了模型：别家的思维链不该回传给新模型。对话内容本身保留。
		st.StripReasoning()
		notice += fmt.Sprintf("；原会话用的是 %s/%s，当前是 %s，已丢弃旧的思维链", st.Info.Provider, st.Info.Model, a.sel.Ref())
	}
	if st.Skipped > 0 {
		notice += fmt.Sprintf("；会话文件里有 %d 行损坏，已跳过", st.Skipped)
	}
	// 原始思考块绑定了当初的系统提示词（含日期），新进程里对不上，回传会被拒绝；
	// 思维链文本保留（DeepSeek 等按文本回传，不受影响）。
	agent.DropNative(st.History)
	if err := a.rec.Attach(st.Path); err != nil {
		return nil, "", err
	}
	a.ag.Restore(st.History, st.Summary)
	return st, notice, nil
}

// newSession 开始一个空白会话：清空 agent 的历史和摘要，记录器改写新文件。旧会话文件原样保留，可以 /resume 回去。
// 调用方必须保证此时没有 Run 在进行。
func (a *app) newSession() error {
	if a.ag == nil || a.rec == nil {
		return errors.New("当前没有可用的 agent，无法新建会话")
	}
	a.rec.Reset(session.Info{
		Provider: a.sel.ProviderName, Model: a.sel.ModelName, Cwd: a.cwd,
		Tools: a.reg.Names(), Version: a.env.version,
	})
	a.ag.Restore(nil, "")
	return nil
}

// sessionStore 把 app 适配成 tui.Sessions。
type sessionStore struct{ a *app }

// listSessions 列出当前目录（all 时为全部目录）的会话，并返回当前正在写的会话文件（没有时为空）。
// TUI 的 /resume 和 RPC 的 session/list 共用。
func (a *app) listSessions(all bool) ([]session.Brief, string, error) {
	dir := config.SessionsDir(a.dataDir)
	var (
		list []session.Brief
		err  error
	)
	if all {
		list, err = session.ListAll(dir)
	} else {
		list, err = session.List(dir, a.cwd)
	}
	if err != nil {
		return nil, "", err
	}
	active := ""
	if a.rec != nil {
		active = a.rec.Path()
	}
	return list, active, nil
}

func (s sessionStore) List(all bool) ([]tui.SessionInfo, error) {
	list, active, err := s.a.listSessions(all)
	if err != nil {
		return nil, err
	}
	out := make([]tui.SessionInfo, len(list))
	for i, b := range list {
		out[i] = tui.SessionInfo{
			ID: b.ID, Path: b.Path, Prompt: b.Prompt, Name: b.Name, Cwd: b.Cwd, Model: b.Model,
			Updated: b.Updated, Turns: b.Turns, Current: active != "" && platform.SamePath(b.Path, active),
		}
	}
	return out, nil
}

func (s sessionStore) Rename(path, name string) error {
	return session.Rename(config.SessionsDir(s.a.dataDir), path, name)
}

func (s sessionStore) Delete(path string) error {
	active := ""
	if s.a.rec != nil {
		active = s.a.rec.Path()
	}
	return session.Delete(config.SessionsDir(s.a.dataDir), path, active)
}

func (s sessionStore) Resume(selector string) (*tui.Restored, error) {
	st, notice, err := s.a.resume(selector)
	if err != nil {
		return nil, err
	}
	return &tui.Restored{Events: st.Events, Notice: notice}, nil
}

func (s sessionStore) New() error { return s.a.newSession() }

func (s sessionStore) Branch(turns int) (*tui.Restored, error) {
	st, notice, err := s.a.branch(turns)
	if err != nil {
		return nil, err
	}
	return &tui.Restored{Events: st.Events, Notice: notice}, nil
}
