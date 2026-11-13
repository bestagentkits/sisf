<?php
/**
 * WordPress configuration of the managed shop. Every value comes from the
 * environment or a secret file, so the same bundle runs in every revision.
 */
function sisf_env( $name, $default = '' ) {
	$file = getenv( $name . '_FILE' );
	if ( $file && is_readable( $file ) ) {
		return rtrim( (string) file_get_contents( $file ), "\r\n" );
	}
	$value = getenv( $name );
	return false === $value ? $default : $value;
}

define( 'DB_NAME', sisf_env( 'WORDPRESS_DB_NAME', 'shop' ) );
define( 'DB_USER', sisf_env( 'WORDPRESS_DB_USER', 'shop' ) );
define( 'DB_PASSWORD', sisf_env( 'WORDPRESS_DB_PASSWORD' ) );
define( 'DB_HOST', sisf_env( 'WORDPRESS_DB_HOST', 'db' ) );
define( 'DB_CHARSET', 'utf8mb4' );
define( 'DB_COLLATE', '' );

// Every key and salt is derived from one secret, never stored in the bundle.
$sisf_secret = sisf_env( 'WORDPRESS_SECRET' );
foreach ( array( 'AUTH_KEY', 'SECURE_AUTH_KEY', 'LOGGED_IN_KEY', 'NONCE_KEY', 'AUTH_SALT', 'SECURE_AUTH_SALT', 'LOGGED_IN_SALT', 'NONCE_SALT' ) as $sisf_key ) {
	define( $sisf_key, hash_hmac( 'sha256', $sisf_key, $sisf_secret ) );
}
unset( $sisf_secret, $sisf_key );

$table_prefix = 'wp_';

if ( sisf_env( 'WORDPRESS_HOME' ) ) {
	define( 'WP_HOME', sisf_env( 'WORDPRESS_HOME' ) );
	define( 'WP_SITEURL', sisf_env( 'WORDPRESS_HOME' ) );
}

// The code is a read-only bundle: nothing installs or edits it at run time.
define( 'DISALLOW_FILE_MODS', true );
define( 'DISALLOW_FILE_EDIT', true );
define( 'AUTOMATIC_UPDATER_DISABLED', true );
define( 'WP_DEBUG', false );
define( 'WP_DEBUG_LOG', false );

if ( ! defined( 'ABSPATH' ) ) {
	define( 'ABSPATH', __DIR__ . '/' );
}
require_once ABSPATH . 'wp-settings.php';
