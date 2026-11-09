package main

import (
	"errors"
	"fmt"

	"github.com/shumengya/sprout/internal/agent/config"
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
	st, err := session.Load(brief.Path)
	if err != nil {
		return nil, "", err
	}
	notice := fmt.Sprintf("已恢复会话 %s（%d 轮）", st.ID, st.Turns)
	if st.Info.Model != a.sel.ModelName || st.Info.Provider != a.sel.ProviderName {
		// 换了模型：别家的思维链不该回传给新模型。对话内容本身保留。
		st.StripReasoning()
		notice += fmt.Sprintf("；原会话用的是 %s/%s，当前是 %s，已丢弃旧的思维链", st.Info.Provider, st.Info.Model, a.sel.Ref())
	}
	if st.Skipped > 0 {
		notice += fmt.Sprintf("；会话文件里有 %d 行损坏，已跳过", st.Skipped)
	}
	if err := a.rec.Attach(st.Path); err != nil {
		return nil, "", err
	}
	a.ag.Restore(st.History, st.Summary)
	return st, notice, nil
}

// sessionStore 把 app 适配成 tui.Sessions。
type sessionStore struct{ a *app }

func (s sessionStore) List() ([]tui.SessionInfo, error) {
	list, err := session.List(config.SessionsDir(s.a.dataDir), s.a.cwd)
	if err != nil {
		return nil, err
	}
	out := make([]tui.SessionInfo, len(list))
	for i, b := range list {
		out[i] = tui.SessionInfo{ID: b.ID, Prompt: b.Prompt, Model: b.Model, Updated: b.Updated, Turns: b.Turns}
	}
	return out, nil
}

func (s sessionStore) Resume(selector string) (*tui.Restored, error) {
	st, notice, err := s.a.resume(selector)
	if err != nil {
		return nil, err
	}
	return &tui.Restored{Events: st.Events, Notice: notice}, nil
}
