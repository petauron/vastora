package agent

import (
	"github.com/petauron/catalog/catalog"
)

func komariTestTask(downloadURL, digest string) DeploymentTask {
	return DeploymentTask{
		ID: "komari-install", ApplicationID: "komari-application", AppKey: komariKey, Operation: "install",
		Manifest: catalog.AppManifest{
			ID: "komari-agent", Version: "1.2.60", License: "MIT", HostAccess: true,
			Name:        catalog.LocalizedText{English: "Komari Agent", SimplifiedChinese: "Komari 探针"},
			Description: catalog.LocalizedText{English: "Komari monitoring agent.", SimplifiedChinese: "Komari 监控探针。"},
			Artifacts: []catalog.Artifact{
				{Name: "komari-agent", OperatingSystem: "linux", Architecture: "amd64", URL: downloadURL, SHA256: digest},
				{Name: "komari-agent", OperatingSystem: "linux", Architecture: "arm64", URL: downloadURL, SHA256: digest},
			},
			Config: []catalog.ConfigField{
				{Key: "endpoint", Type: "string", Label: catalog.LocalizedText{English: "Endpoint", SimplifiedChinese: "面板地址"}, Description: catalog.LocalizedText{English: "Komari endpoint.", SimplifiedChinese: "Komari 面板地址。"}, Required: true},
				{Key: "token", Type: "string", Label: catalog.LocalizedText{English: "Token", SimplifiedChinese: "令牌"}, Description: catalog.LocalizedText{English: "Agent token.", SimplifiedChinese: "探针令牌。"}, Required: true, Secret: true},
			},
		},
		Config: []byte(`{"endpoint":"https://komari.example.test/"}`), Secrets: []byte(`{"token":"secret-token"}`),
	}
}
