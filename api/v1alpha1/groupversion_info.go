// +kubebuilder:object:generate=true
// +groupName=platform.adhar.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{Group: "platform.adhar.io", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func init() {
	SchemeBuilder.Register(&Localbuild{}, &LocalbuildList{})
	SchemeBuilder.Register(&GitRepository{}, &GitRepositoryList{})
	SchemeBuilder.Register(&CustomPackage{}, &CustomPackageList{})
}
