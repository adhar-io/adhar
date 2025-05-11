package idp

import (
	"context"

	"adhar-io/adhar/api/v1alpha1"

	"adhar-io/adhar/platform/k8s"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func GetConfig(ctx context.Context) (v1alpha1.BuildCustomizationSpec, error) {
	b := v1alpha1.BuildCustomizationSpec{}

	kubeClient, err := k8s.GetKubeClient()
	if err != nil {
		return b, err
	}

	list, err := getAdharPlatform(ctx, kubeClient)
	if err != nil {
		return b, err
	}

	// TODO: We assume that only one AdharPlatform exists !
	return list.Items[0].Spec.BuildCustomization, nil
}

func getAdharPlatform(ctx context.Context, kubeClient client.Client) (v1alpha1.AdharPlatformList, error) {
	adharPlatformList := v1alpha1.AdharPlatformList{}
	return adharPlatformList, kubeClient.List(ctx, &adharPlatformList)
}
