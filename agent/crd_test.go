package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

// TestCRDsAreAccepted: every CRD the agent installs passes the validation the
// API server runs on create, CEL rules (x-kubernetes-validations) compiled
// included. A rule that does not compile is otherwise only found when a
// customer installs the agent (2a found one live).
func TestCRDsAreAccepted(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := apiextensions.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	decode := serializer.NewCodecFactory(scheme).UniversalDeserializer().Decode
	files, err := filepath.Glob("install/crd/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no CRDs found: %v", err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			obj, _, err := decode(raw, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			v1, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
			if !ok {
				t.Fatalf("%T is not a v1 CustomResourceDefinition", obj)
			}
			var crd apiextensions.CustomResourceDefinition
			if err := scheme.Convert(v1, &crd, nil); err != nil {
				t.Fatal(err)
			}
			// The API server records the storage version on create.
			for _, v := range crd.Spec.Versions {
				if v.Storage {
					crd.Status.StoredVersions = append(crd.Status.StoredVersions, v.Name)
				}
			}
			for _, e := range validation.ValidateCustomResourceDefinition(context.Background(), &crd) {
				t.Error(e)
			}
		})
	}
}
