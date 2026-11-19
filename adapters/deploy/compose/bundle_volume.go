package compose

// A bundle artifact is a pair: the runtime image the Owner pinned and a
// content bundle, a reproducible tar that trusted code assembled and
// verified. The backend never mounts a host path. It loads each bundle once
// into a named volume, <factory>-bundle-<first 12 hex of the bundle digest>:
// it creates the volume, creates (and never starts) a loader container of
// the runtime image that holds the volume read-write at LoaderTarget,
// uploads the tar into the loader, and removes the loader. From then on the
// volume is mounted read-only, at BundleTarget, by revisions only.
//
// While a loader exists its load is unfinished: a load that was cut off
// leaves its loader behind, and the next Stage removes the loader and the
// volume and loads the bundle again. A load that fails removes both.

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// uploadTimeout bounds the upload of one bundle.
const uploadTimeout = 10 * time.Minute

// BundleSource yields the tar of a content bundle by its digest. The caller
// has verified the tar against the digest; the backend only streams it.
type BundleSource interface {
	Open(ctx context.Context, bundle string) (io.ReadCloser, error)
}

// bundleVolume is the name of a bundle's volume.
func (b *Backend) bundleVolume(bundle string) string {
	return string(b.cfg.Factory) + "-bundle-" + bundleHex(bundle)
}

// bundleLoader is the name of a bundle's loader container.
func (b *Backend) bundleLoader(bundle string) string {
	return string(b.cfg.Factory) + "-loader-" + bundleHex(bundle)
}

// bundleHex is the first 12 hex of a validated sha256 bundle digest.
func bundleHex(bundle string) string { return bundle[len("sha256:") : len("sha256:")+12] }

// ensureBundle makes sure the artifact's bundle volume holds the bundle,
// loading it when it is missing or its last load did not finish.
// Loads are serialised: a loader that exists while none runs is one a load
// left behind, never one another Stage is still using.
func (b *Backend) ensureBundle(ctx context.Context, a deploy.Artifact) error {
	b.bundleMu.Lock()
	defer b.bundleMu.Unlock()
	volume, loader := b.bundleVolume(a.Bundle), b.bundleLoader(a.Bundle)
	unfinished := false
	existing, err := b.api.inspect(ctx, loader)
	switch {
	case err == nil:
		if !b.isLoaderOf(existing.Config.Labels, a.Bundle) {
			return core.NewError(core.CodeConflict, "a container of the loader's name is not the loader of this bundle")
		}
		if err := b.api.removeLoader(ctx, loader); err != nil {
			return err
		}
		unfinished = true
	case !core.HasCode(err, core.CodeNotFound):
		return err
	}
	v, err := b.api.inspectVolume(ctx, volume)
	switch {
	case core.HasCode(err, core.CodeNotFound):
	case err != nil:
		return err
	case v.Labels[LabelFactory] != string(b.cfg.Factory) || v.Labels[LabelBundle] != a.Bundle:
		return core.NewError(core.CodeConflict, "a volume of the bundle's name holds another bundle")
	case !unfinished:
		return nil // loaded before
	default:
		b.cfg.Logger.Warn("an unfinished bundle load is redone", "volume", volume)
		if err := b.api.removeVolume(ctx, volume); err != nil {
			return err
		}
	}
	return b.loadBundle(ctx, a, volume, loader)
}

func (b *Backend) isLoaderOf(labels map[string]string, bundle string) bool {
	return labels[LabelFactory] == string(b.cfg.Factory) && labels[LabelLoader] == "1" && labels[LabelBundle] == bundle
}

// loadBundle creates the bundle volume and fills it through a loader. The
// loader is removed on every path; on failure the volume is too, so no
// revision ever mounts part of a bundle.
func (b *Backend) loadBundle(ctx context.Context, a deploy.Artifact, volume, loader string) (err error) {
	tar, err := b.cfg.BundleSource.Open(ctx, a.Bundle)
	if err != nil {
		return hold("the bundle could not be read", err)
	}
	defer func() { _ = tar.Close() }() // a read-only stream; the upload's answer is what counts
	// Registered before anything is made: a create that failed after the
	// daemon acted leaves nothing behind either.
	defer func() {
		cleanup := context.WithoutCancel(ctx)
		if rmErr := b.api.removeLoader(cleanup, loader); rmErr != nil {
			b.cfg.Logger.Warn("a bundle loader could not be removed", "container", loader, "error", rmErr.Error())
			if err == nil {
				err = rmErr // a loader left behind marks the load unfinished; no revision may use it yet
			}
		}
		if err != nil {
			if rmErr := b.api.removeVolume(cleanup, volume); rmErr != nil {
				b.cfg.Logger.Warn("a partly loaded bundle volume could not be removed", "volume", volume, "error", rmErr.Error())
			}
		}
	}()
	if err := b.api.createVolume(ctx, volume, map[string]string{LabelFactory: string(b.cfg.Factory), LabelBundle: a.Bundle}); err != nil {
		return err
	}
	if err := b.api.create(ctx, loader, b.loaderSpec(a)); err != nil {
		return err
	}
	return b.api.putArchive(ctx, loader, LoaderTarget, tar)
}

// removeLoader deletes a loader with the anonymous volumes its image
// declares, which would otherwise outlive it; the bundle volume is named and
// stays. One that is already gone is not an error.
func (a *dockerAPI) removeLoader(ctx context.Context, name string) error {
	err := a.decode(ctx, http.MethodDelete, "/containers/"+name, url.Values{"force": {"1"}, "v": {"1"}}, nil, nil, http.StatusNoContent, http.StatusOK)
	if core.HasCode(err, core.CodeNotFound) {
		return nil
	}
	return err
}

// volumeView is what the backend reads back about a volume.
type volumeView struct {
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

func (a *dockerAPI) inspectVolume(ctx context.Context, name string) (volumeView, error) {
	var v volumeView
	err := a.decode(ctx, http.MethodGet, "/volumes/"+name, nil, nil, &v, http.StatusOK)
	return v, err
}

func (a *dockerAPI) createVolume(ctx context.Context, name string, labels map[string]string) error {
	body := volumeView{Name: name, Labels: labels}
	return a.decode(ctx, http.MethodPost, "/volumes/create", nil, body, nil, http.StatusCreated, http.StatusOK)
}

// removeVolume deletes a bundle volume; one that is already gone is not an
// error.
func (a *dockerAPI) removeVolume(ctx context.Context, name string) error {
	err := a.decode(ctx, http.MethodDelete, "/volumes/"+name, nil, nil, nil, http.StatusNoContent, http.StatusOK)
	if core.HasCode(err, core.CodeNotFound) {
		return nil
	}
	return err
}

// putArchive streams a tar into a container at dir. It is bounded by
// uploadTimeout rather than the per-call timeout, since a bundle can be
// large.
func (a *dockerAPI) putArchive(ctx context.Context, name, dir string, tar io.Reader) error {
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()
	path := "/containers/" + name + "/archive"
	target := a.base + "/v" + a.version + path + "?" + url.Values{"path": {dir}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, tar)
	if err != nil {
		return core.NewError(core.CodeInternal, "a Docker request could not be built").WithCause(err)
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := (&http.Client{Transport: a.http.Transport}).Do(req)
	resp, err = a.answer(ctx, resp, err, http.MethodPut, path, http.StatusOK)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.Body.Close()
}
