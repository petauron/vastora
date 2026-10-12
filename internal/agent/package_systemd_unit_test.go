package agent

import (
	"strings"
	"testing"

	"github.com/petauron/catalog/catalog"
)

func TestPackageSystemdDirectivePaths(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"/etc/example.env", "/etc/example.env"},
		{"/etc/example file.env", "/etc/example file.env"},
		{"/etc/example%$file.env", "/etc/example%%$file.env"},
		{"/etc/example\"file.env", "/etc/example\"file.env"},
	} {
		got, err := unitDirectivePath(tc.input)
		if err != nil || got != tc.want {
			t.Fatalf("path %q: got %q, error %v", tc.input, got, err)
		}
	}
	for _, input := range []string{"", "relative.env", "\"/etc/example.env\"", "/etc/../example.env", "/etc//example.env", "/etc/example.env ", "/etc/example\\file.env", "/etc/example\x00.env", "/etc/example\t.env", "/etc/example\r.env", "/etc/example.env\nExecStart=/evil"} {
		if _, err := unitDirectivePath(input); err == nil {
			t.Fatalf("accepted unsafe path %q", input)
		}
	}
}

func TestPackageSystemdCredentialPathsRejectExecutorWhitespace(t *testing.T) {
	for _, path := range []string{"/etc/example file", "/etc/example\tfile", "/etc/example\nfile", "/etc/example\u00a0file"} {
		if _, err := unitCredentialPath(path); err == nil {
			t.Fatalf("accepted credential path %q", path)
		}
	}
	if got, err := unitCredentialPath("/etc/example%$file"); err != nil || got != "/etc/example%%$file" {
		t.Fatalf("credential path: got %q, error %v", got, err)
	}
}

func TestPackageSystemdUnitSeparatesPathsAndExecWords(t *testing.T) {
	task := packageTestTask(t)
	task.Manifest.Runtime = &catalog.RuntimeSpec{Kind: "systemd", Systemd: &catalog.SystemdRuntime{}}
	receipt := &InstanceResources{}
	unit, err := compilePackageUnit(task, receipt, "/opt/example/app", "/etc/example % $ file.env", map[string]string{"token": "/etc/example%$credential"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"EnvironmentFile=/etc/example %% $ file.env\n", "LoadCredential=token:/etc/example%%$credential\n", "ExecStart=\"/opt/example/app\"\n"} {
		if !strings.Contains(string(unit), line) {
			t.Fatalf("missing directive %q in unit:\n%s", line, unit)
		}
	}
	if got := unitExecQuote("$HOME %n\nsecond"); got != `"$$HOME %%n\nsecond"` {
		t.Fatalf("ExecStart argument escaping changed: %s", got)
	}
	for _, credential := range []bool{false, true} {
		environment, credentials := "/etc/example.env", map[string]string{"token": "/etc/example.token"}
		if credential {
			credentials["token"] += "\nExecStart=/evil"
		} else {
			environment += "\nExecStart=/evil"
		}
		if _, err := compilePackageUnit(task, receipt, "/opt/example/app", environment, credentials, nil, nil, nil); err == nil {
			t.Fatal("compiler accepted a directive injection")
		}
	}
}
