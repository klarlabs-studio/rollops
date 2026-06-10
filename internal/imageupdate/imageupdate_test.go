package imageupdate

import (
	"strings"
	"testing"
)

func TestPolicyValidate(t *testing.T) {
	p := Policy{AllowedRegistries: []string{"ghcr.io"}, TagPattern: `^v\d+\.\d+\.\d+$`}
	tests := []struct {
		name string
		in   Update
		want string
	}{
		{name: "allowed", in: Update{Image: "ghcr.io/klarlabs/api", Tag: "v1.2.3"}},
		{name: "registry", in: Update{Image: "docker.io/library/nginx", Tag: "v1.2.3"}, want: "registry"},
		{name: "mutable", in: Update{Image: "ghcr.io/klarlabs/api", Tag: "latest"}, want: "mutable"},
		{name: "pattern", in: Update{Image: "ghcr.io/klarlabs/api", Tag: "dev"}, want: "match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.Validate(tt.in)
			if tt.want == "" && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestPatchRolloutImage(t *testing.T) {
	in := []byte(`apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: ssh
    ref: demo/prod/api
    criticality: low
    spec:
      image: ghcr.io/klarlabs/api:v1.0.0
  strategy:
    type: rolling
`)
	out, changed, err := PatchRolloutImage(in, "ghcr.io/klarlabs/api", "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected change")
	}
	if !strings.Contains(string(out), "ghcr.io/klarlabs/api:v1.2.3") {
		t.Fatalf("patched yaml = %s", out)
	}
}
