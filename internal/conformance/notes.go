package conformance

// Notes returns the lines shown beside the verdict of a conformant
// manifest, in a fixed order: whether the factory may publish by itself,
// each way an acknowledgement of the Owner reduces the assurance, and each
// accepted setting that carries a risk. A caller prints all of them, so that
// "conformant" is never read as more than the manifest promises.
func (r Report) Notes() []string {
	selfUpdate := "self-update: enabled, the supervisor boundary holds"
	if !r.SelfUpdate {
		selfUpdate = "self-update: disabled, no trusted supervisor is declared"
	}
	notes := []string{selfUpdate}
	if r.TrustedCodeOnly {
		notes = append(notes, "assurance: reduced, trusted code only: the execution backend does not isolate agent runs or enforce the egress policy")
	}
	if r.SameModelRoute {
		notes = append(notes, "assurance: reduced, same model route: QA runs on the coding agent's provider and model and shares its blind spots; every QA receipt records same_model_route and policy may require the Owner's approval in place of self-publishing")
	}
	for _, risk := range r.Risks {
		notes = append(notes, "risk: "+risk.Path+": "+risk.Message)
	}
	return notes
}
