package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ListAll 跨分组列出会话，并带上各自的工作目录。
func TestListAllAcrossGroups(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	mkSession(t, root, "here0001", now.Add(-time.Hour), "本目录")
	other := filepath.Join(root, Slug(`D:\other`))
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"session","id":"else0002","cwd":"D:\\other","model":"m"}` + "\n" + `{"type":"user","content":"别处"}` + "\n"
	p := filepath.Join(other, "20260101-000000_else0002.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, now, now)

	all, err := ListAll(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != "else0002" || all[0].Cwd != `D:\other` || all[1].Cwd != listCwd {
		t.Fatalf("ListAll = %+v", all)
	}
	if got, _ := ListAll(filepath.Join(root, "nope")); got != nil {
		t.Errorf("目录不存在时应返回空，得到 %v", got)
	}
}

// 名字作为 name 记录追加在会话文件里：以最后一条为准，空名字表示清除；起名不改变“最近使用”的时间。
func TestRenameAppendsNameRecord(t *testing.T) {
	root := t.TempDir()
	mod := time.Now().Add(-time.Hour).Truncate(time.Second)
	p := mkSession(t, root, "name0001", mod, "原始提问")
	if err := Rename(root, p, "  登录\n修复  "); err != nil {
		t.Fatal(err)
	}
	list, _ := List(root, listCwd)
	if list[0].Name != "登录 修复" || list[0].Title() != "登录 修复" {
		t.Fatalf("名字 = %q", list[0].Name)
	}
	if st, _ := os.Stat(p); !st.ModTime().Equal(mod) {
		t.Errorf("起名后修改时间变了：%v → %v", mod, st.ModTime())
	}
	// 重放时忽略 name 记录：历史和轮数都不受影响。
	if st, err := Load(p); err != nil || st.Turns != 1 || len(st.History) != 2 {
		t.Fatalf("Load = %+v, %v", st, err)
	}
	if err := Rename(root, p, "新名字"); err != nil {
		t.Fatal(err)
	}
	if list, _ = List(root, listCwd); list[0].Name != "新名字" {
		t.Fatalf("应以最后一条为准: %q", list[0].Name)
	}
	if err := Rename(root, p, ""); err != nil {
		t.Fatal(err)
	}
	list, _ = List(root, listCwd)
	if list[0].Name != "" || list[0].Title() != "原始提问" {
		t.Fatalf("清除名字后 = %+v", list[0])
	}
	b, err := Find(root, `D:\elsewhere`, p) // 按路径找不受 cwd 限制
	if err != nil || b.ID != "name0001" {
		t.Fatalf("按路径 Find = %+v, %v", b, err)
	}
}

// 旧版本把名字记在分组目录的 meta.json 里：会话文件没有 name 记录时仍然用它；重新起名或清除后以文件为准。
func TestLegacyMetaNamesStillShow(t *testing.T) {
	root := t.TempDir()
	p := mkSession(t, root, "old00001", time.Now(), "原始提问")
	legacy := `{"cwd":"x","sessions":{"` + filepath.Base(p) + `":{"id":"old00001","name":"旧名字"}}}`
	if err := os.WriteFile(filepath.Join(filepath.Dir(p), "meta.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if list, _ := List(root, listCwd); list[0].Name != "旧名字" {
		t.Fatalf("应读出旧名字: %q", list[0].Name)
	}
	if err := Rename(root, p, ""); err != nil {
		t.Fatal(err)
	}
	if list, _ := List(root, listCwd); list[0].Name != "" {
		t.Fatalf("清除后不应再用旧名字: %q", list[0].Name)
	}
}

func TestDeleteRemovesFileAndAttachments(t *testing.T) {
	root := t.TempDir()
	p := mkSession(t, root, "del00001", time.Now(), "要删的")
	keep := mkSession(t, root, "keep0002", time.Now(), "留着")
	imgs := strings.TrimSuffix(p, ".jsonl") + ".images"
	os.MkdirAll(imgs, 0o755)
	os.WriteFile(filepath.Join(imgs, "a.png"), []byte("x"), 0o600)
	agents := strings.TrimSuffix(p, ".jsonl") + ".agents"
	os.MkdirAll(agents, 0o755)
	os.WriteFile(filepath.Join(agents, "call_1.jsonl"), []byte("{}\n"), 0o600)
	Rename(root, p, "有名字")

	if err := Delete(root, p, keep); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("会话文件还在")
	}
	if _, err := os.Stat(imgs); !os.IsNotExist(err) {
		t.Error("图片目录还在")
	}
	if _, err := os.Stat(agents); !os.IsNotExist(err) {
		t.Error("子代理记录目录还在")
	}
	list, _ := List(root, listCwd)
	if len(list) != 1 || list[0].ID != "keep0002" {
		t.Fatalf("剩下 %+v", list)
	}
}

// 删除与重命名只碰会话目录下的会话文件；当前正在写的会话不能删。
func TestManageRefusesUnsafeTargets(t *testing.T) {
	root := t.TempDir()
	p := mkSession(t, root, "act00001", time.Now(), "当前")
	if err := Delete(root, p, p); err == nil || !strings.Contains(err.Error(), "当前正在使用") {
		t.Errorf("删除当前会话应被拒绝，得到 %v", err)
	}
	outside := filepath.Join(t.TempDir(), "x.jsonl")
	os.WriteFile(outside, []byte("{}"), 0o600)
	for _, bad := range []string{
		outside,
		filepath.Join(root, "x.jsonl"), // 不在分组目录里
		filepath.Join(root, Slug(listCwd), "meta.json"),           // 不是 .jsonl
		filepath.Join(root, Slug(listCwd), "..", "..", "y.jsonl"), // 跳出会话目录
	} {
		if err := Delete(root, bad, ""); err == nil {
			t.Errorf("Delete(%s) 应被拒绝", bad)
		}
		if err := Rename(root, bad, "n"); err == nil {
			t.Errorf("Rename(%s) 应被拒绝", bad)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("会话目录以外的文件被动了")
	}
}
