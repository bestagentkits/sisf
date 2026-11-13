package stages

import (
	"bytes"
	"encoding/base64"
	"slices"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/internal/supervisor/programstore"
	"github.com/bestagentkits/sisf/pkg/core"
)

// reportPrefix starts every line the report phase reports a result on.
const reportPrefix = "sisf-report"

// reportKeys are the keys of a report, each reported exactly once.
// The SBOM and provenance are not among them: the supervisor makes those of
// the program it received, and a report that still names them is refused.
var reportKeys = []string{"image-digest", "image"}

// programKey is the key of the lines that export the unit's program: any
// number of them, in order, each holding standard base64 that decodes by
// itself.
const programKey = "program"

// maxReportLine bounds one line of the report that is read. A line of the
// program carries far less: a few kilobytes of base64.
const maxReportLine = 16 << 10

// reportReader reads the output of the report phase as it arrives and
// keeps only the lines that carry the nonce. The program is decoded
// straight into the store's intake, so it is never held whole in memory.
//
// Write never fails: an error would reach the caller as a cut stream.
// What is wrong with the report is kept and told by result.
type reportReader struct {
	prefix string
	// line is the line being read, up to maxReportLine; long is set when
	// it is longer.
	line []byte
	long bool
	// values are the keys reported once.
	values map[string]string
	// program receives the exported program.
	program  *programstore.Intake
	exported bool
	// fault is the first thing wrong with the report; failed is the first
	// failure to keep the program.
	fault  string
	failed error
}

func newReportReader(nonce string, program *programstore.Intake) *reportReader {
	return &reportReader{prefix: reportPrefix + " " + nonce + " ", values: map[string]string{}, program: program}
}

// Write takes the next piece of the output, which may end anywhere.
func (r *reportReader) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		piece := p
		if end >= 0 {
			piece = p[:end]
		}
		if room := maxReportLine - len(r.line); len(piece) > room {
			piece, r.long = piece[:room], true
		}
		r.line = append(r.line, piece...)
		if end < 0 {
			break
		}
		r.endLine()
		p = p[end+1:]
	}
	return n, nil
}

// endLine reads the line that just ended, when it is the reporter's own.
func (r *reportReader) endLine() {
	line, long := strings.TrimSpace(string(r.line)), r.long
	r.line, r.long = r.line[:0], false
	rest, ok := strings.CutPrefix(line, r.prefix)
	if !ok || r.fault != "" || r.failed != nil {
		return
	}
	if long {
		r.fault = "a line of the report is longer than " + strconv.Itoa(maxReportLine) + " bytes"
		return
	}
	key, value, ok := strings.Cut(rest, ":")
	value = strings.TrimSpace(value)
	switch {
	case ok && key == programKey:
		r.addProgram(value)
	case !ok || !slices.Contains(reportKeys, key):
		r.fault = "the report names an unknown key"
	default:
		if _, twice := r.values[key]; twice {
			r.fault = "the report names " + key + " twice"
			return
		}
		r.values[key] = value
	}
}

// addProgram decodes one line of the program into the store.
func (r *reportReader) addProgram(encoded string) {
	r.exported = true
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		r.fault = "the exported program is not base64, line by line"
		return
	}
	switch _, err := r.program.Write(raw); {
	case core.HasCode(err, core.CodeLimitExceeded):
		r.fault = "the exported program is larger than the supervisor's program store receives"
	case err != nil:
		r.failed = err
	}
}

// result reads what the report said once the phase has ended. Every key
// must be reported exactly once, the digest must be valid and the image
// pinned to it. The report must have exported a
// program and the digest it names must be the sha256 of exactly the bytes
// received: only then is the program kept, under that digest.
func (r *reportReader) result() (ImageResult, error) {
	if len(r.line) > 0 {
		r.endLine()
	}
	switch {
	case r.failed != nil:
		return ImageResult{}, r.failed
	case r.fault != "":
		return ImageResult{}, reportUnusable(r.fault)
	}
	for _, key := range reportKeys {
		if r.values[key] == "" {
			return ImageResult{}, reportUnusable("the report does not name " + key)
		}
	}
	digest := core.Digest(r.values["image-digest"])
	switch {
	case digest.Validate() != nil:
		return ImageResult{}, reportUnusable("the reported image digest is not a digest")
	case !strings.HasSuffix(r.values["image"], "@"+string(digest)):
		return ImageResult{}, reportUnusable("the reported image is not pinned to the reported digest")
	case !usableImageName(strings.TrimSuffix(r.values["image"], "@"+string(digest))):
		return ImageResult{}, reportUnusable("the reported image has no name, or a name with a space or a second digest")
	}
	if !r.exported {
		return ImageResult{}, reportUnusable("the report exports no program (key " + programKey + "); the supervisor accepts a build " +
			"only on the program it exports for SISF_REPORT_TARGET_OS and SISF_REPORT_TARGET_ARCH, never on an image it names")
	}
	if received := r.program.Digest(); received != digest {
		return ImageResult{}, reportUnusable("the report names the image digest " + string(digest) +
			" but the program it exported has digest " + string(received))
	}
	if _, err := r.program.Commit(); err != nil {
		return ImageResult{}, err
	}
	return ImageResult{Digest: digest, Image: r.values["image"]}, nil
}

// usableImageName says whether name, the reported image without its digest,
// can name the build in the SBOM the supervisor makes of it.
func usableImageName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "@ \t\n\r")
}

// reportUnusable is the outage of a build command whose report phase did
// not report as the phases ask: a fault of the configuration, retried and
// then held for the Owner, never charged to the candidate.
func reportUnusable(why string) *core.Error {
	return core.NewError(core.CodeUnavailable, why+"; the build command must speak the phased protocol "+
		"(SISF_PHASE test, build and report; see cmd/supervisor/stages/image_builders.go)")
}
