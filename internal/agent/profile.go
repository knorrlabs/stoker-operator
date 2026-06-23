package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/tidwall/sjson"

	"github.com/knorrlabs/stoker-operator/internal/syncengine"
	stokertypes "github.com/knorrlabs/stoker-operator/pkg/types"
)

// TemplateContext holds the variables available in mapping templates.
type TemplateContext struct {
	GatewayName string
	PodName     string
	PodOrdinal  int // StatefulSet replica index (0, 1, 2, ...); 0 for non-StatefulSet pods
	Namespace   string
	Ref         string
	Commit      string
	CRName      string
	Labels      map[string]string
	Vars        map[string]string
}

// buildTemplateContext creates a TemplateContext from agent config, metadata, and pod labels.
func buildTemplateContext(cfg *Config, meta *Metadata, profileVars map[string]string, labels map[string]string) *TemplateContext {
	vars := make(map[string]string, len(profileVars))
	maps.Copy(vars, profileVars)
	podLabels := make(map[string]string, len(labels))
	maps.Copy(podLabels, labels)
	return &TemplateContext{
		GatewayName: cfg.GatewayName,
		PodName:     cfg.PodName,
		PodOrdinal:  podOrdinal(cfg.PodName, labels),
		Namespace:   cfg.CRNamespace,
		Ref:         meta.Ref,
		Commit:      meta.Commit,
		CRName:      cfg.CRName,
		Labels:      podLabels,
		Vars:        vars,
	}
}

// podOrdinal extracts the StatefulSet replica index for a pod. It prefers the
// K8s-native apps.kubernetes.io/pod-index label (set automatically by K8s 1.27+)
// and falls back to parsing the trailing integer from the pod name. Returns 0
// for non-StatefulSet pods or any pod whose name does not end in a digit.
func podOrdinal(podName string, labels map[string]string) int {
	if idxStr, ok := labels["apps.kubernetes.io/pod-index"]; ok {
		if n, err := strconv.Atoi(idxStr); err == nil {
			return n
		}
	}
	if parts := strings.Split(podName, "-"); len(parts) > 0 {
		if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			return n
		}
	}
	return 0
}

// resolveTemplate resolves a Go template string using the given context.
// Returns an error if any referenced key is missing.
func resolveTemplate(tmpl string, ctx *TemplateContext) (string, error) {
	// Fast path: no template syntax.
	if !strings.Contains(tmpl, "{{") {
		return tmpl, nil
	}

	t, err := template.New("").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parsing template %q: %w", tmpl, err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("executing template %q: %w", tmpl, err)
	}
	return buf.String(), nil
}

// validateResolvedPath rejects paths with traversal or absolute components.
func validateResolvedPath(path, label string) error {
	if filepath.IsAbs(path) {
		return fmt.Errorf("%s: absolute path not allowed: %s", label, path)
	}
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return fmt.Errorf("%s: path traversal not allowed: %s", label, path)
	}
	return nil
}

// inferMappingType checks the filesystem to determine whether absSrc is a file
// or directory, validating against an explicitly provided hint when given.
// Returns "dir" or "file".
func inferMappingType(absSrc, hintType string) (string, error) {
	info, err := os.Stat(absSrc)
	if err != nil {
		// Source doesn't exist: caller handles required check separately; fall back to dir.
		return "dir", nil
	}

	actual := "file"
	if info.IsDir() {
		actual = "dir"
	}

	// Validate against explicit hint when provided.
	if hintType != "" && hintType != actual {
		return "", fmt.Errorf("type mismatch: spec says %q but %s is a %s", hintType, absSrc, actual)
	}

	return actual, nil
}

// buildSyncPlan constructs a SyncPlan from a resolved profile, template context,
// and runtime paths. The profile already has defaults merged by the controller.
func buildSyncPlan(
	profile *stokertypes.ResolvedProfile,
	tmplCtx *TemplateContext,
	repoPath string,
	liveDir string,
) (*syncengine.SyncPlan, error) {
	stagingDir := filepath.Join(liveDir, ".sync-staging")

	plan := &syncengine.SyncPlan{
		StagingDir:    stagingDir,
		LiveDir:       liveDir,
		DryRun:        profile.DryRun,
		ApplyTemplate: buildApplyTemplateFunc(tmplCtx),
	}

	// Resolve and validate each mapping.
	for i, m := range profile.Mappings {
		src, err := resolveTemplate(m.Source, tmplCtx)
		if err != nil {
			return nil, fmt.Errorf("mapping[%d].source: %w", i, err)
		}
		dst, err := resolveTemplate(m.Destination, tmplCtx)
		if err != nil {
			return nil, fmt.Errorf("mapping[%d].destination: %w", i, err)
		}

		if err := validateResolvedPath(src, fmt.Sprintf("mapping[%d].source", i)); err != nil {
			return nil, err
		}
		if err := validateResolvedPath(dst, fmt.Sprintf("mapping[%d].destination", i)); err != nil {
			return nil, err
		}

		absSrc := filepath.Join(repoPath, src)

		// Check required flag before type inference (stat happens in both, but keep intent clear).
		if m.Required {
			if _, err := os.Stat(absSrc); os.IsNotExist(err) {
				return nil, fmt.Errorf("mapping[%d]: required source does not exist: %s", i, src)
			}
		}

		// Infer type from filesystem; validate against hint if provided.
		typ, err := inferMappingType(absSrc, m.Type)
		if err != nil {
			return nil, fmt.Errorf("mapping[%d]: %w", i, err)
		}

		plan.Mappings = append(plan.Mappings, syncengine.ResolvedMapping{
			Source:       absSrc,
			Destination:  dst,
			Type:         typ,
			Template:     m.Template,
			ApplyPatches: buildApplyPatchesFunc(m.Patches, tmplCtx, stagingDir, dst, typ == "file"),
		})
	}

	// Excludes already merged by controller (defaults + profile).
	plan.ExcludePatterns = profile.ExcludePatterns

	return plan, nil
}

// buildApplyTemplateFunc returns a function that resolves Go template variables
// inside a staged file in-place. Binary files (containing null bytes) are
// rejected with an error (fail-closed policy).
func buildApplyTemplateFunc(tmplCtx *TemplateContext) func(string) error {
	return func(stagedPath string) error {
		content, err := os.ReadFile(stagedPath)
		if err != nil {
			return fmt.Errorf("reading file for templating: %w", err)
		}

		// Binary file detection: reject files containing null bytes.
		if bytes.IndexByte(content, 0) >= 0 {
			return fmt.Errorf("template=true on binary file is not supported: %s", stagedPath)
		}

		// Fast path: skip resolution if no template syntax present.
		if !strings.Contains(string(content), "{{") {
			return nil
		}

		resolved, err := resolveTemplate(string(content), tmplCtx)
		if err != nil {
			return fmt.Errorf("resolving template in %s: %w", stagedPath, err)
		}

		if err := os.WriteFile(stagedPath, []byte(resolved), 0644); err != nil {
			return fmt.Errorf("writing templated file %s: %w", stagedPath, err)
		}
		return nil
	}
}

// buildApplyPatchesFunc returns a per-file closure that applies JSON field patches
// to any staged file whose path matches one of the patch specs. Returns nil when
// no patches are configured for the mapping (fast path: no closure overhead).
//
// stagedPath is the absolute path of the staged file. The closure computes a path
// relative to the mapping's staging root to match against each patch's File field,
// which supports doublestar glob patterns.
//
// isFileMapping must be true when the mapping target is a single file. In that case
// the root is the parent directory so relToMapping is just the base filename —
// consistent with the CRD's guidance that file is the base name for file mappings.
func buildApplyPatchesFunc(
	patches []stokertypes.ResolvedPatch,
	tmplCtx *TemplateContext,
	stagingDir string,
	mappingDest string,
	isFileMapping bool,
) func(string) error {
	if len(patches) == 0 {
		return nil
	}

	var mappingRoot string
	if isFileMapping {
		// For file mappings, compute paths relative to the parent directory so that
		// relToMapping equals just the base filename (e.g. ".versions.json").
		mappingRoot = filepath.Join(stagingDir, filepath.Dir(mappingDest))
	} else {
		mappingRoot = filepath.Join(stagingDir, mappingDest)
	}

	return func(stagedPath string) error {
		// Compute this file's path relative to the mapping's root in staging.
		relToMapping, err := filepath.Rel(mappingRoot, stagedPath)
		if err != nil {
			return fmt.Errorf("computing rel path for patch: %w", err)
		}
		relToMapping = filepath.ToSlash(relToMapping)

		for _, p := range patches {
			pattern := p.File
			if pattern == "" {
				// Empty file field targets the mapping file itself (file mappings only):
				// match when we're at the root — i.e. the staged file IS mappingDest.
				pattern = filepath.Base(mappingDest)
			}

			matched, err := doublestar.Match(pattern, relToMapping)
			if err != nil {
				return fmt.Errorf("invalid patch file pattern %q: %w", pattern, err)
			}
			if !matched {
				continue
			}

			// Read the file and apply each path/value pair.
			raw, err := os.ReadFile(stagedPath)
			if err != nil {
				return fmt.Errorf("reading file for patch: %w", err)
			}

			result := string(raw)
			for sjsonPath, rawVal := range p.Set {
				resolved, err := resolveTemplate(rawVal, tmplCtx)
				if err != nil {
					return fmt.Errorf("resolving patch value for path %q: %w", sjsonPath, err)
				}

				result, err = applyJSONPatch(result, sjsonPath, resolved)
				if err != nil {
					return fmt.Errorf("applying patch to %s at %q: %w", relToMapping, sjsonPath, err)
				}
			}

			if err := os.WriteFile(stagedPath, []byte(result), 0644); err != nil {
				return fmt.Errorf("writing patched file %s: %w", relToMapping, err)
			}
		}
		return nil
	}
}

// applyJSONPatch applies a single sjson-style path update to a JSON document.
// rawValue is a Go-template-resolved string. It is type-inferred before setting:
// valid JSON literals (true, false, null, numbers, and quoted strings like "\"foo\"")
// are decoded to their native Go types; bare strings are set as-is.
// Returns an error if content is not valid JSON.
func applyJSONPatch(content, path, rawValue string) (string, error) {
	if !json.Valid([]byte(content)) {
		return "", fmt.Errorf("file is not valid JSON")
	}

	// Type-infer the value: attempt JSON decode first, fall back to plain string.
	var typedValue any
	if err := json.Unmarshal([]byte(rawValue), &typedValue); err != nil {
		typedValue = rawValue
	}

	result, err := sjson.Set(content, path, typedValue)
	if err != nil {
		return "", fmt.Errorf("sjson.Set %q: %w", path, err)
	}
	return result, nil
}
