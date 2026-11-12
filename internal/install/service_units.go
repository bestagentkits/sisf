package install

import (
	"bytes"
	"os"
	"text/template"

	"github.com/bestagentkits/sisf/templates"
)

// unitData fills the launchd and systemd templates.
type unitData struct{ Label, FactoryID, Sisf, Dir string }

// writeServiceUnits renders the service-manager units into the install's
// service directory. They are only generated: copying one into place and
// enabling it is the Owner's step, so init changes nothing outside the
// install directory.
func (in *Installer) writeServiceUnits(o Options) error {
	data := unitData{Label: "sh.sisf." + string(o.FactoryID), FactoryID: string(o.FactoryID), Sisf: in.Sisf, Dir: o.Dir}
	for _, p := range []string{data.Sisf, data.Dir} {
		if !pathPattern.MatchString(p) {
			// A path with a space or an XML character cannot be written into
			// a unit safely; the units are an extra, so skip rather than fail.
			in.say("note: service units were not generated because %q has characters a unit cannot carry\n", p)
			return nil
		}
	}
	if err := os.MkdirAll(in.Layout.ServiceDir(), 0o755); err != nil {
		return unavailablef(err, "cannot create %s", in.Layout.ServiceDir())
	}
	for _, f := range []struct{ src, dst string }{
		{"standalone/sisf.service", data.Label + ".service"},
		{"standalone/sisf.plist", data.Label + ".plist"},
	} {
		tpl, err := template.New("unit").Option("missingkey=error").ParseFS(templates.FS, f.src)
		if err != nil {
			return invalidf("the service template %s cannot be read: %v", f.src, err)
		}
		var out bytes.Buffer
		if err := tpl.ExecuteTemplate(&out, tplName(f.src), data); err != nil {
			return invalidf("the service template %s cannot be filled: %v", f.src, err)
		}
		if err := os.WriteFile(in.Layout.ServiceDir()+"/"+f.dst, out.Bytes(), 0o644); err != nil {
			return unavailablef(err, "cannot write %s", f.dst)
		}
	}
	return nil
}

func tplName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}
