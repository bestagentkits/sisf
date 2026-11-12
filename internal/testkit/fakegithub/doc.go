// Package fakegithub is an in-memory stand-in for the parts of GitHub the
// factory uses: issues, labels, comments, pull requests, check runs,
// releases and webhook delivery. It is an http.Handler, so tests mount it
// with httptest.NewServer, and Serve runs it standalone for the simulation
// profile.
//
// It exists to make failure repeatable. Webhook deliveries can be dropped,
// duplicated and reordered on demand, recorded deliveries can be redelivered
// through the same API GitHub offers, and API calls can be made to fail with
// a rate limit or a server error. Time is a counter that advances one second
// per change unless a clock is supplied, so no test needs to sleep.
//
// It never contacts the real GitHub and holds no real credentials.
package fakegithub
