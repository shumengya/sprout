package gitstatus

import "testing"

func TestParseCountsEachPathOnce(t *testing.T) {
	text := "# branch.oid abc\n# branch.head main\n" +
		"1 .M N... 100644 100644 100644 a b src/a.go\n" +
		"1 A. N... 100644 100644 100644 a b new.go\n" +
		"1 .D N... 100644 100644 000000 a b gone.go\n" +
		"2 R. N... 100644 100644 100644 a b R100 new2.go\told2.go\n" +
		"? untracked.txt\n" +
		"u UU N... 1 2 3 4 a b c conflict.go\n"
	want := Info{Branch: "main", Added: 2, Modified: 3, Deleted: 1, OK: true}
	if got := Parse(text); got != want {
		t.Errorf("Parse = %+v，期望 %+v", got, want)
	}
}

func TestParseWithoutBranchLineIsNotOK(t *testing.T) {
	if got := Parse(""); got.OK {
		t.Errorf("空输出不应算读到: %+v", got)
	}
}
