// Package appbundle builds and checks the content bundle of a managed
// application: the document root a revision serves, assembled from the
// candidate's repository and the archives its lockfile pins.
//
// This is trusted packaging only. Assemble runs in factory code, never in a
// candidate's sandbox, and nothing it downloads or copies is ever executed:
// archives are verified, unpacked and repacked as data. Every archive entry
// and repository source that could escape its target, link elsewhere or be
// anything but a plain file or directory is refused, never sanitised, and
// every size is bounded while it is read, not only as an archive claims it.
//
// A sha256 in the lockfile proves integrity, not authenticity. The lockfile
// sits in a repository the candidate edits, so a candidate can pin any
// archive it likes with a matching sha256. What a changed lockfile may
// introduce is judged by Classify on the factory side, against the
// Owner-approved baseline lockfile, and released according to
// managedapp.Policy.Decide.
//
// Verify is the supervisor's side: it reads a bundle tar that a candidate
// could have influenced, refuses anything but plain files and directories
// with safe names and bounded sizes, and returns the digest of the exact
// bytes it read.
package appbundle
