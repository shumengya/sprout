package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// 会话是一棵以“轮”为节点的树，全部分支都在同一个 JSONL 文件里。
//
// 轮号按 user 记录在文件里出现的顺序从 1 起编。一轮默认接在上一条记录所在的位置后面；
// branch 记录 {"turn": N} 把位置移到第 N 轮之后（0 是会话开头），之后的记录都接在那里。
// 没有 branch 记录的旧文件就是一条链，重放结果与逐行重放完全相同。
//
// 重放某个位置时只取从根到它这条路径上的记录，按文件顺序交给 replayer：
// JSONL 的前缀重放出来就是当时的状态，路径上的记录正是“当时”的那个前缀。

// TreeNode 是会话树里的一轮。
type TreeNode struct {
	Turn   int    // 轮号，从 1 起
	Parent int    // 接在第几轮之后；0 是会话开头
	Prompt string // 提问原文（改问时要放回输入框，所以不截断）
	Reply  string // 这一轮最后一段文字回复，压成一行；没有文字时为空
	Images int    // 提问附带的图片数
	Time   time.Time
}

// Tree 是一个会话的轮次树。
type Tree struct {
	Nodes []TreeNode // 按轮号升序
	// Current 是当前位置：下一次提问接在第几轮之后，0 是会话开头。
	Current int
}

// segment 是树上的一段记录：一轮（从 user 记录开始），或一次切换位置后、下一次提问前的记录（turn 为 0）。
// 切换位置单独成段，是因为切换后的 /compact 等记录只属于新位置，不能混进原来那一轮之后的分支里。
type segment struct {
	parent int // 父段下标；根段为 -1
	turn   int
	recs   []rawRec
}

type parsed struct {
	segs    []segment
	turnSeg []int // 轮号 → 段下标；下标 0 是根段
	cur     int   // 文件末尾所在的段
	skipped int
}

// parseFile 读出全部记录并按 branch 记录分段。坏行和指向不存在轮次的 branch 记录计入 skipped。
func parseFile(path string) (*parsed, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开会话文件失败: %w", err)
	}
	defer f.Close()

	p := &parsed{segs: []segment{{parent: -1}}, turnSeg: []int{0}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec rawRec
		if json.Unmarshal(line, &rec) != nil {
			p.skipped++
			continue
		}
		switch rec.Type {
		case "user":
			p.segs = append(p.segs, segment{parent: p.cur, turn: len(p.turnSeg)})
			p.cur = len(p.segs) - 1
			p.turnSeg = append(p.turnSeg, p.cur)
		case "branch":
			// 只能指向已经出现过的轮次；否则是损坏的记录，忽略它，位置不动。
			if rec.Turn < 0 || rec.Turn >= len(p.turnSeg) {
				p.skipped++
				continue
			}
			p.segs = append(p.segs, segment{parent: p.turnSeg[rec.Turn]})
			p.cur = len(p.segs) - 1
			continue
		}
		p.segs[p.cur].recs = append(p.segs[p.cur].recs, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取会话文件失败: %w", err)
	}
	return p, nil
}

// turnOf 返回段所在位置对应的轮号：切换位置产生的段算作它父段那一轮。
func (p *parsed) turnOf(i int) int {
	for i > 0 && p.segs[i].turn == 0 {
		i = p.segs[i].parent
	}
	return p.segs[i].turn
}

// replay 重放从根到第 leaf 段的路径。
func (p *parsed) replay(path string, leaf int) *State {
	var chain []int
	for i := leaf; i >= 0; i = p.segs[i].parent {
		chain = append(chain, i)
	}
	r := &replayer{st: &State{Path: path, Skipped: p.skipped}}
	for i := len(chain) - 1; i >= 0; i-- {
		for _, rec := range p.segs[chain[i]].recs {
			r.handle(rec)
		}
	}
	r.finish()
	r.st.History, r.st.Summary = r.history, r.summary
	return r.st
}

// LoadAt 重放会话在第 turn 轮之后（0 为会话开头）的状态，不改动文件。轮次不存在时报错。
func LoadAt(path string, turn int) (*State, error) {
	p, err := parseFile(path)
	if err != nil {
		return nil, err
	}
	if turn < 0 || turn >= len(p.turnSeg) {
		return nil, fmt.Errorf("会话里没有第 %d 轮（共 %d 轮）", turn, len(p.turnSeg)-1)
	}
	return p.replay(path, p.turnSeg[turn]), nil
}

// ReadTree 读出会话的轮次树和当前位置。
func ReadTree(path string) (*Tree, error) {
	p, err := parseFile(path)
	if err != nil {
		return nil, err
	}
	t := &Tree{Current: p.turnOf(p.cur)}
	for turn := 1; turn < len(p.turnSeg); turn++ {
		s := p.segs[p.turnSeg[turn]]
		n := TreeNode{Turn: turn, Parent: p.turnOf(s.parent)}
		for _, rec := range s.recs {
			switch rec.Type {
			case "user":
				n.Prompt, n.Images = rec.Content, len(rec.Images)
				n.Time, _ = time.Parse(time.RFC3339Nano, rec.Time)
			case "assistant", "interrupted":
				if c := oneLine(rec.Content, promptRunes); c != "" {
					n.Reply = c
				}
			}
		}
		t.Nodes = append(t.Nodes, n)
	}
	if len(t.Nodes) == 0 {
		return nil, errors.New("当前会话还没有任何提问")
	}
	return t, nil
}
