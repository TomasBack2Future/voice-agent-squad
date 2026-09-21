package grokreview

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRepositoryAccessUsesInstallationTokenWithoutSampling(t *testing.T) {
	executor := &fakeCommandExecutor{outputs: [][]byte{[]byte(`[]`)}}
	client := testGitHubCLI(executor)
	if err := client.CheckRepositoryAccess(context.Background(), "owner/repo", "app-token"); err != nil {
		t.Fatal(err)
	}
	if len(executor.calls) != 1 {
		t.Fatal("expected one read-only probe")
	}
	call := executor.calls[0]
	if !containsExact(call.args, "repos/owner/repo/pulls?state=open&per_page=1") || !containsExact(call.env, "GH_TOKEN=app-token") {
		t.Fatal("wrong probe identity")
	}
	if strings.Contains(strings.Join(call.args, " "), "app-token") {
		t.Fatal("token exposed")
	}
}

func TestRepositoryAccessFailsClosed(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"message":"Not Found"}`, `invalid`} {
		client := testGitHubCLI(&fakeCommandExecutor{outputs: [][]byte{[]byte(body)}})
		if err := client.CheckRepositoryAccess(context.Background(), "owner/repo", "app-token"); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	executor := &fakeCommandExecutor{errors: []error{errors.New("HTTP 404")}}
	if err := testGitHubCLI(executor).CheckRepositoryAccess(context.Background(), "owner/repo", "app-token"); err == nil {
		t.Fatal("missing App access accepted")
	}
	executor = &fakeCommandExecutor{}
	if err := testGitHubCLI(executor).CheckRepositoryAccess(context.Background(), "owner/repo", ""); err == nil || len(executor.calls) != 0 {
		t.Fatal("missing token executed")
	}
}
