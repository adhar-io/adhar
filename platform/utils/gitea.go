package utils

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/utils/idp"

	"code.gitea.io/sdk/gitea"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// hardcoded values from what we have in the yaml installation file.
	// Gitea is installed into adhar-system by the bootstrap (not a dedicated
	// "gitea" namespace); the admin credential Secret lives there too.
	GiteaNamespace           = "adhar-system"
	GiteaAdminSecret         = "gitea-credential"
	GiteaAdminName           = globals.GiteaAdminUser
	GiteaAdminTokenName      = "admin"
	GiteaAdminTokenFieldName = "token"
	GiteaURLTempl            = "%s://%s%s:%s%s"
)

func GiteaAdminSecretObject() corev1.Secret {
	return corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      GiteaAdminSecret,
			Namespace: GiteaNamespace,
		},
	}
}

func PatchPasswordSecret(ctx context.Context, kubeClient client.Client, config v1alpha1.BuildCustomizationSpec, ns string, secretName string, username string, pass string) error {
	sec, err := GetSecretByName(ctx, kubeClient, ns, secretName)
	if err != nil {
		return fmt.Errorf("getting secret to patch fails: %w", err)
	}
	u := unstructured.Unstructured{}
	u.SetName(sec.GetName())
	u.SetNamespace(sec.GetNamespace())
	// Typed client reads strip TypeMeta, so the GVK must be set explicitly for
	// the server-side apply patch to be valid.
	u.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))

	err = unstructured.SetNestedField(u.Object, base64.StdEncoding.EncodeToString([]byte(pass)), "data", "password")
	if err != nil {
		return fmt.Errorf("setting password field: %w", err)
	}

	if strings.Contains(secretName, "gitea") {
		// We should recreate a token as user/password changed
		giteaUrl, err := GiteaBaseUrl(ctx)
		if err != nil {
			return fmt.Errorf("getting giteaurl: %w", err)
		}

		t, err := GetGiteaToken(ctx, giteaUrl, string(username), string(pass))
		if err != nil {
			return fmt.Errorf("getting gitea token: %w", err)
		}

		token := base64.StdEncoding.EncodeToString([]byte(t))
		err = unstructured.SetNestedField(u.Object, token, "data", GiteaAdminTokenFieldName)
		if err != nil {
			return fmt.Errorf("setting gitea token field: %w", err)
		}
	}

	return kubeClient.Patch(ctx, &u, client.Apply, client.ForceOwnership, client.FieldOwner(v1alpha1.FieldManager))
}

func GetGiteaToken(ctx context.Context, baseUrl, username, password string) (string, error) {
	giteaClient, err := gitea.NewClient(baseUrl, gitea.SetHTTPClient(GetHttpClient()),
		gitea.SetBasicAuth(username, password), gitea.SetContext(ctx),
	)
	if err != nil {
		return "", fmt.Errorf("creating gitea client: %w", err)
	}
	tokens, resp, err := giteaClient.ListAccessTokens(gitea.ListAccessTokensOptions{})
	if err != nil {
		return "", fmt.Errorf("listing gitea access tokens%s: %w", responseStatus(resp), err)
	}

	for i := range tokens {
		if tokens[i].Name == GiteaAdminTokenName {
			resp, err := giteaClient.DeleteAccessToken(tokens[i].ID)
			if err != nil {
				return "", fmt.Errorf("deleting gitea access token %q%s: %w", GiteaAdminTokenName, responseStatus(resp), err)
			}
			break
		}
	}

	token, resp, err := giteaClient.CreateAccessToken(gitea.CreateAccessTokenOption{
		Name: GiteaAdminTokenName,
		Scopes: []gitea.AccessTokenScope{
			gitea.AccessTokenScopeAll,
		},
	})
	if err != nil {
		return "", fmt.Errorf("creating gitea access token %q%s: %w", GiteaAdminTokenName, responseStatus(resp), err)
	}

	return token.Token, nil
}

// responseStatus renders a Gitea SDK response for an error message.
//
// It exists because the SDK returns a NIL *Response together with a non-nil
// error whenever the request never reached the server -- an unreachable host, a
// context cancellation, a malformed URL. Reading resp.Status on that path
// panicked the reconciler with a nil dereference, and it is the EXPECTED path
// during `adhar up`, where Gitea is polled before it is serving. The panic
// replaced the real message ("connection refused") with a stack trace.
func responseStatus(resp *gitea.Response) string {
	if resp == nil || resp.Response == nil {
		return ""
	}
	return " (status: " + resp.Status + ")"
}

func GiteaBaseUrl(ctx context.Context) (string, error) {
	idpConfig, err := idp.GetConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("fetching idp config: %w", err)
	}
	return GiteaBaseUrlFromConfig(idpConfig), nil
}

// GiteaBaseUrlFromConfig is the pure half of GiteaBaseUrl: the URL a platform
// spec implies, with no cluster access.
//
// It is separate so the routing rule can be tested at all. GiteaBaseUrl reaches
// through idp.GetConfig to a live API server, so the only "test" it ever had
// re-implemented this formatting inline and asserted on its own output -- it
// could not fail, and it would not have noticed either branch changing.
func GiteaBaseUrlFromConfig(cfg v1alpha1.BuildCustomizationSpec) string {
	// Path routing puts every service on one hostname under a prefix; otherwise
	// each gets its own subdomain.
	if cfg.UsePathRouting {
		return fmt.Sprintf(GiteaURLTempl, cfg.Protocol, "", cfg.Host, cfg.Port, "/gitea")
	}
	return fmt.Sprintf(GiteaURLTempl, cfg.Protocol, "gitea.", cfg.Host, cfg.Port, "")
}
