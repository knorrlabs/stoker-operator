package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	stokertypes "github.com/knorrlabs/stoker-operator/pkg/types"
)

const (
	mappingTypeDir  = "dir"
	mappingTypeFile = "file"
)

func TestResolveTemplate_AllFields(t *testing.T) {
	ctx := &TemplateContext{
		GatewayName: "gw-blue",
		Namespace:   "prod",
		Ref:         "refs/heads/main",
		Commit:      "abc123",
		CRName:      "my-stoker",
		Labels:      map[string]string{"site": "us-east-1", "tier": "edge"},
		Vars:        map[string]string{"env": "production", "region": "us-east"},
	}

	tests := []struct {
		tmpl string
		want string
	}{
		{"{{.GatewayName}}", "gw-blue"},
		{"{{.Namespace}}", "prod"},
		{"{{.Ref}}", "refs/heads/main"},
		{"{{.Commit}}", "abc123"},
		{"{{.CRName}}", "my-stoker"},
		{"{{.Labels.site}}", "us-east-1"},
		{"sites/{{.Labels.tier}}/config", "sites/edge/config"},
		{"{{.Vars.env}}", "production"},
		{"config/{{.Vars.region}}/overlay", "config/us-east/overlay"},
		{"no-template", "no-template"},
	}

	for _, tt := range tests {
		got, err := resolveTemplate(tt.tmpl, ctx)
		if err != nil {
			t.Errorf("resolveTemplate(%q): %v", tt.tmpl, err)
			continue
		}
		if got != tt.want {
			t.Errorf("resolveTemplate(%q) = %q, want %q", tt.tmpl, got, tt.want)
		}
	}
}

func TestResolveTemplate_MissingKey(t *testing.T) {
	ctx := &TemplateContext{
		GatewayName: "gw",
		Labels:      map[string]string{},
		Vars:        map[string]string{},
	}

	tests := []string{
		"{{.Vars.missing}}",
		"{{.Labels.missing}}",
	}
	for _, tmpl := range tests {
		_, err := resolveTemplate(tmpl, ctx)
		if err == nil {
			t.Errorf("expected error for missing key in %q", tmpl)
		}
	}
}

func TestValidateResolvedPath_Traversal(t *testing.T) {
	tests := []struct {
		path    string
		wantErr bool
	}{
		{"config/resources", false},
		{"projects/myproject", false},
		{"../escape", true},
		{"config/../../etc", true},
		{"/absolute/path", true},
		{".", false},
		{"config", false},
	}

	for _, tt := range tests {
		err := validateResolvedPath(tt.path, "test")
		if (err != nil) != tt.wantErr {
			t.Errorf("validateResolvedPath(%q): err=%v, wantErr=%v", tt.path, err, tt.wantErr)
		}
	}
}

func TestBuildSyncPlan_Basic(t *testing.T) {
	tmp := t.TempDir()
	repoPath := filepath.Join(tmp, "repo")
	liveDir := filepath.Join(tmp, "live")

	// Create source dirs.
	writeFile(t, filepath.Join(repoPath, "shared", "config.json"), "shared")
	writeFile(t, filepath.Join(repoPath, "site", "us-east", "override.json"), "override")

	profile := &stokertypes.ResolvedProfile{
		Mappings: []stokertypes.ResolvedMapping{
			{Source: "shared", Destination: "config/resources/core", Type: mappingTypeDir},
			{Source: "site/{{.Vars.region}}", Destination: "config/resources/core", Type: mappingTypeDir},
		},
		Vars: map[string]string{"region": "us-east"},
	}

	ctx := &TemplateContext{
		GatewayName: "gw-1",
		Namespace:   "default",
		Vars:        map[string]string{"region": "us-east"},
	}

	plan, err := buildSyncPlan(profile, ctx, repoPath, liveDir)
	if err != nil {
		t.Fatalf("buildSyncPlan: %v", err)
	}

	if len(plan.Mappings) != 2 {
		t.Fatalf("expected 2 mappings, got %d", len(plan.Mappings))
	}

	if plan.Mappings[0].Destination != "config/resources/core" {
		t.Errorf("mapping[0].Destination = %q, want config/resources/core", plan.Mappings[0].Destination)
	}
	if plan.Mappings[1].Source != filepath.Join(repoPath, "site", "us-east") {
		t.Errorf("mapping[1].Source = %q, want %s", plan.Mappings[1].Source, filepath.Join(repoPath, "site", "us-east"))
	}
}

func TestBuildSyncPlan_RequiredMissing(t *testing.T) {
	tmp := t.TempDir()
	repoPath := filepath.Join(tmp, "repo")
	liveDir := filepath.Join(tmp, "live")

	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatal(err)
	}

	profile := &stokertypes.ResolvedProfile{
		Mappings: []stokertypes.ResolvedMapping{
			{Source: "nonexistent", Destination: "config", Type: mappingTypeDir, Required: true},
		},
	}

	ctx := &TemplateContext{GatewayName: "gw", Namespace: "default", Vars: map[string]string{}}

	_, err := buildSyncPlan(profile, ctx, repoPath, liveDir)
	if err == nil {
		t.Error("expected error for required missing source")
	}
}

func TestBuildSyncPlan_ExcludesFromProfile(t *testing.T) {
	tmp := t.TempDir()
	repoPath := filepath.Join(tmp, "repo")
	liveDir := filepath.Join(tmp, "live")

	writeFile(t, filepath.Join(repoPath, "src", "a.txt"), "a")

	profile := &stokertypes.ResolvedProfile{
		Mappings: []stokertypes.ResolvedMapping{
			{Source: "src", Destination: "dst", Type: mappingTypeDir},
		},
		ExcludePatterns: []string{"**/*.bak", "**/*.tmp", "**/*.log"},
	}

	ctx := &TemplateContext{GatewayName: "gw", Namespace: "default", Vars: map[string]string{}}

	plan, err := buildSyncPlan(profile, ctx, repoPath, liveDir)
	if err != nil {
		t.Fatalf("buildSyncPlan: %v", err)
	}

	// Excludes come directly from the resolved profile (controller already merged defaults).
	if len(plan.ExcludePatterns) != 3 {
		t.Errorf("expected 3 exclude patterns, got %d: %v", len(plan.ExcludePatterns), plan.ExcludePatterns)
	}
}

func TestBuildTemplateContext(t *testing.T) {
	cfg := &Config{
		GatewayName: "gw-test",
		PodName:     "ignition-0",
		CRName:      "my-cr",
		CRNamespace: "my-ns",
	}
	meta := &Metadata{
		Ref:    "refs/heads/main",
		Commit: "deadbeef",
	}
	vars := map[string]string{"site": "us-east-1"}
	labels := map[string]string{"app": "ignition", "tier": "edge"}

	ctx := buildTemplateContext(cfg, meta, vars, labels)

	if ctx.GatewayName != "gw-test" {
		t.Errorf("GatewayName = %q", ctx.GatewayName)
	}
	if ctx.PodName != "ignition-0" {
		t.Errorf("PodName = %q, want ignition-0", ctx.PodName)
	}
	if ctx.Namespace != "my-ns" {
		t.Errorf("Namespace = %q", ctx.Namespace)
	}
	if ctx.Ref != "refs/heads/main" {
		t.Errorf("Ref = %q", ctx.Ref)
	}
	if ctx.Commit != "deadbeef" {
		t.Errorf("Commit = %q", ctx.Commit)
	}
	if ctx.CRName != "my-cr" {
		t.Errorf("CRName = %q", ctx.CRName)
	}
	if ctx.Labels["app"] != "ignition" {
		t.Errorf("Labels[app] = %q", ctx.Labels["app"])
	}
	if ctx.Labels["tier"] != "edge" {
		t.Errorf("Labels[tier] = %q", ctx.Labels["tier"])
	}
	if ctx.Vars["site"] != "us-east-1" {
		t.Errorf("Vars[site] = %q", ctx.Vars["site"])
	}
}

func TestPodOrdinal(t *testing.T) {
	cases := []struct {
		name    string
		podName string
		labels  map[string]string
		wantOrd int
	}{
		{
			name:    "k8s label takes priority",
			podName: "pod-99",
			labels:  map[string]string{"apps.kubernetes.io/pod-index": "3"},
			wantOrd: 3,
		},
		{
			name:    "fallback to pod name ordinal",
			podName: "my-gateway-2",
			labels:  map[string]string{},
			wantOrd: 2,
		},
		{
			name:    "non-statefulset pod returns 0",
			podName: "my-deployment-abc12-xyz99",
			labels:  map[string]string{},
			wantOrd: 0,
		},
		{
			name:    "invalid label value falls back to pod name",
			podName: "my-gateway-5",
			labels:  map[string]string{"apps.kubernetes.io/pod-index": "not-a-number"},
			wantOrd: 5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := podOrdinal(tc.podName, tc.labels)
			if got != tc.wantOrd {
				t.Errorf("podOrdinal(%q, %v) = %d, want %d", tc.podName, tc.labels, got, tc.wantOrd)
			}
		})
	}
}

func TestResolveTemplate_PodOrdinal(t *testing.T) {
	ctx := &TemplateContext{
		PodName:    "public-demo-fe-gateway-0",
		PodOrdinal: 0,
		Vars:       map[string]string{"projectName": "public-demo"},
	}
	got, err := resolveTemplate("{{.Vars.projectName}}-{{.PodOrdinal}}", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "public-demo-0" {
		t.Errorf("got %q, want public-demo-0", got)
	}
}

func TestResolveTemplate_PodName(t *testing.T) {
	ctx := &TemplateContext{
		GatewayName: "ignition",
		PodName:     "ignition-2",
		Vars:        map[string]string{},
	}
	got, err := resolveTemplate("system-{{.PodName}}", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "system-ignition-2" {
		t.Errorf("got %q, want system-ignition-2", got)
	}
}

func TestApplyTemplateFunc_TextFile(t *testing.T) {
	ctx := &TemplateContext{
		GatewayName: "gw-site1",
		PodName:     "ignition-0",
		Namespace:   "prod",
		Vars:        map[string]string{"deploymentMode": "production"},
	}
	fn := buildApplyTemplateFunc(ctx)

	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.json")
	writeFile(t, path, `{"systemName": "{{.GatewayName}}", "mode": "{{.Vars.deploymentMode}}"}`)

	if err := fn(path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"systemName": "gw-site1", "mode": "production"}`
	if string(content) != want {
		t.Errorf("got %q, want %q", string(content), want)
	}
}

func TestApplyTemplateFunc_BinaryFileRejected(t *testing.T) {
	ctx := &TemplateContext{GatewayName: "gw", Vars: map[string]string{}}
	fn := buildApplyTemplateFunc(ctx)

	tmp := t.TempDir()
	path := filepath.Join(tmp, "binary.bin")
	// Write a file with a null byte — should be rejected.
	if err := os.WriteFile(path, []byte("hello\x00world"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := fn(path); err == nil {
		t.Error("expected error for binary file, got nil")
	}
}

func TestApplyTemplateFunc_NoTemplateSkipped(t *testing.T) {
	ctx := &TemplateContext{GatewayName: "gw", Vars: map[string]string{}}
	fn := buildApplyTemplateFunc(ctx)

	tmp := t.TempDir()
	path := filepath.Join(tmp, "plain.txt")
	original := "no template syntax here"
	writeFile(t, path, original)

	if err := fn(path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, _ := os.ReadFile(path)
	if string(content) != original {
		t.Errorf("file was modified unexpectedly: %q", string(content))
	}
}

func TestBuildSyncPlan_TemplateFlag(t *testing.T) {
	tmp := t.TempDir()
	repoPath := filepath.Join(tmp, "repo")
	liveDir := filepath.Join(tmp, "live")

	writeFile(t, filepath.Join(repoPath, "config", "system.json"), `{"name":"{{.GatewayName}}"}`)

	profile := &stokertypes.ResolvedProfile{
		Mappings: []stokertypes.ResolvedMapping{
			{Source: "config", Destination: "config/resources", Type: mappingTypeDir, Template: true},
		},
	}
	ctx := &TemplateContext{GatewayName: "gw-site1", Vars: map[string]string{}}

	plan, err := buildSyncPlan(profile, ctx, repoPath, liveDir)
	if err != nil {
		t.Fatalf("buildSyncPlan: %v", err)
	}
	if !plan.Mappings[0].Template {
		t.Error("expected Template=true to propagate to SyncPlan mapping")
	}
	if plan.ApplyTemplate == nil {
		t.Error("expected ApplyTemplate func to be set")
	}
}

// ── applyJSONPatch ────────────────────────────────────────────────────────────

func TestApplyJSONPatch_StringValue(t *testing.T) {
	in := `{"SystemName":"old","enabled":true}`
	out, err := applyJSONPatch(in, "SystemName", "gateway-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Value should be a JSON string, not bare text.
	if out != `{"SystemName":"gateway-1","enabled":true}` {
		t.Errorf("got %s", out)
	}
}

func TestApplyJSONPatch_BoolInference(t *testing.T) {
	in := `{"enabled":false}`
	out, err := applyJSONPatch(in, "enabled", "true")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// "true" should be decoded to boolean true, not string "true".
	if out != `{"enabled":true}` {
		t.Errorf("got %s", out)
	}
}

func TestApplyJSONPatch_NumberInference(t *testing.T) {
	in := `{"port":8088}`
	out, err := applyJSONPatch(in, "port", "9090")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// "9090" should be decoded to number, not string "9090".
	if out != `{"port":9090}` {
		t.Errorf("got %s", out)
	}
}

func TestApplyJSONPatch_NestedPath(t *testing.T) {
	in := `{"networkInterfaces":[{"address":"10.0.0.1"}]}`
	out, err := applyJSONPatch(in, "networkInterfaces.0.address", "192.168.1.100")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != `{"networkInterfaces":[{"address":"192.168.1.100"}]}` {
		t.Errorf("got %s", out)
	}
}

func TestApplyJSONPatch_InvalidJSON(t *testing.T) {
	_, err := applyJSONPatch("not json", "key", "value")
	if err == nil {
		t.Error("expected error for non-JSON content, got nil")
	}
}

// ── buildApplyPatchesFunc ─────────────────────────────────────────────────────

func TestBuildApplyPatchesFunc_NilWhenNoPatches(t *testing.T) {
	fn := buildApplyPatchesFunc(nil, &TemplateContext{}, t.TempDir(), "config", false)
	if fn != nil {
		t.Error("expected nil func for empty patches")
	}
}

func TestBuildApplyPatchesFunc_ExactFileMatch(t *testing.T) {
	tmp := t.TempDir()
	stagingDir := filepath.Join(tmp, "staging")
	mappingDest := "config/resources/core"

	// Write a staged file at the expected path.
	target := filepath.Join(stagingDir, mappingDest, "system-properties", "config.json")
	writeFile(t, target, `{"SystemName":"old-name","port":8088}`)

	ctx := &TemplateContext{GatewayName: "site-gateway", Vars: map[string]string{}}
	patches := []stokertypes.ResolvedPatch{
		{
			File: "system-properties/config.json",
			Set:  map[string]string{"SystemName": "{{.GatewayName}}"},
		},
	}

	fn := buildApplyPatchesFunc(patches, ctx, stagingDir, mappingDest, false)
	if fn == nil {
		t.Fatal("expected non-nil func")
	}
	if err := fn(target); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, _ := os.ReadFile(target)
	if !contains(string(content), `"SystemName":"site-gateway"`) {
		t.Errorf("patch not applied, got: %s", content)
	}
}

func TestBuildApplyPatchesFunc_GlobMatch(t *testing.T) {
	tmp := t.TempDir()
	stagingDir := filepath.Join(tmp, "staging")
	mappingDest := "config/db-connections"

	conn1 := filepath.Join(stagingDir, mappingDest, "conn1.json")
	conn2 := filepath.Join(stagingDir, mappingDest, "conn2.json")
	writeFile(t, conn1, `{"host":"old-host"}`)
	writeFile(t, conn2, `{"host":"old-host"}`)

	ctx := &TemplateContext{Vars: map[string]string{"dbHost": "db.prod.internal"}}
	patches := []stokertypes.ResolvedPatch{
		{File: "*.json", Set: map[string]string{"host": "{{.Vars.dbHost}}"}},
	}

	fn := buildApplyPatchesFunc(patches, ctx, stagingDir, mappingDest, false)
	for _, f := range []string{conn1, conn2} {
		if err := fn(f); err != nil {
			t.Fatalf("fn(%s): %v", f, err)
		}
		content, _ := os.ReadFile(f)
		if !contains(string(content), `"host":"db.prod.internal"`) {
			t.Errorf("patch not applied to %s, got: %s", f, content)
		}
	}
}

func TestBuildApplyPatchesFunc_NoMatchSkipped(t *testing.T) {
	tmp := t.TempDir()
	stagingDir := filepath.Join(tmp, "staging")
	mappingDest := "config/resources"

	notTarget := filepath.Join(stagingDir, mappingDest, "other.json")
	writeFile(t, notTarget, `{"key":"original"}`)

	ctx := &TemplateContext{Vars: map[string]string{}}
	patches := []stokertypes.ResolvedPatch{
		{File: "system-properties/config.json", Set: map[string]string{"key": "changed"}},
	}

	fn := buildApplyPatchesFunc(patches, ctx, stagingDir, mappingDest, false)
	if err := fn(notTarget); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, _ := os.ReadFile(notTarget)
	if !contains(string(content), `"key":"original"`) {
		t.Errorf("file should not have been modified, got: %s", content)
	}
}

func TestBuildApplyPatchesFunc_EmptyFileField_FileMappingCase(t *testing.T) {
	tmp := t.TempDir()
	stagingDir := filepath.Join(tmp, "staging")
	// For a file mapping, mappingDest is the file itself (e.g. config/versions/.versions.json).
	mappingDest := "config/versions/.versions.json"

	target := filepath.Join(stagingDir, mappingDest)
	writeFile(t, target, `{"gatewayVersion":"1.0.0"}`)

	ctx := &TemplateContext{Vars: map[string]string{"gatewayVersion": "2.5.0"}}
	patches := []stokertypes.ResolvedPatch{
		// file omitted — should match the mapped file itself.
		{Set: map[string]string{"gatewayVersion": "{{.Vars.gatewayVersion}}"}},
	}

	fn := buildApplyPatchesFunc(patches, ctx, stagingDir, mappingDest, true)
	if err := fn(target); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, _ := os.ReadFile(target)
	if !contains(string(content), `"gatewayVersion":"2.5.0"`) {
		t.Errorf("patch not applied, got: %s", content)
	}
}

// ── Type inference ────────────────────────────────────────────────────────────

func TestInferMappingType_InfersDir(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "mydir")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	typ, err := inferMappingType(dir, "")
	if err != nil || typ != mappingTypeDir {
		t.Errorf("inferMappingType(dir) = %q, %v; want dir, nil", typ, err)
	}
}

func TestInferMappingType_InfersFile(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "config.json")
	writeFile(t, f, "{}")
	typ, err := inferMappingType(f, "")
	if err != nil || typ != mappingTypeFile {
		t.Errorf("inferMappingType(file) = %q, %v; want file, nil", typ, err)
	}
}

func TestInferMappingType_HintMismatch(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "config.json")
	writeFile(t, f, "{}")
	_, err := inferMappingType(f, mappingTypeDir) // file is actually a file, not dir
	if err == nil {
		t.Error("expected type mismatch error, got nil")
	}
}

func TestInferMappingType_NonexistentDefaultsToDir(t *testing.T) {
	typ, err := inferMappingType("/nonexistent/path", "")
	if err != nil || typ != mappingTypeDir {
		t.Errorf("nonexistent path: got %q, %v; want dir, nil", typ, err)
	}
}

func TestBuildSyncPlan_TypeInferredFromFilesystem(t *testing.T) {
	tmp := t.TempDir()
	repoPath := filepath.Join(tmp, "repo")
	liveDir := filepath.Join(tmp, "live")

	// Create a file (not a dir) at the source path.
	writeFile(t, filepath.Join(repoPath, "config", ".versions.json"), `{"version":"1.0"}`)

	profile := &stokertypes.ResolvedProfile{
		Mappings: []stokertypes.ResolvedMapping{
			// Type omitted — should be inferred as "file".
			{Source: "config/.versions.json", Destination: "config/.versions.json"},
		},
	}
	ctx := &TemplateContext{GatewayName: "gw", Vars: map[string]string{}}

	plan, err := buildSyncPlan(profile, ctx, repoPath, liveDir)
	if err != nil {
		t.Fatalf("buildSyncPlan: %v", err)
	}
	if plan.Mappings[0].Type != mappingTypeFile {
		t.Errorf("expected inferred type=file, got %q", plan.Mappings[0].Type)
	}
}

func TestBuildSyncPlan_PatchesWiredToMapping(t *testing.T) {
	tmp := t.TempDir()
	repoPath := filepath.Join(tmp, "repo")
	liveDir := filepath.Join(tmp, "live")

	writeFile(t, filepath.Join(repoPath, "config", "system-properties", "config.json"),
		`{"SystemName":"placeholder"}`)

	profile := &stokertypes.ResolvedProfile{
		Mappings: []stokertypes.ResolvedMapping{
			{
				Source:      "config",
				Destination: "config/resources/core",
				Patches: []stokertypes.ResolvedPatch{
					{
						File: "system-properties/config.json",
						Set:  map[string]string{"SystemName": "{{.GatewayName}}"},
					},
				},
			},
		},
	}
	ctx := &TemplateContext{GatewayName: "prod-gw", Vars: map[string]string{}}

	plan, err := buildSyncPlan(profile, ctx, repoPath, liveDir)
	if err != nil {
		t.Fatalf("buildSyncPlan: %v", err)
	}
	if plan.Mappings[0].ApplyPatches == nil {
		t.Error("expected ApplyPatches func to be set on mapping with patches")
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
