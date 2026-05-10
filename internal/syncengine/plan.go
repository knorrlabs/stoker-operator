package syncengine

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const mappingTypeFile = "file"

// ResolvedMapping is a single source→destination mapping with absolute source
// and destination-relative target.
type ResolvedMapping struct {
	Source      string // absolute path on disk
	Destination string // relative to live dir
	Type        string // "dir" or "file" (inferred from filesystem if empty)
	Template    bool   // if true, resolve Go template vars in file contents during staging
	// ApplyPatches is called on each staged file for this mapping. It receives the
	// absolute path of the staged file and applies JSON field patches in-place.
	// Nil means no patches are configured for this mapping.
	ApplyPatches func(stagedPath string) error
}

// SyncPlan describes a complete profile-based sync operation.
type SyncPlan struct {
	Mappings        []ResolvedMapping
	ExcludePatterns []string
	StagingDir      string
	LiveDir         string
	DryRun          bool
	// ApplyTemplate is called on each staged file whose mapping has Template=true.
	// It should rewrite the file in-place with template variables resolved.
	// Binary files must be rejected by the implementation. If nil, template-enabled
	// mappings are staged without content transformation (no error).
	ApplyTemplate func(stagedPath string) error
}

// DryRunDiff reports what a dry-run sync would change.
type DryRunDiff struct {
	Added    []string
	Modified []string
	Deleted  []string
}

// ExecutePlan performs a staging-based sync: builds staging from ordered mappings,
// then merges staging to live. Orphan cleanup is scoped to managed paths only.
func (e *Engine) ExecutePlan(plan *SyncPlan) (*SyncResult, error) {
	start := time.Now()
	result := &SyncResult{}

	excludes := MergeExcludes(plan.ExcludePatterns)

	// Phase 1: Build staging directory from ordered mappings.
	if err := os.RemoveAll(plan.StagingDir); err != nil {
		return nil, fmt.Errorf("cleaning staging dir: %w", err)
	}
	if err := os.MkdirAll(plan.StagingDir, 0755); err != nil {
		return nil, fmt.Errorf("creating staging dir: %w", err)
	}

	// Track all destination-relative paths written to staging.
	stagedFiles := make(map[string]bool)

	for _, m := range plan.Mappings {
		if m.Type == mappingTypeFile {
			if err := stageSingleFile(m, plan.StagingDir, excludes, stagedFiles, plan.ApplyTemplate); err != nil {
				return nil, err
			}
		} else {
			if err := stageDirectory(m, plan.StagingDir, excludes, stagedFiles, plan.ApplyTemplate); err != nil {
				return nil, err
			}
		}
	}

	// Compute managed destination roots for orphan scoping.
	managedRoots := computeManagedRoots(plan.Mappings)

	if plan.DryRun {
		// Phase 2 (dry-run): Compute diff without writing to live.
		diff, err := computeDryRunDiff(plan.StagingDir, plan.LiveDir, managedRoots, excludes)
		if err != nil {
			return nil, fmt.Errorf("computing dry-run diff: %w", err)
		}
		result.DryRunDiff = diff
		result.FilesAdded = len(diff.Added)
		result.FilesModified = len(diff.Modified)
		result.FilesDeleted = len(diff.Deleted)
	} else {
		// Phase 2 (live): Merge staging to live directory.
		added, modified, err := mergeStagingToLive(plan.StagingDir, plan.LiveDir)
		if err != nil {
			return nil, fmt.Errorf("merging staging to live: %w", err)
		}
		result.FilesAdded = added
		result.FilesModified = modified

		// Orphan cleanup — only within managed roots.
		deleted, err := cleanOrphans(plan.StagingDir, plan.LiveDir, managedRoots, excludes)
		if err != nil {
			return nil, fmt.Errorf("cleaning orphans: %w", err)
		}
		result.FilesDeleted = deleted
	}

	// Phase 3: Cleanup staging.
	_ = os.RemoveAll(plan.StagingDir)

	result.ProjectsSynced = discoverProjects(plan.LiveDir)
	result.Duration = time.Since(start)

	return result, nil
}

// stageSingleFile copies a single file mapping into the staging directory.
// If applyTemplate is non-nil and m.Template is true, it is called on the staged file
// to resolve Go template variables in-place.
func stageSingleFile(m ResolvedMapping, stagingDir string, excludes []string, staged map[string]bool, applyTemplate func(string) error) error {
	relDst := filepath.ToSlash(m.Destination)
	if ShouldExclude(relDst, excludes) || IsProtected(relDst) {
		return nil
	}

	dstPath := filepath.Join(stagingDir, m.Destination)
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("creating staging parent for %s: %w", m.Destination, err)
	}

	if _, err := copyFileRaw(m.Source, dstPath); err != nil {
		return fmt.Errorf("staging file %s: %w", m.Destination, err)
	}

	if m.Template && applyTemplate != nil {
		if err := applyTemplate(dstPath); err != nil {
			return fmt.Errorf("templating %s: %w", m.Destination, err)
		}
	}

	if m.ApplyPatches != nil {
		if err := m.ApplyPatches(dstPath); err != nil {
			return fmt.Errorf("patching %s: %w", m.Destination, err)
		}
	}

	staged[filepath.ToSlash(m.Destination)] = true
	return nil
}

// stageDirectory walks a source directory and copies its contents into staging
// under the mapping's destination prefix. If applyTemplate is non-nil and
// m.Template is true, it is called on each staged file to resolve Go template
// variables in-place.
func stageDirectory(m ResolvedMapping, stagingDir string, excludes []string, staged map[string]bool, applyTemplate func(string) error) error {
	return filepath.WalkDir(m.Source, func(srcPath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // skip symlinks
		}

		relToSrc, err := filepath.Rel(m.Source, srcPath)
		if err != nil {
			return err
		}
		if relToSrc == "." {
			return nil
		}

		// Build destination-relative path.
		dstRel := filepath.Join(m.Destination, relToSrc)
		dstRelSlash := filepath.ToSlash(dstRel)

		if ShouldExclude(dstRelSlash, excludes) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if IsProtected(dstRelSlash) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		dstPath := filepath.Join(stagingDir, dstRel)

		if d.IsDir() {
			return os.MkdirAll(dstPath, 0755)
		}

		if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
			return fmt.Errorf("creating staging dir for %s: %w", dstRel, err)
		}
		if _, err := copyFileRaw(srcPath, dstPath); err != nil {
			return fmt.Errorf("staging %s: %w", dstRel, err)
		}

		if m.Template && applyTemplate != nil {
			if err := applyTemplate(dstPath); err != nil {
				return fmt.Errorf("templating %s: %w", dstRel, err)
			}
		}

		if m.ApplyPatches != nil {
			if err := m.ApplyPatches(dstPath); err != nil {
				return fmt.Errorf("patching %s: %w", dstRel, err)
			}
		}

		staged[dstRelSlash] = true
		return nil
	})
}

// copyFileRaw copies src to dst unconditionally (no equality check).
// Used for staging where we always want to write.
func copyFileRaw(src, dst string) (bool, error) {
	return copyFile(src, dst)
}

// computeManagedRoots returns the set of destination paths managed by the plan's mappings.
// For directory mappings: the destination directory itself.
// For file mappings: the destination file path itself (NOT the parent directory).
// Using the parent directory for file mappings would add "." for root-level files like
// ".versions.json", causing isUnderManagedRoot to match every path and orphan-delete all
// files in the live directory that Ignition wrote at runtime.
func computeManagedRoots(mappings []ResolvedMapping) map[string]bool {
	roots := make(map[string]bool)
	for _, m := range mappings {
		roots[filepath.ToSlash(m.Destination)] = true
	}
	return roots
}

// isUnderManagedRoot checks if a path falls within any managed root.
// For directory roots: matches the root itself and all paths below it.
// For file roots: exact match only.
func isUnderManagedRoot(relPath string, managedRoots map[string]bool) bool {
	relPath = filepath.ToSlash(relPath)
	for root := range managedRoots {
		if relPath == root || strings.HasPrefix(relPath, root+"/") {
			return true
		}
	}
	return false
}

// isAncestorOfManagedRoot returns true if relPath is a strict parent directory
// of any managed root. For example, "config" is an ancestor of
// "config/resources/core". Used to allow traversal into parent directories
// without treating them as managed (i.e. eligible for orphan deletion).
func isAncestorOfManagedRoot(relPath string, managedRoots map[string]bool) bool {
	relPath = filepath.ToSlash(relPath)
	for root := range managedRoots {
		if strings.HasPrefix(root, relPath+"/") {
			return true
		}
	}
	return false
}

// mergeStagingToLive walks staging and copies changed files to live.
func mergeStagingToLive(stagingDir, liveDir string) (added, modified int, err error) {
	err = filepath.WalkDir(stagingDir, func(stagingPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // skip symlinks
		}

		relPath, relErr := filepath.Rel(stagingDir, stagingPath)
		if relErr != nil {
			return relErr
		}
		if relPath == "." {
			return nil
		}

		livePath := filepath.Join(liveDir, relPath)

		if d.IsDir() {
			return os.MkdirAll(livePath, 0755)
		}

		_, existErr := os.Lstat(livePath)
		existed := existErr == nil

		written, copyErr := copyFile(stagingPath, livePath)
		if copyErr != nil {
			return fmt.Errorf("merging %s: %w", relPath, copyErr)
		}

		if written {
			if existed {
				modified++
			} else {
				added++
			}
		}
		return nil
	})
	return added, modified, err
}

// cleanOrphans removes files in live that are under managed roots but not in staging.
func cleanOrphans(stagingDir, liveDir string, managedRoots map[string]bool, excludes []string) (int, error) {
	deleted := 0

	err := filepath.WalkDir(liveDir, func(livePath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // skip symlinks
		}

		relPath, relErr := filepath.Rel(liveDir, livePath)
		if relErr != nil {
			return relErr
		}
		if relPath == "." {
			return nil
		}

		// Skip protected and excluded paths.
		if IsProtected(relPath) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if ShouldExclude(relPath, excludes) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Only clean within managed roots.
		if !isUnderManagedRoot(relPath, managedRoots) {
			if d.IsDir() {
				// Traverse into parent directories of managed roots so we can
				// reach nested managed subtrees (e.g. "config" → "config/resources/dev").
				if isAncestorOfManagedRoot(relPath, managedRoots) {
					return nil
				}
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil
		}

		// Check if this file exists in staging.
		stagingPath := filepath.Join(stagingDir, relPath)
		if _, err := os.Lstat(stagingPath); os.IsNotExist(err) {
			if removeErr := os.Remove(livePath); removeErr != nil && !os.IsNotExist(removeErr) {
				return fmt.Errorf("removing orphan %s: %w", relPath, removeErr)
			}
			deleted++
			// Remove now-empty parent directories up to the managed root boundary.
			parentDir := filepath.Dir(livePath)
			for {
				parentRel, relErr := filepath.Rel(liveDir, parentDir)
				if relErr != nil || !isUnderManagedRoot(filepath.ToSlash(parentRel), managedRoots) {
					break
				}
				entries, rdErr := os.ReadDir(parentDir)
				if rdErr != nil || len(entries) > 0 {
					break
				}
				if rmErr := os.Remove(parentDir); rmErr != nil {
					break
				}
				parentDir = filepath.Dir(parentDir)
			}
		}
		return nil
	})

	return deleted, err
}

// computeDryRunDiff compares staging against live to produce a diff without writing.
func computeDryRunDiff(stagingDir, liveDir string, managedRoots map[string]bool, excludes []string) (*DryRunDiff, error) {
	diff := &DryRunDiff{}

	// Find added and modified files.
	err := filepath.WalkDir(stagingDir, func(stagingPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // skip symlinks
		}

		relPath, relErr := filepath.Rel(stagingDir, stagingPath)
		if relErr != nil {
			return relErr
		}
		if relPath == "." || d.IsDir() {
			return nil
		}

		livePath := filepath.Join(liveDir, relPath)
		if _, err := os.Lstat(livePath); os.IsNotExist(err) {
			diff.Added = append(diff.Added, filepath.ToSlash(relPath))
		} else if !filesEqual(stagingPath, livePath) {
			diff.Modified = append(diff.Modified, filepath.ToSlash(relPath))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Find deleted files (in live under managed roots but not in staging).
	err = filepath.WalkDir(liveDir, func(livePath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // skip symlinks
		}

		relPath, relErr := filepath.Rel(liveDir, livePath)
		if relErr != nil {
			return relErr
		}
		if relPath == "." {
			return nil
		}

		if IsProtected(relPath) || ShouldExclude(relPath, excludes) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if !isUnderManagedRoot(relPath, managedRoots) {
			if d.IsDir() {
				// Traverse into parent directories of managed roots so we can
				// reach nested managed subtrees (e.g. "config" → "config/resources/dev").
				if isAncestorOfManagedRoot(relPath, managedRoots) {
					return nil
				}
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil
		}

		stagingPath := filepath.Join(stagingDir, relPath)
		if _, err := os.Lstat(stagingPath); os.IsNotExist(err) {
			diff.Deleted = append(diff.Deleted, filepath.ToSlash(relPath))
		}
		return nil
	})

	return diff, err
}
