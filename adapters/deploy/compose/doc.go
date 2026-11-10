// Package compose is the deployment backend of a Docker Compose factory. It
// runs each revision of a unit as a container next to the others, and the
// edge divides traffic between two of them: blue-green when the share is
// 100, canary below it.
//
// The backend reaches Docker only through the endpoint its configuration
// names, which is the supervisor-only socket proxy. It never mounts,
// discovers or defaults a socket, and the containers it creates get no
// socket, no privileges and no host path.
//
// A revision is judged by what runs: the label of the container and the
// revision the application itself reports at /healthz. When those two
// disagree, or either is missing, the revision is not the one that was asked
// for, whatever an earlier call answered.
package compose
