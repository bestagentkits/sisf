package appbundle

import "github.com/bestagentkits/sisf/pkg/managedapp"

// DefaultLimits bound a managed application's content bundle wherever it
// is assembled, so `sisf app bundle` and the factory's build agree on what
// a bundle may hold.
var DefaultLimits = Limits{MaxTotalBytes: 1 << 30, MaxFiles: 100_000, MaxFileBytes: 128 << 20}

// SpecInput is the Assemble input for the bundle spec describes, built from
// the checkout in repoDir. `sisf app bundle` and the factory's build both
// use it, so one checkout gives one digest whichever of them assembles it.
func SpecInput(spec *managedapp.Spec, repoDir string, fetcher Fetcher, cacheDir, outputPath string) Input {
	return Input{
		RepoDir: repoDir, Bundle: spec.Bundle, Fetcher: fetcher, CacheDir: cacheDir, OutputPath: outputPath,
		Limits: DefaultLimits, Mountpoints: spec.BundleMountpoints(),
	}
}
