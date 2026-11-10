package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

// Client speaks the admin socket protocol of one supervisor:
//
//	GET  /status          Status
//	POST /emergency-stop  EmergencyStopRequest -> EmergencyStopReport
//	GET  /emergency-stop  FreezeStatus
//	POST /rollback        RollbackRequest -> RollbackReport
//
// A refusal comes back as {"code","message"} and is returned as a
// *core.Error with that code. The address is held to the listener's rule,
// so a client cannot be pointed at another machine.
type Client struct{ addr string }

// NewClient returns a client of the supervisor listening on addr:
// unix:/absolute/path or a loopback host:port.
func NewClient(addr string) (*Client, error) {
	if _, err := parseAdminAddr(addr); err != nil {
		return nil, err
	}
	return &Client{addr: addr}, nil
}

// Status asks for the supervisor's status.
func (c *Client) Status(ctx context.Context) (Status, error) { return FetchStatus(ctx, c.addr) }

// EmergencyStop asks the supervisor to stop the factory.
func (c *Client) EmergencyStop(ctx context.Context, req EmergencyStopRequest) (EmergencyStopReport, error) {
	if err := req.Validate(); err != nil {
		return EmergencyStopReport{}, err
	}
	var report EmergencyStopReport
	return report, c.post(ctx, EmergencyStopPath, req, &report)
}

// FreezeStatus asks whether an emergency stop is in force.
func (c *Client) FreezeStatus(ctx context.Context) (FreezeStatus, error) {
	var status FreezeStatus
	return status, callAdmin(ctx, c.addr, http.MethodGet, EmergencyStopPath, nil, &status)
}

// RequestRollback asks the supervisor to restore a unit to its last known
// good release under the Owner's approval approvalRef.
func (c *Client) RequestRollback(ctx context.Context, req RollbackRequest) (RollbackReport, error) {
	if err := req.Validate(); err != nil {
		return RollbackReport{}, err
	}
	var report RollbackReport
	return report, c.post(ctx, RollbackPath, req, &report)
}

// post sends one JSON request and decodes the answer.
func (c *Client) post(ctx context.Context, path string, req, out any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return invalid("the admin request cannot be encoded").WithCause(err)
	}
	return callAdmin(ctx, c.addr, http.MethodPost, path, bytes.NewReader(body), out)
}
