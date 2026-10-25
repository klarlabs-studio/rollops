package kubernetes

import (
	"strings"
	"testing"
)

func TestSplitMiddlewareAnnotation(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{"security-headers@file", nil},
		{"security-headers@kubernetescrd", []string{"security-headers"}},
		{"klarlabs-security-headers@kubernetescrd", []string{"klarlabs-security-headers"}},
		{
			"klarlabs-security-headers@kubernetescrd, klarlabs-rate-limit@kubernetescrd",
			[]string{"klarlabs-security-headers", "klarlabs-rate-limit"},
		},
	}
	for _, tc := range cases {
		got := splitMiddlewareAnnotation(tc.raw)
		if len(got) != len(tc.want) {
			t.Fatalf("split(%q): got %#v, want %#v", tc.raw, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("split(%q)[%d] = %q, want %q", tc.raw, i, got[i], tc.want[i])
			}
		}
	}
}

func TestDanglingMiddlewareWarnings_InBatchIsQuiet(t *testing.T) {
	ingress := []byte(`
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: site
  namespace: klarlabs
  annotations:
    traefik.ingress.kubernetes.io/router.middlewares: klarlabs-security-headers@kubernetescrd
`)
	mw := []byte(`
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: security-headers
  namespace: klarlabs
`)
	warns := DanglingMiddlewareWarnings([][]byte{ingress, mw}, nil)
	if len(warns) != 0 {
		t.Fatalf("in-batch Middleware must not warn, got %v", warns)
	}
}

func TestDanglingMiddlewareWarnings_SameNSShortFormInBatch(t *testing.T) {
	ingress := []byte(`
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: site
  namespace: klarlabs
  annotations:
    traefik.ingress.kubernetes.io/router.middlewares: security-headers@kubernetescrd
`)
	mw := []byte(`
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: security-headers
  namespace: klarlabs
`)
	warns := DanglingMiddlewareWarnings([][]byte{ingress, mw}, nil)
	if len(warns) != 0 {
		t.Fatalf("same-ns short form in batch must not warn, got %v", warns)
	}
}

func TestDanglingMiddlewareWarnings_MissingWarns(t *testing.T) {
	ingress := []byte(`
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: site
  namespace: klarlabs
  annotations:
    traefik.ingress.kubernetes.io/router.middlewares: klarlabs-security-headers@kubernetescrd
`)
	warns := DanglingMiddlewareWarnings([][]byte{ingress}, func(ns, name string) (bool, error) {
		return false, nil
	})
	if len(warns) != 1 {
		t.Fatalf("warns = %v, want 1", warns)
	}
	if !strings.Contains(warns[0], "security-headers") || !strings.Contains(warns[0], "neither in this batch") {
		t.Errorf("warning = %q", warns[0])
	}
}

func TestDanglingMiddlewareWarnings_LiveInClusterIsQuiet(t *testing.T) {
	ingress := []byte(`
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: site
  namespace: klarlabs
  annotations:
    traefik.ingress.kubernetes.io/router.middlewares: klarlabs-security-headers@kubernetescrd
`)
	warns := DanglingMiddlewareWarnings([][]byte{ingress}, func(ns, name string) (bool, error) {
		return ns == "klarlabs" && name == "security-headers", nil
	})
	if len(warns) != 0 {
		t.Fatalf("live Middleware must not warn, got %v", warns)
	}
}

func TestDanglingMiddlewareWarnings_HyphenatedNameResolves(t *testing.T) {
	// Namespace and name both contain hyphens: naively splitting on the last
	// '-' would mis-parse. Matching ns+"-"+name against the token must work.
	ingress := []byte(`
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: site
  namespace: klar-labs
  annotations:
    traefik.ingress.kubernetes.io/router.middlewares: klar-labs-security-headers@kubernetescrd
`)
	mw := []byte(`
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: security-headers
  namespace: klar-labs
`)
	warns := DanglingMiddlewareWarnings([][]byte{ingress, mw}, nil)
	if len(warns) != 0 {
		t.Fatalf("hyphenated ns+name in batch must not warn, got %v", warns)
	}
}

func TestDanglingMiddlewareWarnings_NoAnnotationQuiet(t *testing.T) {
	ingress := []byte(`
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: site
  namespace: klarlabs
`)
	warns := DanglingMiddlewareWarnings([][]byte{ingress}, nil)
	if len(warns) != 0 {
		t.Fatalf("got %v", warns)
	}
}
