package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/petauron/catalog/catalog"
)

func TestDockerPackagePreservesImageCommandDefaults(t *testing.T) {
	command := json.RawMessage(`"/app/service"`)
	argument := json.RawMessage(`"serve"`)
	for _, scenario := range []struct {
		name      string
		command   []catalog.Value
		arguments []catalog.Value
		wantEntry []string
		wantArgs  []string
	}{
		{name: "image defaults"},
		{name: "empty defaults", command: []catalog.Value{}, arguments: []catalog.Value{}},
		{name: "arguments only", arguments: []catalog.Value{{Literal: &argument}}, wantArgs: []string{"serve"}},
		{name: "explicit command", command: []catalog.Value{{Literal: &command}}, arguments: []catalog.Value{{Literal: &argument}}, wantEntry: []string{"/app/service"}, wantArgs: []string{"serve"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			task := packageTestTask(t)
			task.Manifest.Runtime.Docker.Containers[0].Command = scenario.command
			task.Manifest.Runtime.Docker.Containers[0].Arguments = scenario.arguments
			plans, err := compileDockerPackage(task, &InstanceResources{ApplicationID: task.ApplicationID})
			if err != nil {
				t.Fatal(err)
			}
			// Assert the actual Docker request shape: [] clears the entrypoint;
			// null retains the image default. Length-only comparisons miss this.
			raw, err := json.Marshal(plans[0].options.Config)
			if err != nil {
				t.Fatal(err)
			}
			var request struct {
				Entrypoint []string
				Cmd        []string
			}
			if err := json.Unmarshal(raw, &request); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(request.Entrypoint, scenario.wantEntry) || !reflect.DeepEqual(request.Cmd, scenario.wantArgs) {
				t.Fatalf("Docker command overrides: entrypoint=%#v cmd=%#v", request.Entrypoint, request.Cmd)
			}
		})
	}
}
