package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/petauron/catalog/catalog"
)

func nativeTaskFixture(t *testing.T) DeploymentTask {
	t.Helper()
	task := packageTestTask(t)
	task.Manifest.Images = nil
	task.Manifest.Runtime = nativePlanFixture().Runtime
	task.Manifest.Artifacts = []catalog.Artifact{{Name: "bundle", OperatingSystem: "linux", Architecture: "amd64", URL: "https://example.com/bundle.tar.gz", SHA256: strings.Repeat("a", 64), Format: "tar.gz"}}
	task.AuthorizedCapabilities = []string{"root", "native-services"}
	packageTaskDigest(t, &task)
	return task
}

func TestNativeTaskRequiresExactCapabilitiesAndVersions(t *testing.T) {
	task := nativeTaskFixture(t)
	if err := validatePackageTask(task); err != nil {
		t.Fatal(err)
	}
	task.AuthorizedCapabilities = []string{"root"}
	if err := validatePackageTask(task); err == nil {
		t.Fatal("native task without capability accepted")
	}
	task = nativeTaskFixture(t)
	task.Manifest.Runtime.Version = 3
	packageTaskDigest(t, &task)
	if err := validatePackageTask(task); err == nil {
		t.Fatal("future native task accepted")
	}
}

func TestNativeReceiptRecordsContractAndRejectsImplicitMigration(t *testing.T) {
	task := nativeTaskFixture(t)
	backend := &fakePackageBackend{}
	executor := PackageExecutor{StateDirectory: packageTestDirectory(t), Backend: backend}
	result, err := executor.Deploy(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Resources.RuntimeVersion != 2 {
		t.Fatal("native contract not receipted")
	}
	// An old receipt must not be interpreted as a v2 bundle based only on kind.
	result.Resources.RuntimeVersion = 0
	if err := executor.save(result.Resources); err != nil {
		t.Fatal(err)
	}
	task.Operation = "upgrade"
	task.ID = "upgrade"
	backend.calls = nil
	if _, err := executor.Deploy(context.Background(), task); err == nil {
		t.Fatal("implicit v1 to v2 migration allowed")
	}
	if len(backend.calls) != 0 {
		t.Fatal("migration rejection occurred after backend mutation")
	}
	task.PackageMaintenance = &PackageMaintenanceTask{Action: "backup"}
	_, err = maintainPackage(context.Background(), executor, &NativeServicesPackageBackend{}, task)
	if err == nil || !strings.Contains(err.Error(), "runtime contract") {
		t.Fatal("maintenance ignored installed contract", err)
	}
}
