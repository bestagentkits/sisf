package healthprobe_test

import "net/http"

// testClient sends test requests on a transport of its own. Closing an httptest
// server closes the idle connections of the default transport, which would break
// a request that a parallel test sends through it.
var testClient = &http.Client{Transport: &http.Transport{}}
