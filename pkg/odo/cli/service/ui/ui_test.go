package ui

import (
	"reflect"
	"testing"

	beta1 "github.com/kubernetes-sigs/service-catalog/pkg/apis/servicecatalog/v1beta1"
	"github.com/openshift/odo/pkg/testingutil"
	"gopkg.in/AlecAivazis/survey.v1/core"
)

func init() {
	// disable color output for all prompts to simplify testing
	core.DisableColor = true
}

func TestGetCategories(t *testing.T) {
	t.Run("getServiceClassesCategories should work", func(t *testing.T) {
		foo := testingutil.FakeClusterServiceClass("foo", "footag", "footag2")
		bar := testingutil.FakeClusterServiceClass("bar", "")
		boo := testingutil.FakeClusterServiceClass("boo")
		classes := map[string][]beta1.ClusterServiceClass{"footag": {foo}, "other": {bar, boo}}

		categories := getServiceClassesCategories(classes)
		expected := []string{"footag", "other"}
		if !reflect.DeepEqual(expected, categories) {
			t.Errorf("test failed, expected %v, got %v", expected, categories)
		}
	})
}

func TestGetServicePlanNames(t *testing.T) {
	t.Run("GetServicePlanNames should work", func(t *testing.T) {
		foo := testingutil.FakeClusterServicePlan("foo", 1)
		bar := testingutil.FakeClusterServicePlan("bar", 2)
		boo := testingutil.FakeClusterServicePlan("boo", 3)

		plans := GetServicePlanNames(map[string]beta1.ClusterServicePlan{"foo": foo, "bar": bar, "boo": boo})
		expected := []string{"bar", "boo", "foo"}
		if !reflect.DeepEqual(expected, plans) {
			t.Errorf("test failed, expected %v, got %v", expected, plans)
		}
	})
}

func TestWrapIfNeeded(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		prefixSize int
		expected   string
	}{
		{
			name:       "empty string, empty prefix",
			input:      "",
			prefixSize: 0,
			expected:   "",
		},
		{
			name:       "short string, empty prefix should not be wrapped",
			input:      "foo bar baz",
			prefixSize: 0,
			expected:   "foo bar baz",
		},
		{
			name:       "short string, empty prefix should not be wrapped with default width",
			input:      "foo bar baz",
			prefixSize: 2,
			expected:   "foo bar baz",
		},
		{
			name:       "short string, long prefix should wrap",
			input:      "foo bar baz",
			prefixSize: 78,
			expected:   "foo\nbar\nbaz",
		},
		{
			name:       "long string, empty prefix should wrap",
			input:      "0123456789 0123456789 0123456789 0123456789 0123456789 0123456789 0123456789 0123456789",
			prefixSize: 0,
			expected:   "0123456789 0123456789 0123456789 0123456789 0123456789 0123456789 0123456789\n0123456789",
		},
		{
			name:       "long string, short prefix should wrap",
			input:      "0123456789 0123456789 0123456789 0123456789 0123456789 0123456789 0123456789 0123456789",
			prefixSize: 2,
			expected:   "0123456789 0123456789 0123456789 0123456789 0123456789 0123456789 0123456789\n0123456789",
		},
		{
			name:       "long string, longer prefix should wrap more",
			input:      "0123456789 0123456789 0123456789 0123456789 0123456789 0123456789 0123456789 0123456789",
			prefixSize: 10,
			expected:   "0123456789 0123456789 0123456789 0123456789 0123456789 0123456789\n0123456789 0123456789",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := wrapIfNeeded(tt.input, tt.prefixSize)
			if tt.expected != output {
				t.Errorf("test failed, expected %s, got %s", tt.expected, output)
			}
		})
	}
}

func init() {
	// disable color output for all prompts to simplify testing
	core.DisableColor = true
}

func TestGetLongDescription(t *testing.T) {
	desc := testingutil.FakeClusterServiceClass("foo")
	desc.Spec.ExternalMetadata = testingutil.SingleValuedRawExtension("longDescription", "description")
	empty := testingutil.FakeClusterServiceClass("foo")
	empty.Spec.ExternalMetadata = testingutil.SingleValuedRawExtension("longDescription", "")
	tests := []struct {
		name     string
		input    beta1.ClusterServiceClass
		expected string
	}{
		{
			name:     "no metadata",
			input:    testingutil.FakeClusterServiceClass("foo"),
			expected: "",
		},
		{
			name:     "description",
			input:    desc,
			expected: "description",
		},
		{
			name:     "empty description",
			input:    empty,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := getLongDescription(tt.input)
			if tt.expected != output {
				t.Errorf("test failed, expected %s, got %s", tt.expected, output)
			}
		})
	}
}
