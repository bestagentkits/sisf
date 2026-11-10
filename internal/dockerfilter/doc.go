// Package dockerfilter is the only way any part of the factory reaches the
// Docker API. It is an HTTP reverse proxy in front of the Docker socket that
// lets through exactly what the docker execution adapter needs to run
// ephemeral runner containers, and nothing else. Everything it does not
// recognise is refused: deny by default.
//
// # What passes
//
//   - GET or HEAD /vX/_ping, so the adapter can tell the daemon is up.
//   - GET /vX/info, answered by the filter itself with the names of the
//     allowed container runtimes the host has and nothing more, so the
//     adapter can tell whether gVisor (runsc) is there. No other system
//     information leaves the daemon.
//   - POST /vX/containers/create, only for a body that names an allowed
//     image by digest, carries the factory's runner label, joins only the
//     sandbox network, runs as a user other than root on a read-only root
//     file system with every capability dropped, no-new-privileges set and
//     memory, CPU and process limits, and has no bind, mount, volume,
//     device, host namespace, published port or privilege of any kind. The
//     body is checked strictly (no duplicate keys in any letter case, no
//     unknown keys, no keys in another letter case) and the filter forwards
//     its own re-encoding of what it checked, never the client's bytes.
//   - start, stop, wait, logs, remove and inspect of a container that
//     carries the runner label. The filter inspects the container itself
//     first and acts on the full container ID it read, so a name or an ID
//     prefix cannot be swapped for another container in between.
//
// Exec, attach, archive, commit, export, update, rename, image, build, pull,
// volume, network, swarm, service, secret, config, plugin, events, system
// and every unknown endpoint are refused, and so is every API version not
// configured, every request whose path is encoded, not clean or in absolute
// form, every protocol upgrade, and every body that is chunked, oversized or
// sent to an endpoint that takes none.
package dockerfilter
