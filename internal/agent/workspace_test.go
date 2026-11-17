package agent

import "testing"

func TestRepoKey(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:org/repo.git":              "github.com_org_repo",
		"https://user:pw@github.com/org/repo.git/": "github.com_org_repo",
		"/tmp/x/sample":                            "tmp_x_sample",
	} {
		if got := RepoKey(in); got != want {
			t.Errorf("RepoKey(%q)=%q want %q", in, got, want)
		}
	}
}

func TestWSURL(t *testing.T) {
	if wsURL("https://a.b") != "wss://a.b/api/agent/ws" || wsURL("http://a:1") != "ws://a:1/api/agent/ws" {
		t.Fatal("wsURL 转换不对")
	}
}
