/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package up

import (
	"context"
	"fmt"

	"adhar-io/adhar/cmd/ai"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/logger"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ensureAILLMSeedSecret stages the LLM credential for the OpenBao bootstrap Job.
//
// WHY THIS IS A SECRET AND NOT A DIRECT WRITE. OpenBao is a stack package; at this
// point in the bring-up it does not exist yet, so there is nowhere to write the value
// to. The staged Secret is picked up by the OpenBao bootstrap Job
// (security/openbao/manifests/bootstrap.yaml), which re-runs on every sync and
// imports it into secret/adhar-ai/llm — after which the adhar-ai-llm ExternalSecret
// projects it and agentgateway is keyed. The end result is that
//
//	ADHAR_AI_LLM_API_KEY=… adhar up -f config.yaml --env production
//
// produces a fully AI-enabled platform with no follow-up command, which is what the
// operator expected the first time.
//
// The value never reaches Git: it goes from the operator's environment into one
// Secret in one cluster. It is not logged, and the Secret carries a label marking it
// as platform-managed so it is visible to whoever audits the namespace.
func ensureAILLMSeedSecret(ctx context.Context, kubeClient client.Client) error {
	seed, err := ai.LLMSeedFromEnvironment()
	if err != nil {
		return err
	}
	if seed == nil {
		// Nothing supplied. The AI stack installs unkeyed and the platform is
		// unaffected, so this is a normal outcome and not worth a warning.
		return nil
	}

	data := map[string][]byte{}
	for k, v := range seed.Fields() {
		data[k] = []byte(v)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ai.SeedSecretName,
			Namespace: globals.AdharSystemNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "adhar-cli",
				"adhar.io/component":           "ai",
			},
			Annotations: map[string]string{
				"adhar.io/purpose": "staged LLM credential; imported into OpenBao by the openbao-bootstrap Job",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}

	existing := &corev1.Secret{}
	err = kubeClient.Get(ctx, client.ObjectKeyFromObject(secret), existing)
	switch {
	case err == nil:
		existing.Data = secret.Data
		existing.Labels = secret.ObjectMeta.Labels
		existing.Annotations = secret.ObjectMeta.Annotations
		if uerr := kubeClient.Update(ctx, existing); uerr != nil {
			return fmt.Errorf("updating %s: %w", ai.SeedSecretName, uerr)
		}
	default:
		if cerr := kubeClient.Create(ctx, secret); cerr != nil {
			return fmt.Errorf("creating %s: %w", ai.SeedSecretName, cerr)
		}
	}

	logger.Infof("Adhar AI credential staged: %s (imported into OpenBao once it initialises)", seed.Describe())
	return nil
}
