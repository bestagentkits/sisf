package dockerfilter

// Bundle volumes and uploads of the deploy role (see deploy_bundle_mounts.go):
//
//   - POST /vX/volumes/create of a bundle volume only: a body of exactly
//     Name and Labels, the name <factory>-bundle-<12 hex> and the labels
//     exactly sisf.factory and sisf.bundle with that hex. No driver or
//     driver option can be sent, so a volume cannot be a host path in
//     disguise.
//   - GET /vX/volumes/{name} of a bundle volume name, answered by the filter
//     with the volume's name and labels and nothing else.
//   - DELETE /vX/volumes/{name} of a bundle volume this factory labelled.
//     The daemon refuses to remove a volume a container still mounts.
//   - PUT /vX/containers/{id}/archive?path=/sisf-bundle into a bundle loader
//     that is not running, streamed and bounded by MaxArchiveBytes.

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
)

// volumeCreateRequest is the only volume configuration the filter forwards.
type volumeCreateRequest struct {
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

// createVolume checks the create of a bundle volume and forwards the
// filter's own encoding of it.
func (f *Filter) createVolume(w http.ResponseWriter, r *http.Request, rt route) *denial {
	raw, d := f.readBody(r)
	if d != nil {
		return d
	}
	body, err := f.checkVolumeCreate(raw)
	if err != nil {
		return deny(http.StatusForbidden, err.Error())
	}
	return f.forward(w, r, http.MethodPost, rt.version, "/volumes/create", nil, body)
}

func (f *Filter) checkVolumeCreate(raw []byte) ([]byte, error) {
	if err := scanStrict(raw); err != nil {
		return nil, err
	}
	if _, err := objectKeys(raw, "the volume", "Name", "Labels"); err != nil {
		return nil, err
	}
	var req volumeCreateRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, errors.New("the volume configuration has a field of the wrong type")
	}
	hex, ok := f.bundleVolumeHex(req.Name)
	bundle := deployDigestPattern.FindStringSubmatch(req.Labels[DeployLabelBundle])
	want := map[string]string{DeployLabelFactory: string(f.cfg.RunnerValue), DeployLabelBundle: req.Labels[DeployLabelBundle]}
	switch {
	case f.cfg.BundleTarget == "":
		return nil, errors.New("this filter allows no bundle volume")
	case !ok:
		return nil, errors.New("only a bundle volume of this factory may be created")
	case bundle == nil || bundle[1][:12] != hex || !maps.Equal(req.Labels, want):
		return nil, errors.New("a bundle volume carries exactly this factory's sisf.factory and the sisf.bundle its name is derived from")
	}
	return json.Marshal(req)
}

// volumeView is what the filter answers about a bundle volume.
type volumeView struct {
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

// fetchVolume inspects a bundle volume of this factory's name.
func (f *Filter) fetchVolume(r *http.Request, rt route) (volumeView, *denial) {
	if _, ok := f.bundleVolumeHex(rt.container); !ok {
		return volumeView{}, deny(http.StatusForbidden, "only a bundle volume of this factory may be named")
	}
	status, body, d := f.fetch(r, rt.version, "/volumes/"+rt.container)
	switch {
	case d != nil:
		return volumeView{}, d
	case status == http.StatusNotFound:
		return volumeView{}, deny(http.StatusNotFound, "no such volume")
	case status != http.StatusOK:
		return volumeView{}, deny(http.StatusBadGateway, "the daemon could not inspect the volume")
	}
	var seen volumeView
	if err := json.Unmarshal(body, &seen); err != nil || seen.Name != rt.container {
		return volumeView{}, deny(http.StatusBadGateway, "the daemon's volume answer is not understood")
	}
	return seen, nil
}

// inspectVolume answers with a bundle volume's name and labels only.
func (f *Filter) inspectVolume(w http.ResponseWriter, r *http.Request, rt route) *denial {
	seen, d := f.fetchVolume(r, rt)
	if d != nil {
		return d
	}
	writeJSON(w, http.StatusOK, seen)
	return nil
}

// removeVolume removes a bundle volume this factory labelled.
func (f *Filter) removeVolume(w http.ResponseWriter, r *http.Request, rt route) *denial {
	seen, d := f.fetchVolume(r, rt)
	if d != nil {
		return d
	}
	if seen.Labels[DeployLabelFactory] != string(f.cfg.RunnerValue) {
		return deny(http.StatusForbidden, "the volume is not a bundle volume of this factory")
	}
	return f.forward(w, r, http.MethodDelete, rt.version, "/volumes/"+rt.container, nil, nil)
}

// checkArchive judges the framing of an upload before the target is read:
// a tar stream at the loader's path, within the size bound.
func (f *Filter) checkArchive(r *http.Request, rt route) *denial {
	ct := strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0])
	switch {
	case rt.query.Get("path") != DeployLoaderTarget:
		return deny(http.StatusForbidden, "a bundle is uploaded at "+DeployLoaderTarget+" only")
	case !strings.EqualFold(ct, "application/x-tar"):
		return deny(http.StatusBadRequest, "an upload must be a tar stream")
	case r.ContentLength == 0:
		return deny(http.StatusBadRequest, "an upload needs a body")
	case r.ContentLength > f.cfg.MaxArchiveBytes:
		return deny(http.StatusRequestEntityTooLarge, "the upload is larger than "+strconv.FormatInt(f.cfg.MaxArchiveBytes, 10)+" bytes")
	}
	return nil
}

// forwardArchive streams a checked upload to the loader with the full ID
// the filter inspected. The body is never held whole; one byte past the
// bound fails the upload, and the daemon sees a cut stream.
func (f *Filter) forwardArchive(w http.ResponseWriter, r *http.Request, rt route, id string) *denial {
	target := f.cfg.UpstreamURL + "/v" + rt.version + "/containers/" + id + "/archive?path=" + DeployLoaderTarget
	body := &boundedReader{r: r.Body, left: f.cfg.MaxArchiveBytes}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPut, target, body)
	if err != nil {
		return deny(http.StatusInternalServerError, "the request to the daemon could not be built")
	}
	req.Header.Set("Content-Type", "application/x-tar")
	req.ContentLength = r.ContentLength
	if r.ContentLength < 0 {
		req.ContentLength = -1
	}
	resp, err := f.cfg.Upstream.RoundTrip(req)
	switch {
	case body.over.Load():
		if err == nil {
			_ = resp.Body.Close()
		}
		return deny(http.StatusRequestEntityTooLarge, "the upload is larger than "+strconv.FormatInt(f.cfg.MaxArchiveBytes, 10)+" bytes")
	case err != nil:
		return deny(http.StatusBadGateway, "the Docker daemon did not answer")
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, maxInspectBytes)) // the status is what counts
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(answer)))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(answer)
	return nil
}

// errArchiveTooLarge cuts an upload that passed its bound.
var errArchiveTooLarge = errors.New("the upload passed its size bound")

// boundedReader reads at most left bytes and fails, remembering why, on
// one more.
type boundedReader struct {
	r    io.Reader
	left int64
	over atomic.Bool // read by the handler while the transport may still be reading
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.left <= 0 {
		var one [1]byte
		if n, _ := b.r.Read(one[:]); n > 0 {
			b.over.Store(true)
			return 0, errArchiveTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.r.Read(p)
	b.left -= int64(n)
	return n, err
}
