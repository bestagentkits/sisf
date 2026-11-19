package detect

// dependencyProfile is what adoption knows about a dependency's image: how to
// tell it is ready, and how to dump and load its data. The commands run
// inside the dependency, so they read its own credentials from its own
// secret files and never pass a password on a command line.
type dependencyProfile struct {
	ready, backup, restore []string
}

// mysqlFamily dumps one database consistently; the password reaches the
// client through MYSQL_PWD, read from the secret file in the same shell.
func mysqlFamily(dump, client, prefix string) dependencyProfile {
	auth := `MYSQL_PWD="$(cat "$` + prefix + `_PASSWORD_FILE")" `
	return dependencyProfile{
		ready: []string{"sh", "-c", auth + client + ` -u"$` + prefix + `_USER" -e "SELECT 1" "$` + prefix + `_DATABASE" >/dev/null`},
		backup: []string{"sh", "-c", "set -eu; " + auth + "exec " + dump +
			` --single-transaction --quick --routines --triggers --hex-blob -u"$` + prefix + `_USER" "$` + prefix + `_DATABASE"`},
		restore: []string{"sh", "-c", "set -eu; " + auth + "exec " + client + ` -u"$` + prefix + `_USER" "$` + prefix + `_DATABASE"`},
	}
}

// dependencyProfiles are keyed by image name (see imageName).
var dependencyProfiles = map[string]dependencyProfile{
	"mariadb": mysqlFamily("mariadb-dump", "mariadb", "MARIADB"),
	"mysql":   mysqlFamily("mysqldump", "mysql", "MYSQL"),
	"postgres": {
		ready:   []string{"sh", "-c", `pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"`},
		backup:  []string{"sh", "-c", `set -eu; PGPASSWORD="$(cat "$POSTGRES_PASSWORD_FILE")" exec pg_dump --clean --if-exists -U "$POSTGRES_USER" -d "$POSTGRES_DB"`},
		restore: []string{"sh", "-c", `set -eu; PGPASSWORD="$(cat "$POSTGRES_PASSWORD_FILE")" exec psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"`},
	},
}
