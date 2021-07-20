package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ghodss/yaml"

	devfile "github.com/devfile/api/v2/pkg/apis/workspaces/v1alpha2"
	"github.com/devfile/library/pkg/devfile/parser/data/v2/common"
	parsercommon "github.com/devfile/library/pkg/devfile/parser/data/v2/common"
	"github.com/openshift/odo/pkg/kclient"
	"github.com/openshift/odo/pkg/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog"

	olm "github.com/operator-framework/api/pkg/operators/v1alpha1"

	applabels "github.com/openshift/odo/pkg/application/labels"
	componentlabels "github.com/openshift/odo/pkg/component/labels"
	"github.com/openshift/odo/pkg/occlient"
	"github.com/pkg/errors"

	"github.com/devfile/library/pkg/devfile/parser"
	servicebinding "github.com/redhat-developer/service-binding-operator/api/v1alpha1"
)

// GetCSV checks if the CR provided by the user in the YAML file exists in the namesapce
// It returns a CR (string representation) and CSV (Operator) upon successfully
// able to find them, an error otherwise.
func GetCSV(client *kclient.Client, crd map[string]interface{}) (string, olm.ClusterServiceVersion, error) {
	cr := crd["kind"].(string)
	csvs, err := client.ListClusterServiceVersions()
	if err != nil {
		return cr, olm.ClusterServiceVersion{}, err
	}

	csv, err := doesCRExist(cr, csvs)
	if err != nil {
		return cr, olm.ClusterServiceVersion{},
			fmt.Errorf("could not find specified service/custom resource: %s; please check the \"kind\" field in the yaml (it's case-sensitive)", cr)
	}
	return cr, csv, nil
}

// doesCRExist checks if the CR exists in the CSV
func doesCRExist(kind string, csvs *olm.ClusterServiceVersionList) (olm.ClusterServiceVersion, error) {
	for _, csv := range csvs.Items {
		for _, operatorCR := range csv.Spec.CustomResourceDefinitions.Owned {
			if kind == operatorCR.Kind {
				return csv, nil
			}
		}
	}
	return olm.ClusterServiceVersion{}, errors.New("could not find the requested cluster resource")
}

// CreateOperatorService creates new service (actually a Deployment) from OperatorHub
func CreateOperatorService(client *kclient.Client, group, version, resource string, CustomResourceDefinition map[string]interface{}) error {
	err := client.CreateDynamicResource(CustomResourceDefinition, group, version, resource)
	if err != nil {
		return errors.Wrap(err, "unable to create operator backed service")
	}
	return nil
}

// DeleteServiceAndUnlinkComponents will delete the service with the provided `name`
// it also removes links to that service in components of the application
func DeleteServiceAndUnlinkComponents(client *occlient.Client, serviceName string, applicationName string) error {
	// first we attempt to delete the service instance itself
	labels := componentlabels.GetLabels(serviceName, applicationName, false)
	err := client.GetKubeClient().DeleteServiceInstance(labels)
	if err != nil {
		return err
	}

	// lookup all the components of the application
	applicationSelector := fmt.Sprintf("%s=%s", applabels.ApplicationLabel, applicationName)
	componentsDCs, err := client.ListDeploymentConfigs(applicationSelector)
	if err != nil {
		return errors.Wrapf(err, "unable to list the components in order to check if they need to be unlinked")
	}

	// go through the components and check if they have the service name as part of the envFrom configuration
	for _, dc := range componentsDCs {
		for _, envFromSourceName := range dc.Spec.Template.Spec.Containers[0].EnvFrom {
			if envFromSourceName.SecretRef.Name == serviceName {
				if componentName, ok := dc.Labels[componentlabels.ComponentLabel]; ok {
					err := client.UnlinkSecret(serviceName, componentName, applicationName)
					if err != nil {
						klog.Warningf("Unable to unlink component %s from service", componentName)
					} else {
						klog.V(2).Infof("Component %s was successfully unlinked from service", componentName)
					}
				}
			}
		}
	}

	return nil
}

// DeleteOperatorService deletes an Operator backed service
// TODO: make it unlink the service from component as a part of
// https://github.com/openshift/odo/issues/3563
func DeleteOperatorService(client *kclient.Client, serviceName string) error {
	kind, name, err := SplitServiceKindName(serviceName)
	if err != nil {
		return errors.Wrapf(err, "Refer %q to see list of running services", serviceName)
	}

	csv, err := client.GetCSVWithCR(kind)
	if err != nil {
		return err
	}

	if csv == nil {
		return fmt.Errorf("unable to find any Operator providing the service %q", kind)
	}

	crs := client.GetCustomResourcesFromCSV(csv)
	var cr *olm.CRDDescription

	for _, c := range *crs {
		customResource := c
		if customResource.Kind == kind {
			cr = &customResource
			break
		}
	}

	group, version, resource, err := GetGVRFromCR(cr)
	if err != nil {
		return err
	}

	return client.DeleteDynamicResource(name, group, version, resource)
}

// ListOperatorServices lists all operator backed services.
// It returns list of services, slice of services that it failed (if any) to list and error (if any)
func ListOperatorServices(client *kclient.Client) ([]unstructured.Unstructured, []string, error) {
	klog.V(4).Info("Getting list of services")

	// First let's get the list of all the operators in the namespace
	csvs, err := client.ListClusterServiceVersions()
	if err == kclient.ErrNoSuchOperator {
		return nil, nil, err
	}

	if err != nil {
		return nil, nil, errors.Wrap(err, "Unable to list operator backed services")
	}

	var allCRInstances []unstructured.Unstructured
	var failedListingCR []string

	// let's get the Services a.k.a Custom Resources (CR) defined by each operator, one by one
	for _, csv := range csvs.Items {
		clusterServiceVersion := csv
		klog.V(4).Infof("Getting services started from operator: %s", clusterServiceVersion.Name)
		customResources := client.GetCustomResourcesFromCSV(&clusterServiceVersion)

		// list and write active instances of each service/CR
		var instances []unstructured.Unstructured
		for _, cr := range *customResources {
			customResource := cr

			list, err := GetCRInstances(client, &customResource)
			if err != nil {
				crName := strings.Join([]string{csv.Name, cr.Kind}, "/")
				klog.V(4).Infof("Failed to list instances of %q with error: %s", crName, err.Error())
				failedListingCR = append(failedListingCR, crName)
				break
			}

			if len(list.Items) > 0 {
				instances = append(instances, list.Items...)
			}
		}

		// assuming there are more than one instances of a CR
		allCRInstances = append(allCRInstances, instances...)
	}

	return allCRInstances, failedListingCR, nil
}

// GetGVKRFromCR returns values for group, version, kind and resource for a
// given Custom Resource (CR)
func GetGVKRFromCR(cr olm.CRDDescription) (group, version, kind, resource string, err error) {
	return getGVKRFromCR(cr)
}

func getGVKRFromCR(cr olm.CRDDescription) (group, version, kind, resource string, err error) {
	version = cr.Version
	kind = cr.Kind

	gr := strings.SplitN(cr.Name, ".", 2)
	if len(gr) != 2 {
		err = fmt.Errorf("couldn't split Custom Resource's name into two: %s", cr.Name)
		return
	}
	resource = gr[0]
	group = gr[1]

	return
}

func GetGVRFromOperator(csv olm.ClusterServiceVersion, cr string) (group, version, resource string, err error) {
	for _, customresource := range csv.Spec.CustomResourceDefinitions.Owned {
		custRes := customresource
		if custRes.Kind == cr {
			return GetGVRFromCR(&custRes)
		}
	}
	return "", "", "", fmt.Errorf("couldn't parse group, version, resource from Operator %q", csv.Name)
}

// GetGVRFromCR parses and returns the values for group, version and resource
// for a given Custom Resource (CR).
func GetGVRFromCR(cr *olm.CRDDescription) (group, version, resource string, err error) {
	version = cr.Version

	gr := strings.SplitN(cr.Name, ".", 2)
	if len(gr) != 2 {
		err = fmt.Errorf("couldn't split Custom Resource's name into two: %s", cr.Name)
		return
	}
	resource = gr[0]
	group = gr[1]

	return
}

func GetGVKFromCR(cr *olm.CRDDescription) (group, version, kind string, err error) {
	return getGVKFromCR(cr)
}

// getGVKFromCR parses and returns the values for group, version and resource
// for a given Custom Resource (CR).
func getGVKFromCR(cr *olm.CRDDescription) (group, version, kind string, err error) {
	kind = cr.Kind
	version = cr.Version

	gr := strings.SplitN(cr.Name, ".", 2)
	if len(gr) != 2 {
		err = fmt.Errorf("couldn't split Custom Resource's name into two: %s", cr.Name)
		return
	}
	group = gr[1]

	return
}

// GetAlmExample fetches the ALM example from an Operator's definition. This
// example contains the example yaml to be used to spin up a service for a
// given CR in an Operator
func GetAlmExample(csv olm.ClusterServiceVersion, cr, serviceType string) (almExample map[string]interface{}, err error) {
	var almExamples []map[string]interface{}

	val, ok := csv.Annotations["alm-examples"]
	if ok {
		err = json.Unmarshal([]byte(val), &almExamples)
		if err != nil {
			return nil, errors.Wrap(err, "unable to unmarshal alm-examples")
		}
	} else {
		// There's no alm examples in the CSV's definition
		return nil,
			fmt.Errorf("could not find alm-examples in %q Operator's definition", cr)
	}

	almExample, err = getAlmExample(almExamples, cr, serviceType)
	if err != nil {
		return nil, err
	}

	return almExample, nil
}

// getAlmExample returns the alm-example for exact service of an Operator
func getAlmExample(almExamples []map[string]interface{}, crd, operator string) (map[string]interface{}, error) {
	for _, example := range almExamples {
		if example["kind"].(string) == crd {
			return example, nil
		}
	}
	return nil, errors.Errorf("could not find example yaml definition for %q service in %q Operator's definition.", crd, operator)
}

// GetCRInstances fetches and returns instances of the CR provided in the
// "customResource" field. It also returns error (if any)
func GetCRInstances(client *kclient.Client, customResource *olm.CRDDescription) (*unstructured.UnstructuredList, error) {
	klog.V(4).Infof("Getting instances of: %s\n", customResource.Name)

	group, version, resource, err := GetGVRFromCR(customResource)
	if err != nil {
		return nil, err
	}

	instances, err := client.ListDynamicResource(group, version, resource)
	if err != nil {
		return nil, err
	}

	return instances, nil
}

// IsOperatorServiceNameValid checks if the provided name follows
// <service-type>/<service-name> format. For example: "EtcdCluster/example" is
// a valid service name but "EtcdCluster/", "EtcdCluster", "example" aren't.
func IsOperatorServiceNameValid(name string) (string, string, error) {
	checkName := strings.SplitN(name, "/", 2)

	if len(checkName) != 2 || checkName[0] == "" || checkName[1] == "" {
		return "", "", fmt.Errorf("invalid service name. Must adhere to <service-type>/<service-name> formatting. For example: %q. Execute %q for list of services", "EtcdCluster/example", "odo service list")
	}
	return checkName[0], checkName[1], nil
}

// OperatorSvcExists checks whether an Operator backed service with given name
// exists or not. It takes 'serviceName' of the format
// '<service-kind>/<service-name>'. For example: EtcdCluster/example.
// It doesn't bother about application since
// https://github.com/openshift/odo/issues/2801 is blocked
func OperatorSvcExists(client *kclient.Client, serviceName string) (bool, error) {
	kind, name, err := SplitServiceKindName(serviceName)
	if err != nil {
		return false, errors.Wrapf(err, "Refer %q to see list of running services", serviceName)
	}

	// Get the CSV (Operator) that provides the CR
	csv, err := client.GetCSVWithCR(kind)
	if err != nil {
		return false, err
	}

	// Get the specific CR that matches "kind"
	crs := client.GetCustomResourcesFromCSV(csv)

	var cr *olm.CRDDescription
	for _, custRes := range *crs {
		c := custRes
		if c.Kind == kind {
			cr = &c
			break
		}
	}

	// Get instances of the specific CR
	crInstances, err := GetCRInstances(client, cr)
	if err != nil {
		return false, err
	}

	for _, s := range crInstances.Items {
		if s.GetKind() == kind && s.GetName() == name {
			return true, nil
		}
	}

	return false, nil
}

// SplitServiceKindName splits the service name provided for deletion by the
// user. It has to be of the format <service-kind>/<service-name>. Example: EtcdCluster/myetcd
func SplitServiceKindName(serviceName string) (string, string, error) {
	sn := strings.SplitN(serviceName, "/", 2)
	if len(sn) != 2 || sn[0] == "" || sn[1] == "" {
		return "", "", fmt.Errorf("couldn't split %q into exactly two", serviceName)
	}

	kind := sn[0]
	name := sn[1]

	return kind, name, nil
}

// IsCSVSupported checks if the cluster supports resources of type ClusterServiceVersion
func IsCSVSupported() (bool, error) {
	client, err := occlient.New()
	if err != nil {
		return false, err
	}

	return client.GetKubeClient().IsCSVSupported()
}

// IsDefined checks if a service with the given name is defined in a DevFile
func IsDefined(name string, devfileObj parser.DevfileObj) (bool, error) {
	components, err := devfileObj.Data.GetComponents(common.DevfileOptions{})
	if err != nil {
		return false, err
	}
	for _, c := range components {
		if c.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// ListDevfileLinks returns the names of the links defined in a Devfile
func ListDevfileLinks(devfileObj parser.DevfileObj) ([]string, error) {
	if devfileObj.Data == nil {
		return nil, nil
	}
	components, err := devfileObj.Data.GetComponents(common.DevfileOptions{
		ComponentOptions: parsercommon.ComponentOptions{ComponentType: devfile.KubernetesComponentType},
	})
	if err != nil {
		return nil, err
	}
	var services []string
	for _, c := range components {
		var u unstructured.Unstructured
		err = yaml.Unmarshal([]byte(c.Kubernetes.Inlined), &u)
		if err != nil {
			return nil, err
		}
		if !isLinkResource(u.GetKind()) {
			continue
		}
		var sbr servicebinding.ServiceBinding
		js, err := u.MarshalJSON()
		if err != nil {
			return nil, err
		}
		err = json.Unmarshal(js, &sbr)
		if err != nil {
			return nil, err
		}
		sbrServices := sbr.Spec.Services
		if len(sbrServices) != 1 {
			return nil, errors.New("ServiceBinding should have only one service")
		}
		service := sbrServices[0]
		if service.Kind == "Service" {
			services = append(services, service.Name)
		} else {
			services = append(services, service.Kind+"/"+service.Name)
		}
	}
	return services, nil
}

// ListDevfileServices returns the names of the services defined in a Devfile
func ListDevfileServices(devfileObj parser.DevfileObj) ([]string, error) {
	if devfileObj.Data == nil {
		return nil, nil
	}
	components, err := devfileObj.Data.GetComponents(common.DevfileOptions{
		ComponentOptions: parsercommon.ComponentOptions{ComponentType: devfile.KubernetesComponentType},
	})
	if err != nil {
		return nil, err
	}
	var services []string
	for _, c := range components {
		var u unstructured.Unstructured
		err = yaml.Unmarshal([]byte(c.Kubernetes.Inlined), &u)
		if err != nil {
			return nil, err
		}
		services = append(services, strings.Join([]string{u.GetKind(), c.Name}, "/"))
	}
	return services, nil
}

// FindDevfileServiceBinding returns the name of the ServiceBinding defined in a Devfile matching kind and name
func FindDevfileServiceBinding(devfileObj parser.DevfileObj, kind string, name string) (string, bool, error) {
	if devfileObj.Data == nil {
		return "", false, nil
	}
	components, err := devfileObj.Data.GetComponents(common.DevfileOptions{
		ComponentOptions: parsercommon.ComponentOptions{ComponentType: devfile.KubernetesComponentType},
	})
	if err != nil {
		return "", false, err
	}

	for _, c := range components {
		var u unstructured.Unstructured
		err = yaml.Unmarshal([]byte(c.Kubernetes.Inlined), &u)
		if err != nil {
			return "", false, err
		}

		if isLinkResource(u.GetKind()) {
			var sbr servicebinding.ServiceBinding
			err = yaml.Unmarshal([]byte(c.Kubernetes.Inlined), &sbr)
			if err != nil {
				return "", false, err
			}
			services := sbr.Spec.Services
			if len(services) != 1 {
				continue
			}
			service := services[0]
			if service.Kind == kind && service.Name == name {
				return u.GetName(), true, nil
			}
		}
	}
	return "", false, nil
}

// AddKubernetesComponentToDevfile adds service definition to devfile as an inlined Kubernetes component
func AddKubernetesComponentToDevfile(crd, name string, devfileObj parser.DevfileObj) error {
	err := devfileObj.Data.AddComponents([]devfile.Component{{
		Name: name,
		ComponentUnion: devfile.ComponentUnion{
			Kubernetes: &devfile.KubernetesComponent{
				K8sLikeComponent: devfile.K8sLikeComponent{
					BaseComponent: devfile.BaseComponent{},
					K8sLikeComponentLocation: devfile.K8sLikeComponentLocation{
						Inlined: crd,
					},
				},
			},
		},
	}})
	if err != nil {
		return err
	}

	return devfileObj.WriteYamlDevfile()
}

// DeleteKubernetesComponentFromDevfile deletes an inlined Kubernetes component from devfile, if one exists
func DeleteKubernetesComponentFromDevfile(name string, devfileObj parser.DevfileObj) error {
	components, err := devfileObj.Data.GetComponents(common.DevfileOptions{})
	if err != nil {
		return err
	}

	found := false
	for _, c := range components {
		if c.Name == name {
			err = devfileObj.Data.DeleteComponent(c.Name)
			if err != nil {
				return err
			}
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("could not find the service %q in devfile", name)
	}

	return devfileObj.WriteYamlDevfile()
}

// DynamicCRD holds the original CR obtained from the Operator (a CSV), or user
// (when they use --from-file flag), and few other attributes that are likely
// to be used to validate a CRD before creating a service from it
type DynamicCRD struct {
	// contains the CR as obtained from CSV or user
	OriginalCRD map[string]interface{}
}

func NewDynamicCRD() *DynamicCRD {
	return &DynamicCRD{}
}

// ValidateMetadataInCRD validates if the CRD has metadata.name field and returns an error
func (d *DynamicCRD) ValidateMetadataInCRD() error {
	metadata, ok := d.OriginalCRD["metadata"].(map[string]interface{})
	if !ok {
		// this condition is satisfied if there's no metadata at all in the provided CRD
		return fmt.Errorf("couldn't find \"metadata\" in the yaml; need metadata start the service")
	}

	if _, ok := metadata["name"].(string); ok {
		// found the metadata.name; no error
		return nil
	}
	return fmt.Errorf("couldn't find metadata.name in the yaml; provide a name for the service")
}

// SetServiceName modifies the CRD to contain user provided name on the CLI
// instead of using the default one in almExample
func (d *DynamicCRD) SetServiceName(name string) {
	metaMap := d.OriginalCRD["metadata"].(map[string]interface{})

	for k := range metaMap {
		if k == "name" {
			metaMap[k] = name
			return
		}
	}
	metaMap["name"] = name
}

// GetServiceNameFromCRD fetches the service name from metadata.name field of the CRD
func (d *DynamicCRD) GetServiceNameFromCRD() (string, error) {
	metadata, ok := d.OriginalCRD["metadata"].(map[string]interface{})
	if !ok {
		// this condition is satisfied if there's no metadata at all in the provided CRD
		return "", fmt.Errorf("couldn't find \"metadata\" in the yaml; need metadata.name to start the service")
	}

	if name, ok := metadata["name"].(string); ok {
		// found the metadata.name; no error
		return name, nil
	}
	return "", fmt.Errorf("couldn't find metadata.name in the yaml; provide a name for the service")
}

// AddComponentLabelsToCRD appends odo labels to CRD if "labels" field already exists in metadata; else creates labels
func (d *DynamicCRD) AddComponentLabelsToCRD(labels map[string]string) {
	metaMap := d.OriginalCRD["metadata"].(map[string]interface{})

	for k := range metaMap {
		if k == "labels" {
			metaLabels := metaMap["labels"].(map[string]interface{})
			for i := range labels {
				metaLabels[i] = labels[i]
			}
			return
		}
	}
	// if metadata doesn't have 'labels' field, we set it up
	metaMap["labels"] = labels
}

// PushServiceFromKubernetesInlineComponents updates service(s) from Kubernetes Inlined component in a devfile by creating new ones or removing old ones
// returns true if the component needs to be restarted (when a service binding has been created or deleted)
func PushServiceFromKubernetesInlineComponents(client *kclient.Client, k8sComponents []devfile.Component, labels map[string]string) (bool, error) {

	// check csv support before proceeding
	csvSupported, err := IsCSVSupported()
	if err != nil || !csvSupported {
		return false, err
	}

	type DeployedInfo struct {
		DoesDeleteRestartsComponent bool
		Kind                        string
		Name                        string
	}

	deployed := map[string]DeployedInfo{}

	deployedServices, _, err := ListOperatorServices(client)
	if err != nil && err != kclient.ErrNoSuchOperator {
		// We ignore ErrNoSuchOperator error as we can deduce Operator Services are not installed
		return false, err
	}
	for _, svc := range deployedServices {
		name := svc.GetName()
		kind := svc.GetKind()
		deployedLabels := svc.GetLabels()
		if deployedLabels[applabels.ManagedBy] == "odo" && deployedLabels[componentlabels.ComponentLabel] == labels[componentlabels.ComponentLabel] {
			deployed[kind+"/"+name] = DeployedInfo{
				DoesDeleteRestartsComponent: isLinkResource(kind),
				Kind:                        kind,
				Name:                        name,
			}
		}
	}
	needRestart := false
	madeChange := false

	// create an object on the kubernetes cluster for all the Kubernetes Inlined components
	for _, c := range k8sComponents {
		// get the string representation of the YAML definition of a CRD
		strCRD := c.Kubernetes.Inlined

		// convert the YAML definition into map[string]interface{} since it's needed to create dynamic resource
		d := NewDynamicCRD()
		err := yaml.Unmarshal([]byte(strCRD), &d.OriginalCRD)
		if err != nil {
			return false, err
		}

		cr, csv, err := GetCSV(client, d.OriginalCRD)
		if err != nil {
			return false, err
		}

		var group, version, kind, resource string
		for _, crd := range csv.Spec.CustomResourceDefinitions.Owned {
			if crd.Kind == cr {
				group, version, kind, resource, err = getGVKRFromCR(crd)
				if err != nil {
					return false, err
				}
				break
			}
		}

		// add labels to the CRD before creation
		d.AddComponentLabelsToCRD(labels)

		crdName, ok := getCRDName(d.OriginalCRD)
		if !ok {
			continue
		}

		if _, found := deployed[cr+"/"+crdName]; !found && isLinkResource(cr) {
			// If creating the ServiceBinding, the component will restart
			needRestart = true
		}

		delete(deployed, cr+"/"+crdName)

		// create the service on cluster
		err = client.CreateDynamicResource(d.OriginalCRD, group, version, resource)
		if err != nil {
			if strings.Contains(err.Error(), "already exists") {
				// this could be the case when "odo push" was executed after making change to code but there was no change to the service itself
				// TODO: better way to handle this might be introduced by https://github.com/openshift/odo/issues/4553
				continue // this ensures that services slice is not updated
			} else {
				return false, err
			}
		}

		name, _ := d.GetServiceNameFromCRD() // ignoring error because invalid yaml won't be inserted into devfile through odo
		if isLinkResource(cr) {
			log.Successf("Created link %q on the cluster; component will be restarted", name)
		} else {
			log.Successf("Created service %q on the cluster; refer %q to know how to link it to the component", strings.Join([]string{kind, name}, "/"), "odo link -h")
		}
		madeChange = true
	}

	for key, val := range deployed {
		err = DeleteOperatorService(client, key)
		if err != nil {
			return false, err

		}

		if isLinkResource(val.Kind) {
			log.Successf("Deleted link %q on the cluster; component will be restarted", val.Name)
		} else {
			log.Successf("Deleted service %q from the cluster", key)
		}
		madeChange = true

		if val.DoesDeleteRestartsComponent {
			needRestart = true
		}
	}

	if !madeChange {
		log.Success("Services and Links are in sync with the cluster, no changes are required")
	}

	return needRestart, nil
}

// UpdateKubernetesInlineComponentsOwnerReferences adds an owner reference to an inlined Kubernetes resource
// if not already present in the list of owner references
func UpdateKubernetesInlineComponentsOwnerReferences(client *kclient.Client, k8sComponents []devfile.Component, ownerReference metav1.OwnerReference) error {
	for _, c := range k8sComponents {
		// get the string representation of the YAML definition of a CRD
		strCRD := c.Kubernetes.Inlined

		// convert the YAML definition into map[string]interface{} since it's needed to create dynamic resource
		d := NewDynamicCRD()
		err := yaml.Unmarshal([]byte(strCRD), &d.OriginalCRD)
		if err != nil {
			return err
		}

		cr, csv, err := GetCSV(client, d.OriginalCRD)
		if err != nil {
			return err
		}

		var group, version, resource string
		for _, crd := range csv.Spec.CustomResourceDefinitions.Owned {
			if crd.Kind == cr {
				group, version, _, resource, err = getGVKRFromCR(crd)
				if err != nil {
					return err
				}
				break
			}
		}

		crdName, ok := getCRDName(d.OriginalCRD)
		if !ok {
			continue
		}

		u, err := client.GetDynamicResource(group, version, resource, crdName)
		if err != nil {
			return err
		}

		found := false
		for _, ownerRef := range u.GetOwnerReferences() {
			if ownerRef.UID == ownerReference.UID {
				found = true
				break
			}
		}
		if found {
			continue
		}
		u.SetOwnerReferences(append(u.GetOwnerReferences(), ownerReference))

		err = client.UpdateDynamicResource(group, version, resource, crdName, u)
		if err != nil {
			return err
		}
	}
	return nil
}

func getCRDName(crd map[string]interface{}) (string, bool) {
	metadata, ok := crd["metadata"].(map[string]interface{})
	if !ok {
		return "", false
	}
	name, ok := metadata["name"].(string)
	if !ok {
		return "", false
	}
	return name, true
}

func isLinkResource(kind string) bool {
	return kind == "ServiceBinding"
}
