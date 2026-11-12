//go:build smoke

package smoke

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/adapters/deploy/local"
	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// How the run proves that a Standalone release runs the candidate's own
// build: the supervisor keeps the program each build exports in its
// program store, under the sha256 of the bytes, and the local deploy
// backend starts that file. The run looks at the edge while the release is
// under way. The first time the edge reports the candidate's digest as the
// running revision it records what the page answers, the state of the
// candidate's pull request at that moment and the sha256 of the store's
// file. After that it leaves the edge alone, so the release is judged as
// it would be without the run: by the low-traffic hold.

// programStoreDir is where the supervisor of a Standalone install keeps
// the programs builds exported (internal/supervisor/programstore).
func programStoreDir(home string) string {
	return filepath.Join(install.Layout{Dir: home}.Data("trust"), "programs")
}

// storedProgram is the store's file of the program with digest.
func storedProgram(home, digest string) string {
	return filepath.Join(programStoreDir(home), "sha256-"+strings.TrimPrefix(digest, "sha256:"))
}

// fileDigest is the sha256 of a file, "" when it cannot be read.
func fileDigest(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// installedAppDigest is the digest `sisf up` pinned for the application it
// built: the revision the factory serves before any release.
func installedAppDigest(home string) (string, error) {
	m, err := install.Layout{Dir: home}.LoadManifest()
	if err != nil {
		return "", err
	}
	return string(m.Units[manifest.UnitApp].Digest), nil
}

// edgeRevision is the revision the application behind the edge reports.
func edgeRevision(ctx context.Context) (string, error) {
	status, body, err := httpGet(ctx, loopbackURL(install.PortEdge, "/healthz"))
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("GET /healthz on the edge answered %d", status)
	}
	var health struct{ Revision string }
	if err := json.Unmarshal([]byte(body), &health); err != nil {
		return "", fmt.Errorf("GET /healthz on the edge did not answer with JSON")
	}
	return health.Revision, nil
}

// candidateSighting is what the run saw the first time the edge served the
// candidate.
type candidateSighting struct {
	At       time.Time
	Revision string
	// PageStatus and Page are what GET / answered through the edge.
	PageStatus int
	Page       string
	// PullRequest is the candidate's pull request and PullState its state
	// at that moment.
	PullRequest, PullState string
	// StoreDigest is the sha256 of the store's file for the revision.
	StoreDigest string
}

func (s candidateSighting) String() string {
	return fmt.Sprintf("at %s the edge served revision %s; GET / answered HTTP %d %q; the store's file for it hashes to %s; pull request %s was %s",
		s.At.UTC().Format(time.RFC3339), s.Revision, s.PageStatus, truncate(strings.TrimSpace(s.Page), 120), orNone(s.StoreDigest), orNone(s.PullRequest), orNone(s.PullState))
}

// candidateWatch looks for the candidate on the edge until it saw it once.
type candidateWatch struct {
	home string
	// installed is the revision served before any release.
	installed string
	sighting  *candidateSighting
}

// look records the sighting when the edge serves candidate, the digest the
// factory's record names for the run's candidate. It sends the edge one
// health request per call until then, and one page request when it sees it.
func (w *candidateWatch) look(ctx context.Context, r *commandRunner, candidate, pullRequest string) {
	if w == nil || w.sighting != nil || candidate == "" {
		return
	}
	revision, err := edgeRevision(ctx)
	if err != nil || revision != candidate || revision == w.installed {
		return
	}
	s := candidateSighting{At: time.Now(), Revision: revision, PullRequest: pullRequest,
		StoreDigest: fileDigest(storedProgram(w.home, revision))}
	s.PageStatus, s.Page, _ = httpGet(ctx, loopbackURL(install.PortEdge, "/"))
	if pullRequest != "" {
		if pr, err := pullRequestMerge(ctx, r, pullRequest); err == nil {
			s.PullState = pr.State
		}
	}
	w.sighting = &s
	r.logf("the candidate is served: %s", s)
}

// assertCandidateServed checks that the release ran the candidate's own
// build: the edge served the digest the factory records for the candidate,
// the store's file for it has exactly that sha256, and its pull request
// was still open. wantStatus and wantPage are what its page had to answer.
func assertCandidateServed(r *commandRunner, w *candidateWatch, candidate string, wantStatus int, wantPage string) {
	r.t.Helper()
	const name = "the candidate's own build served through the edge before the merge"
	if w == nil || w.sighting == nil {
		result(r, name, false, "the edge never reported the candidate's digest "+orNone(candidate)+" as the running revision")
		return
	}
	s := *w.sighting
	ok := candidate != "" && s.Revision == candidate && s.StoreDigest == candidate && s.Revision != w.installed &&
		s.PullState == "OPEN" && s.PageStatus == wantStatus && strings.Contains(s.Page, wantPage)
	result(r, name, ok, fmt.Sprintf("%s; the factory records the candidate as %s and the installed release is %s; the page had to answer HTTP %d with %q",
		s, orNone(candidate), orNone(w.installed), wantStatus, wantPage))
}

// appListener is the process that listens on one of the application's two
// slots and the command line it runs.
type appListener struct {
	Port    int
	PID     string
	Command string
}

// appListeners finds the processes listening on the application's slots.
func appListeners(ctx context.Context, r *commandRunner) []appListener {
	var out []appListener
	for _, port := range []int{local.BluePort, local.GreenPort} {
		pids, err := r.run(ctx, time.Minute, nil, "lsof", "-nP", "-t", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN")
		if err != nil {
			continue // lsof exits 1 when nothing listens
		}
		for pid := range strings.FieldsSeq(pids) {
			if _, err := strconv.Atoi(pid); err != nil {
				continue
			}
			command, _ := r.run(ctx, time.Minute, nil, "ps", "-o", "command=", "-p", pid)
			out = append(out, appListener{Port: port, PID: pid, Command: strings.TrimSpace(command)})
		}
	}
	return out
}

// servesPromoted waits until the edge serves digest from a process that
// was not running before (not one of before) and reports what serves it:
// the page it answers and the program the serving process runs.
func servesPromoted(ctx context.Context, r *commandRunner, home, digest string, before []appListener, limit time.Duration) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	old := map[string]bool{}
	for _, l := range before {
		old[l.PID] = true
	}
	want := storedProgram(home, digest)
	last := "the edge did not answer"
	for {
		revision, err := edgeRevision(ctx)
		if err == nil {
			last = "the edge serves revision " + revision
		}
		if err == nil && revision == digest {
			var fresh []appListener
			for _, l := range appListeners(ctx, r) {
				if !old[l.PID] {
					fresh = append(fresh, l)
				}
			}
			status, page, _ := httpGet(ctx, loopbackURL(install.PortEdge, "/"))
			runsStore := false
			var commands []string
			for _, l := range fresh {
				commands = append(commands, fmt.Sprintf("port %d pid %s runs %s", l.Port, l.PID, l.Command))
				runsStore = runsStore || strings.HasPrefix(l.Command, want)
			}
			last = fmt.Sprintf("the edge serves revision %s; GET / answered HTTP %d %q; %s; the store's file hashes to %s",
				revision, status, truncate(strings.TrimSpace(page), 120), strings.Join(commands, "; "), orNone(fileDigest(want)))
			if len(fresh) > 0 && runsStore && status == http.StatusOK && fileDigest(want) == digest {
				return true, last
			}
		}
		select {
		case <-ctx.Done():
			return false, last + " (gave up after " + limit.String() + ")"
		case <-time.After(pollInterval):
		}
	}
}
