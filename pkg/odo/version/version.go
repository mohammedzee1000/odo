package version

import (
	"github.com/openshift/odo/pkg/occlient"
	odoversion "github.com/openshift/odo/pkg/version"
	"os"
	"strings"
)

type OdoVersion struct {
	Version       string               `json:"Version"`
	GitCommit     string               `json:"GitCommit"`
	ServerInfo    *occlient.ServerInfo `json:"ServerInfo"`
	KubernetesEnv map[string]string    `json:"KubernetesEnv"`
}

//NewOdoVersion initializes a NewOdoVersionObject
func NewOdoVersion(client bool) *OdoVersion {
	ov := &OdoVersion{
		Version:   odoversion.VERSION,
		GitCommit: odoversion.GITCOMMIT,
	}
	ov.getKubernetesEnv()
	if !client {
		ov.getClientVersion()
	}
	return ov
}

//getClientVersion gets the kubernetes client version
func (ov *OdoVersion) getClientVersion() {
	// Let's fetch the info about the server, ignoring errors
	client, err := occlient.New()
	if err == nil {
		ov.ServerInfo, _ = client.GetServerVersion()
	}
}

//getKubernetesEnv gets all kubernetes related env variables
func (ov *OdoVersion) getKubernetesEnv() {
	ov.KubernetesEnv = make(map[string]string)
	for _, v := range os.Environ() {
		if strings.HasPrefix(v, "KUBECTL_") {
			sp := strings.Split(v, "=")
			ov.KubernetesEnv[sp[0]] = sp[1]
		}
	}
}
