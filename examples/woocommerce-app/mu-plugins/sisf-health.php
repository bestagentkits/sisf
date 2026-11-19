<?php
/**
 * Plugin Name: SISF health
 * Description: Answers GET /healthz with the revision this container was
 * started as, which is how the SISF supervisor observes what is running.
 * It answers 503 when the database cannot be read.
 */
if ( isset( $_SERVER['REQUEST_URI'] ) && '/healthz' === strtok( $_SERVER['REQUEST_URI'], '?' ) ) {
	global $wpdb;
	$sisf_db_ok = ( '1' === (string) $wpdb->get_var( 'SELECT 1' ) );
	status_header( $sisf_db_ok ? 200 : 503 );
	header( 'Content-Type: application/json' );
	header( 'Cache-Control: no-store' );
	echo wp_json_encode(
		array(
			'status'   => $sisf_db_ok ? 'ok' : 'down',
			'revision' => (string) getenv( 'SISF_REVISION' ),
			'database' => $sisf_db_ok ? 'ok' : 'unreachable',
		)
	);
	exit;
}
