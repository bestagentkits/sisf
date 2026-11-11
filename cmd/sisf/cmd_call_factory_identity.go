package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/pkg/core"
)

// verifyFactory proves, before a token or a login request is sent, that the
// control server at the URL is the factory of the install in the SISF home.
// The default URL is a fixed loopback port, and a second factory on the same
// host may answer there; it must never receive this install's token. With no
// install in the SISF home, such as on a laptop that talks to a remote
// factory, there is nothing to compare with and nothing is checked.
func (c *connFlags) verifyFactory(ctx context.Context, client *apiClient) error {
	home, err := resolveDir(c.dir)
	if err != nil {
		return err
	}
	m, err := (install.Layout{Dir: home}).LoadManifest()
	if err != nil {
		return nil // no install here: a remote factory, named by its URL
	}
	anonymous := *client
	anonymous.token = "" // the health route is open; the token waits for the proof
	status, raw, _, err := anonymous.do(ctx, http.MethodGet, "/healthz", nil)
	if err != nil {
		return notRunning(client.base, home, err)
	}
	var health struct {
		FactoryID core.FactoryID `json:"factory_id"`
	}
	_ = json.Unmarshal(raw, &health) // an unreadable answer names no factory
	switch {
	case health.FactoryID == "":
		return core.NewError(core.CodeConflict, "the server at "+client.base+" does not say which factory it is (HTTP "+strconv.Itoa(status)+"), so nothing was sent to it",
			core.NextAction{Action: "sisf doctor --dir " + home, Description: "see what answers on this install's ports"})
	case health.FactoryID != m.FactoryID:
		return core.NewError(core.CodeConflict, "the server at "+client.base+" is factory "+string(health.FactoryID)+", not "+string(m.FactoryID)+" of the install in "+home+"; nothing was sent to it",
			core.NextAction{Action: "--url", Description: "name this factory's control URL, or stop the other factory that holds its port"},
			core.NextAction{Action: "sisf doctor --dir " + home, Description: "see what answers on this install's ports"})
	}
	return nil
}

// notRunning is the failure of a factory that does not answer, with the
// steps that help: start it, or point the CLI at the right URL.
// An empty home leaves the install out of the commands.
func notRunning(base, home string, cause error) error {
	at := ""
	if home != "" {
		at = " --dir " + home
	}
	return core.NewError(core.CodeUnavailable, "the factory at "+base+" cannot be reached",
		core.NextAction{Action: "sisf up" + at, Description: "start the factory, if it is installed on this host"},
		core.NextAction{Action: "sisf doctor" + at, Description: "see which of its units do not answer"},
		core.NextAction{Action: "--url or $" + controlURLEnv, Description: "name the control URL of a factory that runs elsewhere"},
	).WithCause(cause)
}
