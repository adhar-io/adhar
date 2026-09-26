package provider

import "testing"

// A remote shell's warnings must never reach a kubeconfig.
//
// `sudo` on a host whose own name does not resolve prints
//
//	sudo: unable to resolve host dev-master-0: Name or service not known
//
// to stderr, and that line landed ABOVE "apiVersion: v1" in the fetched
// kubeconfig. The platform bootstrap then failed with "yaml: mapping values are
// not allowed in this context" — an error naming YAML, with nothing pointing at
// the shell noise that caused it — after the cluster had been built successfully
// (Azure, 2026-09-26).
func TestTrimToKubeconfigDropsShellNoise(t *testing.T) {
	const body = "apiVersion: v1\nclusters:\n- cluster:\n    server: https://10.0.0.1:6443\n"

	cases := map[string]string{
		"clean":                 body,
		"sudo hostname warning": "sudo: unable to resolve host dev-master-0: Name or service not known\n" + body,
		"several noisy lines":   "warning one\nwarning two\n" + body,
		"leading blank lines":   "\n\n" + body,
	}
	for name, in := range cases {
		if got := trimToKubeconfig(in); got != body {
			t.Errorf("%s: trimToKubeconfig did not recover the kubeconfig\n got: %q", name, got)
		}
	}
}

// Nothing resembling a kubeconfig means nothing to trim: returning "" would turn a
// real failure into a confusing empty-file one.
func TestTrimToKubeconfigLeavesUnrecognisedOutputAlone(t *testing.T) {
	const junk = "permission denied\n"
	if got := trimToKubeconfig(junk); got != junk {
		t.Errorf("expected the input unchanged, got %q", got)
	}
}
