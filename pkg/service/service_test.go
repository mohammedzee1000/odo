package service

import (
	"github.com/devfile/api/v2/pkg/apis/workspaces/v1alpha2"
	devfile "github.com/devfile/api/v2/pkg/apis/workspaces/v1alpha2"
	"github.com/devfile/library/pkg/devfile/parser/data/v2/common"

	"github.com/devfile/library/pkg/devfile/parser"
	devfileCtx "github.com/devfile/library/pkg/devfile/parser/context"
	"github.com/devfile/library/pkg/devfile/parser/data"
	devfileFileSystem "github.com/devfile/library/pkg/testingutil/filesystem"
	"reflect"
	"testing"

	scv1beta1 "github.com/kubernetes-sigs/service-catalog/pkg/apis/servicecatalog/v1beta1"
	appsv1 "github.com/openshift/api/apps/v1"
	applabels "github.com/openshift/odo/pkg/application/labels"
	componentlabels "github.com/openshift/odo/pkg/component/labels"
	"github.com/openshift/odo/pkg/occlient"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
)

func TestDeleteServiceAndUnlinkComponents(t *testing.T) {
	const appName = "app"
	type args struct {
		ServiceName string
	}
	tests := []struct {
		name                       string
		args                       args
		serviceList                scv1beta1.ServiceInstanceList
		dcList                     appsv1.DeploymentConfigList
		expectedDCNamesToBeUpdated []string
		wantErr                    bool
	}{
		{
			name: "Case 1: Delete service that has linked component",
			args: args{
				ServiceName: "mysql",
			},
			wantErr:                    false,
			expectedDCNamesToBeUpdated: []string{"component-with-matching-link"},
			serviceList: scv1beta1.ServiceInstanceList{
				Items: []scv1beta1.ServiceInstance{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "mysql",
							Labels: map[string]string{
								applabels.ApplicationLabel:         appName,
								componentlabels.ComponentLabel:     "mysql",
								componentlabels.ComponentTypeLabel: "mysql-persistent",
							},
						},
					},
				},
			},
			dcList: appsv1.DeploymentConfigList{
				Items: []appsv1.DeploymentConfig{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "component-with-no-links" + "-" + appName,
							Labels: map[string]string{
								applabels.ApplicationLabel:     appName,
								componentlabels.ComponentLabel: "component-with-no-links",
							},
						},
						Spec: appsv1.DeploymentConfigSpec{
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name: "dummyContainer",
										},
									},
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "component-with-matching-link" + "-" + appName,
							Labels: map[string]string{
								applabels.ApplicationLabel:     appName,
								componentlabels.ComponentLabel: "component-with-matching-link",
							},
						},
						Spec: appsv1.DeploymentConfigSpec{
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											EnvFrom: []corev1.EnvFromSource{
												{
													SecretRef: &corev1.SecretEnvSource{
														LocalObjectReference: corev1.LocalObjectReference{
															Name: "mysql",
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "component-with-non-matching-link" + "-" + appName,
							Labels: map[string]string{
								applabels.ApplicationLabel:     appName,
								componentlabels.ComponentLabel: "component-with-non-matching-link",
							},
						},
						Spec: appsv1.DeploymentConfigSpec{
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											EnvFrom: []corev1.EnvFromSource{
												{
													SecretRef: &corev1.SecretEnvSource{
														LocalObjectReference: corev1.LocalObjectReference{
															Name: "other",
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		client, fakeClientSet := occlient.FakeNew()

		//fake the services listing
		fakeClientSet.ServiceCatalogClientSet.PrependReactor("list", "serviceinstances", func(action ktesting.Action) (bool, runtime.Object, error) {
			return true, &tt.serviceList, nil
		})

		// Fake the servicebinding delete
		fakeClientSet.ServiceCatalogClientSet.PrependReactor("delete", "servicebindings", func(action ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, nil
		})

		// Fake the serviceinstance delete
		fakeClientSet.ServiceCatalogClientSet.PrependReactor("delete", "serviceinstances", func(action ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, nil
		})

		//fake the dc listing
		fakeClientSet.AppsClientset.PrependReactor("list", "deploymentconfigs", func(action ktesting.Action) (bool, runtime.Object, error) {
			return true, &tt.dcList, nil
		})

		//fake the dc get
		fakeClientSet.AppsClientset.PrependReactor("get", "deploymentconfigs", func(action ktesting.Action) (bool, runtime.Object, error) {
			dcNameToFind := action.(ktesting.GetAction).GetName()
			var matchingDC appsv1.DeploymentConfig
			found := false
			for _, dc := range tt.dcList.Items {
				if dc.Name == dcNameToFind {
					matchingDC = dc
					found = true
					break
				}
			}

			if !found {
				t.Errorf("Expected to find DeploymentConfig named %s in the dcList", dcNameToFind)
			}

			return true, &matchingDC, nil
		})

		err := DeleteServiceAndUnlinkComponents(client, tt.args.ServiceName, "app")

		if !tt.wantErr == (err != nil) {
			t.Errorf("service.DeleteServiceAndUnlinkComponents(...) unexpected error %v, wantErr %v", err, tt.wantErr)
		}

		// ensure we deleted the service
		if len(fakeClientSet.ServiceCatalogClientSet.Actions()) != 3 && !tt.wantErr {
			t.Errorf("service was deleted properly, got actions: %v", fakeClientSet.ServiceCatalogClientSet.Actions())
		}

		// ensure we updated the correct number of deployments
		// there should always be a list action
		// then each update to a dc is 2 actions, a get and an update
		expectedNumberOfDCActions := 1 + (2 * len(tt.expectedDCNamesToBeUpdated))
		if len(fakeClientSet.AppsClientset.Actions()) != 3 && !tt.wantErr {
			t.Errorf("expected to see %d actions, got: %v", expectedNumberOfDCActions, fakeClientSet.AppsClientset.Actions())
		}
	}
}

func TestAddKubernetesComponentToDevfile(t *testing.T) {
	fs := devfileFileSystem.NewFakeFs()

	type args struct {
		crd        string
		name       string
		devfileObj parser.DevfileObj
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
		want    []v1alpha2.Component
	}{
		{
			name: "Case 1: Add service CRD to devfile.yaml",
			args: args{
				crd:  "test CRD",
				name: "testName",
				devfileObj: parser.DevfileObj{
					Data: func() data.DevfileData {
						devfileData, err := data.NewDevfileData(string(data.APISchemaVersion200))
						if err != nil {
							t.Error(err)
						}
						return devfileData
					}(),
					Ctx: devfileCtx.FakeContext(fs, parser.OutputDevfileYamlPath),
				},
			},
			wantErr: false,
			want: []v1alpha2.Component{{
				Name: "testName",
				ComponentUnion: devfile.ComponentUnion{
					Kubernetes: &devfile.KubernetesComponent{
						K8sLikeComponent: devfile.K8sLikeComponent{
							BaseComponent: devfile.BaseComponent{},
							K8sLikeComponentLocation: devfile.K8sLikeComponentLocation{
								Inlined: "test CRD",
							},
						},
					},
				},
			},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := AddKubernetesComponentToDevfile(tt.args.crd, tt.args.name, tt.args.devfileObj); (err != nil) != tt.wantErr {
				t.Errorf("AddKubernetesComponentToDevfile() error = %v, wantErr %v", err, tt.wantErr)
			}
			got, err := tt.args.devfileObj.Data.GetComponents(common.DevfileOptions{})
			if err != nil {
				t.Error(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetComponents() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeleteKubernetesComponentFromDevfile(t *testing.T) {
	fs := devfileFileSystem.NewFakeFs()

	type args struct {
		name       string
		devfileObj parser.DevfileObj
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
		want    []v1alpha2.Component
	}{
		{
			name: "Case 1: Remove a CRD from devfile.yaml",
			args: args{
				name: "testName",
				devfileObj: parser.DevfileObj{
					Data: func() data.DevfileData {
						devfileData, err := data.NewDevfileData(string(data.APISchemaVersion200))
						if err != nil {
							t.Error(err)
						}
						err = devfileData.AddComponents([]v1alpha2.Component{{
							Name: "testName",
							ComponentUnion: devfile.ComponentUnion{
								Kubernetes: &devfile.KubernetesComponent{
									K8sLikeComponent: devfile.K8sLikeComponent{
										BaseComponent: devfile.BaseComponent{},
										K8sLikeComponentLocation: devfile.K8sLikeComponentLocation{
											Inlined: "test CRD",
										},
									},
								},
							},
						},
						})
						if err != nil {
							t.Error(err)
						}
						return devfileData
					}(),
					Ctx: devfileCtx.FakeContext(fs, parser.OutputDevfileYamlPath),
				},
			},
			wantErr: false,
			want:    []v1alpha2.Component{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := DeleteKubernetesComponentFromDevfile(tt.args.name, tt.args.devfileObj); (err != nil) != tt.wantErr {
				t.Errorf("DeleteKubernetesComponentFromDevfile() error = %v, wantErr %v", err, tt.wantErr)
			}
			got, err := tt.args.devfileObj.Data.GetComponents(common.DevfileOptions{})
			if err != nil {
				t.Error(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetComponents() = %v, want %v", got, tt.want)
			}
		})
	}
}
