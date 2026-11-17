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

// sessionTree 读出当前会话的轮次树。会话还没有落盘时报错。
func (a *app) sessionTree() (*session.Tree, error) {
	if a.rec == nil || a.rec.Path() == "" {
		return nil, errors.New("当前会话还没有任何提问")
	}
	return session.ReadTree(a.rec.Path())
}

// navigate 把当前会话的位置移到第 turn 轮之后（0 为会话开头），之后的提问从那里分出新的分支。
// 所有分支都留在同一个会话文件里，随时可以再切回来。调用方必须保证此时没有 Run 在进行。
//
// 先在不动文件的情况下重放目标位置，确认没问题再写 branch 记录，最后才换掉 agent 的历史：
// 任何一步失败，文件和 agent 都保持原样。
func (a *app) navigate(turn int) (*session.State, string, error) {
	if a.ag == nil || a.rec == nil {
		return nil, "", errors.New("当前没有可用的 agent，无法切换会话分支")
	}
	path := a.rec.Path()
	if path == "" {
		return nil, "", errors.New("当前会话还没有任何提问")
	}
	st, err := session.LoadAt(path, turn)
	if err != nil {
		return nil, "", err
	}
	notes := a.adopt(st)
	if err := a.rec.Branch(turn); err != nil {
		return nil, "", fmt.Errorf("写入会话记录失败: %w", err)
	}
	a.ag.Restore(st.History, st.Summary)
	if turn == 0 {
		return st, "已回到会话开头，接下来的提问会开出新的分支；原来的对话仍在 /tree 里" + notes, nil
	}
	return st, fmt.Sprintf("已切换到第 %d 轮之后，接下来的提问会开出新的分支；原来的对话仍在 /tree 里", turn) + notes, nil
}

// attach 载入 path 指向的会话，让 agent 换成它的历史、记录器接着写这个文件。
// 返回的 notes 是附加说明（以“；”开头，没有时为空），由调用方拼在自己的提示后面。
func (a *app) attach(path string) (*session.State, string, error) {
	st, err := session.Load(path)
	if err != nil {
		return nil, "", err
	}
	notice := a.adopt(st)
	if err := a.rec.Attach(st.Path); err != nil {
		return nil, "", err
	}
	a.ag.Restore(st.History, st.Summary)
	return st, notice, nil
}

// adopt 把重放出的状态调整成能交给当前 agent 的样子（丢掉对不上的思维链），返回附加说明（以“；”开头，没有时为空）。
func (a *app) adopt(st *session.State) string {
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
	return notice
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

func (s sessionStore) Tree() (*tui.SessionTree, error) {
	t, err := s.a.sessionTree()
	if err != nil {
		return nil, err
	}
	out := &tui.SessionTree{Current: t.Current, Nodes: make([]tui.TreeNode, len(t.Nodes))}
	for i, n := range t.Nodes {
		out.Nodes[i] = tui.TreeNode{Turn: n.Turn, Parent: n.Parent, Prompt: n.Prompt, Reply: n.Reply, Images: n.Images, Time: n.Time}
	}
	return out, nil
}

func (s sessionStore) Navigate(turn int) (*tui.Restored, error) {
	st, notice, err := s.a.navigate(turn)
	if err != nil {
		return nil, err
	}
	return &tui.Restored{Events: st.Events, Notice: notice}, nil
}
